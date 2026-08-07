// Package web serves the site: the public page, the login flows and the admin UI.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"github.com/mrcat71/waitformeet/internal/auth"
	"github.com/mrcat71/waitformeet/internal/config"
	"github.com/mrcat71/waitformeet/internal/i18n"
	"github.com/mrcat71/waitformeet/internal/media"
	"github.com/mrcat71/waitformeet/internal/store"
	"github.com/mrcat71/waitformeet/internal/users"
	"github.com/mrcat71/waitformeet/internal/weather"
)

const (
	// loginBurst and loginWindow throttle password guessing per address.
	loginBurst  = 8
	loginWindow = 5 * time.Minute
)

// Server holds everything the handlers need.
type Server struct {
	cfg      *config.Config
	store    *store.Store
	users    *users.Service
	sessions *auth.Manager
	oidc     *auth.OIDC
	media    *media.Store
	weather  *weather.Client
	bundle   *i18n.Bundle
	log      *slog.Logger

	mux       *http.ServeMux
	static    http.Handler
	templates map[string]*template.Template
	limiter   *auth.RateLimiter

	// assetVersion busts browser caches when a new build ships.
	assetVersion string
}

// Options are the collaborators New needs. They are built in main so that a
// failure to reach the identity provider stops startup rather than the first login.
type Options struct {
	Config   *config.Config
	Store    *store.Store
	Users    *users.Service
	Sessions *auth.Manager
	OIDC     *auth.OIDC
	Bundle   *i18n.Bundle
	Logger   *slog.Logger
	// Version stamps asset URLs.
	Version string
}

// New builds the server and registers every route.
func New(opts Options) (*Server, error) {
	static, err := staticHandler()
	if err != nil {
		return nil, err
	}
	templates, err := parseTemplates()
	if err != nil {
		return nil, err
	}
	mediaStore, err := media.NewStore(opts.Config.DataDir)
	if err != nil {
		return nil, err
	}

	s := &Server{
		cfg:          opts.Config,
		store:        opts.Store,
		users:        opts.Users,
		sessions:     opts.Sessions,
		oidc:         opts.OIDC,
		media:        mediaStore,
		weather:      weather.New(),
		bundle:       opts.Bundle,
		log:          opts.Logger,
		mux:          http.NewServeMux(),
		static:       static,
		templates:    templates,
		limiter:      auth.NewRateLimiter(loginBurst, loginWindow),
		assetVersion: opts.Version,
	}
	s.routes()
	return s, nil
}

// Handler returns the fully wrapped handler, middleware included.
//
// Order matters, outermost first: security headers apply to everything including
// panics; the panic recovery must sit outside the logger so a panic is still
// logged as a request; the session lookup has to run before any handler asks who
// the visitor is.
func (s *Server) Handler() http.Handler {
	return s.securityHeaders(
		s.recoverPanic(
			s.logRequests(
				s.withSession(
					s.withLocaleCookie(s.mux)))))
}

func (s *Server) routes() {
	s.mux.Handle("GET "+StaticPrefix, s.static)

	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /readyz", s.handleReadyz)

	s.mux.HandleFunc("GET /{$}", s.handleHome)

	// Always reachable: link previews, the calendar export, and the pieces that
	// make the site installable on a phone.
	s.mux.HandleFunc("GET /og.png", s.handleOGImage)
	s.mux.HandleFunc("GET /calendar.ics", s.handleCalendar)
	s.mux.HandleFunc("GET /manifest.webmanifest", s.handleManifest)
	s.mux.HandleFunc("GET /sw.js", s.handleServiceWorker)
	s.mux.HandleFunc("GET /robots.txt", s.handleRobots)
	s.mux.HandleFunc("GET /metrics", s.handleMetrics)

	s.mux.HandleFunc("GET /login", s.handleLoginForm)
	s.mux.HandleFunc("POST /login", s.handleLoginSubmit)
	s.mux.HandleFunc("POST /logout", s.handleLogout)
	s.mux.HandleFunc("POST /auth/oidc/start", s.handleOIDCStart)
	s.mux.HandleFunc("GET /auth/oidc/callback", s.handleOIDCCallback)
	s.mux.HandleFunc("GET /invite", s.handleInviteForm)
	s.mux.HandleFunc("POST /invite", s.handleInviteSubmit)

	// Notes and photos follow their section visibility, which admins control.
	s.mux.HandleFunc("GET /notes", s.requireSection(notesSection, s.handleNotes))
	s.mux.HandleFunc("POST /notes", s.requireSignedIn(s.handleNoteCreate))
	s.mux.HandleFunc("POST /notes/{id}/delete", s.requireSignedIn(s.handleNoteDelete))
	s.mux.HandleFunc("GET /gallery", s.requireSection(gallerySection, s.handleGallery))
	s.mux.HandleFunc("POST /gallery", s.requireSignedIn(s.handleGalleryUpload))
	s.mux.HandleFunc("POST /gallery/{id}/delete", s.requireSignedIn(s.handleGalleryDelete))
	s.mux.HandleFunc("GET /media/{kind}/{name}", s.requireSection(gallerySection, s.handleMedia))

	// Editing: anyone signed in.
	s.mux.HandleFunc("GET /admin", s.requireSignedIn(s.handleAdminContent))
	s.mux.HandleFunc("POST /admin", s.requireSignedIn(s.handleAdminContentSave))
	s.mux.HandleFunc("GET /admin/events", s.requireSignedIn(s.handleAdminEvents))
	s.mux.HandleFunc("POST /admin/events/main", s.requireSignedIn(s.handleAdminMainEvent))
	s.mux.HandleFunc("POST /admin/events/milestone", s.requireSignedIn(s.handleAdminMilestoneCreate))
	s.mux.HandleFunc("POST /admin/events/milestone/{id}", s.requireSignedIn(s.handleAdminMilestoneUpdate))
	s.mux.HandleFunc("POST /admin/events/milestone/{id}/delete", s.requireSignedIn(s.handleAdminMilestoneDelete))
	s.mux.HandleFunc("GET /admin/quotes", s.requireSignedIn(s.handleAdminQuotes))
	s.mux.HandleFunc("POST /admin/quotes", s.requireSignedIn(s.handleAdminQuoteCreate))
	s.mux.HandleFunc("POST /admin/quotes/{id}/delete", s.requireSignedIn(s.handleAdminQuoteDelete))

	// Managing people and deciding what is public: admins only.
	s.mux.HandleFunc("GET /admin/users", s.requireAdmin(s.handleAdminUsers))
	s.mux.HandleFunc("POST /admin/users/invite", s.requireAdmin(s.handleAdminInvite))
	s.mux.HandleFunc("POST /admin/users/{id}", s.requireAdmin(s.handleAdminUserUpdate))
	s.mux.HandleFunc("POST /admin/users/{id}/delete", s.requireAdmin(s.handleAdminUserDelete))
	s.mux.HandleFunc("POST /admin/invites/{id}/delete", s.requireAdmin(s.handleAdminInviteDelete))
	s.mux.HandleFunc("GET /admin/site", s.requireAdmin(s.handleAdminSite))
	s.mux.HandleFunc("POST /admin/site/visibility", s.requireAdmin(s.handleAdminVisibilitySave))
	s.mux.HandleFunc("GET /admin/export", s.requireAdmin(s.handleAdminExport))
	s.mux.HandleFunc("POST /admin/import", s.requireAdmin(s.handleAdminImport))

	// Anything not matched above is a 404 rendered in the visitor's language,
	// rather than the bare stdlib page.
	s.mux.HandleFunc("/", s.handleNotFound)
}

// withSession resolves the signed-in user once per request and puts them on the
// context, so no handler has to remember to look them up.
func (s *Server) withSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, sess, err := s.sessions.Current(r.Context(), r)
		switch {
		case err == nil:
			s.sessions.Touch(r.Context(), w, r, sess)
			r = r.WithContext(auth.WithUser(r.Context(), user, sess))
		case errorIsNoSession(err):
			// An anonymous visitor is the normal case, not a problem.
		default:
			// A database failure here must not silently downgrade someone to
			// anonymous; that would quietly hide their own content from them.
			s.serverError(w, r, err)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withLocaleCookie remembers an explicit ?lang choice.
func (s *Server) withLocaleCookie(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requested := r.URL.Query().Get(i18n.LocaleQuery); requested != "" {
			if matched := s.bundle.Match(requested); matched != "" {
				http.SetCookie(w, &http.Cookie{
					Name:     i18n.LocaleCookie,
					Value:    matched,
					Path:     "/",
					MaxAge:   int((365 * 24 * time.Hour).Seconds()),
					HttpOnly: true,
					Secure:   s.cfg.CookieSecure,
					SameSite: http.SameSiteLaxMode,
				})
			}
		}
		next.ServeHTTP(w, r)
	})
}

func errorIsNoSession(err error) bool {
	return errors.Is(err, auth.ErrNoSession)
}

// handleHealthz reports that the process is alive. It deliberately touches nothing
// else: a liveness probe that fails on a slow database restarts a pod that was
// merely busy.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReadyz reports that the server can actually serve traffic, which means the
// database answers.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
	defer cancel()

	if err := s.store.DB().PingContext(ctx); err != nil {
		s.log.WarnContext(ctx, "readiness check failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "unavailable",
			"reason": "database unreachable",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	// The response is already committed by this point, so a failed encode can only
	// be logged by the caller's middleware; there is nothing left to signal with.
	_ = json.NewEncoder(w).Encode(body)
}

// errorData renders the shared error page.
type errorData struct {
	*page
	Status  int
	Heading string
	Body    string
}

// renderError shows a translated error page, falling back to plain text if even
// the error page cannot be built.
func (s *Server) renderError(w http.ResponseWriter, r *http.Request, status int, titleKey, bodyKey string) {
	settings, err := s.store.Settings(r.Context())
	if err != nil {
		http.Error(w, http.StatusText(status), status)
		return
	}
	base, err := s.newPage(w, r, settings)
	if err != nil {
		http.Error(w, http.StatusText(status), status)
		return
	}
	base.NoIndex = true

	s.render(w, r, status, "error", &errorData{
		page:    base,
		Status:  status,
		Heading: base.T(titleKey),
		Body:    base.T(bodyKey),
	})
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	s.renderError(w, r, http.StatusNotFound, "error.not_found.title", "error.not_found.body")
}

func (s *Server) forbidden(w http.ResponseWriter, r *http.Request) {
	s.renderError(w, r, http.StatusForbidden, "error.forbidden.title", "error.forbidden.body")
}

// serverError logs the cause and shows a page that does not leak it.
func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.ErrorContext(r.Context(), "request failed",
		"error", err, "method", r.Method, "path", r.URL.Path)
	s.renderError(w, r, http.StatusInternalServerError, "error.server.title", "error.server.body")
}

// requireCSRF verifies the token on an unsafe request, rendering the error page and
// returning false when it fails.
func (s *Server) requireCSRF(w http.ResponseWriter, r *http.Request) bool {
	if err := s.sessions.VerifyCSRF(r); err != nil {
		s.log.WarnContext(r.Context(), "rejected a request with a bad CSRF token",
			"path", r.URL.Path, "ip", s.clientIP(r))
		s.renderError(w, r, http.StatusForbidden, "error.forbidden.title", "error.csrf")
		return false
	}
	return true
}
