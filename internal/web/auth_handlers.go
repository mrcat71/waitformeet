package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/mrcat71/waitformeet/internal/auth"
	"github.com/mrcat71/waitformeet/internal/i18n"
	"github.com/mrcat71/waitformeet/internal/store"
	"github.com/mrcat71/waitformeet/internal/users"
)

type loginData struct {
	*page
	Email    string
	ReturnTo string
}

type inviteData struct {
	*page
	Token          string
	Email          string
	Invalid        bool
	MinPasswordLen int
}

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if auth.IsSignedIn(r.Context()) {
		http.Redirect(w, r, safeReturnTo(r.URL.Query().Get("return_to")), http.StatusSeeOther)
		return
	}
	s.renderLogin(w, r, http.StatusOK, "", "", r.URL.Query().Get("return_to"))
}

func (s *Server) renderLogin(w http.ResponseWriter, r *http.Request, status int, errMsg, email, returnTo string) {
	settings, err := s.store.Settings(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	base, err := s.newPage(w, r, settings)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	base.NoIndex = true
	base.Error = errMsg
	base.Title = base.T("auth.sign_in.heading")

	s.render(w, r, status, "login", &loginData{
		page:     base,
		Email:    email,
		ReturnTo: safeReturnTo(returnTo),
	})
}

func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	if !s.cfg.LocalAuthEnabled {
		s.forbidden(w, r)
		return
	}

	ctx := r.Context()
	email := store.NormalizeEmail(r.PostFormValue("email"))
	password := r.PostFormValue("password")
	returnTo := r.PostFormValue("return_to")

	// Throttle by address rather than by account, so guessing many accounts from
	// one place is limited just as much as hammering a single one.
	key := s.clientIP(r)
	if allowed, retryAfter := s.limiter.Allow(key); !allowed {
		seconds := int(retryAfter.Seconds()) + 1
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		s.renderLogin(w, r, http.StatusTooManyRequests,
			s.printerFor(r).T("auth.error.throttled", "seconds", seconds), email, returnTo)
		return
	}

	user, err := s.store.UserByEmail(ctx, email)
	if errors.Is(err, store.ErrNotFound) {
		// Spend the same time as a real comparison so that the response time does
		// not reveal whether the address exists.
		auth.DummyVerify(password)
		s.rejectLogin(w, r, email, returnTo)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	if user.Disabled {
		auth.DummyVerify(password)
		s.rejectLogin(w, r, email, returnTo)
		return
	}
	if err := auth.VerifyPassword(user.PasswordHash, password); err != nil {
		s.rejectLogin(w, r, email, returnTo)
		return
	}

	if err := s.sessions.StartSession(ctx, w, r, user.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.limiter.Reset(key)
	s.log.InfoContext(ctx, "signed in", "user_id", user.ID, "method", "password")

	http.Redirect(w, r, safeReturnTo(returnTo), http.StatusSeeOther)
}

// rejectLogin reports a failure without saying which part was wrong.
func (s *Server) rejectLogin(w http.ResponseWriter, r *http.Request, email, returnTo string) {
	s.log.InfoContext(r.Context(), "failed sign-in", "email", email, "ip", s.clientIP(r))
	s.renderLogin(w, r, http.StatusUnauthorized,
		s.printerFor(r).T("auth.error.credentials"), email, returnTo)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	if err := s.sessions.EndSession(r.Context(), w, r); err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleOIDCStart(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	if s.oidc == nil {
		s.forbidden(w, r)
		return
	}

	target, err := s.oidc.Start(w, safeReturnTo(r.PostFormValue("return_to")))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if s.oidc == nil {
		s.forbidden(w, r)
		return
	}

	claims, returnTo, err := s.oidc.Complete(ctx, w, r)
	if err != nil {
		// Provider-side failures are expected in normal use (a cancelled login, a
		// stale tab), so they are logged at info and shown as a retry prompt.
		s.log.InfoContext(ctx, "single sign-on did not complete", "error", err)
		s.renderLogin(w, r, http.StatusUnauthorized, s.printerFor(r).T("auth.error.sso"), "", "")
		return
	}

	user, err := s.users.ResolveOIDC(ctx, s.cfg.OIDC, claims)
	if errors.Is(err, auth.ErrOIDCNotAllowed) {
		s.log.InfoContext(ctx, "refused an identity with no account here",
			"subject", claims.Subject, "email", claims.Email)
		s.renderLogin(w, r, http.StatusForbidden, s.printerFor(r).T("auth.error.not_allowed"), "", "")
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if user.Disabled {
		s.renderLogin(w, r, http.StatusForbidden, s.printerFor(r).T("auth.error.not_allowed"), "", "")
		return
	}

	if err := s.sessions.StartSession(ctx, w, r, user.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.log.InfoContext(ctx, "signed in", "user_id", user.ID, "method", "oidc")

	http.Redirect(w, r, safeReturnTo(returnTo), http.StatusSeeOther)
}

func (s *Server) handleInviteForm(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	s.renderInvite(w, r, http.StatusOK, token, "")
}

func (s *Server) renderInvite(w http.ResponseWriter, r *http.Request, status int, token, errMsg string) {
	ctx := r.Context()

	settings, err := s.store.Settings(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	base, err := s.newPage(w, r, settings)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	base.NoIndex = true
	base.Error = errMsg
	base.Title = base.T("auth.invite.heading")

	data := &inviteData{
		page:           base,
		Token:          token,
		MinPasswordLen: auth.MinPasswordLen,
	}

	inv, err := s.store.InviteByTokenHash(ctx, users.HashInviteToken(token))
	switch {
	case errors.Is(err, store.ErrNotFound):
		data.Invalid = true
	case err != nil:
		s.serverError(w, r, err)
		return
	default:
		if usableErr := inv.Usable(s.store.Now()); usableErr != nil {
			data.Invalid = true
		} else {
			data.Email = inv.Email
		}
	}

	if data.Invalid {
		status = http.StatusNotFound
	}
	s.render(w, r, status, "invite", data)
}

func (s *Server) handleInviteSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}

	ctx := r.Context()
	token := r.PostFormValue("token")
	password := r.PostFormValue("password")
	confirm := r.PostFormValue("confirm")
	printer := s.printerFor(r)

	if password != confirm {
		s.renderInvite(w, r, http.StatusBadRequest, token, printer.T("auth.invite.mismatch"))
		return
	}

	user, err := s.users.Redeem(ctx, token, password)
	switch {
	case errors.Is(err, auth.ErrPasswordTooShort):
		s.renderInvite(w, r, http.StatusBadRequest, token,
			printer.T("auth.password_hint", "min", auth.MinPasswordLen))
		return
	case errors.Is(err, auth.ErrPasswordTooLong):
		s.renderInvite(w, r, http.StatusBadRequest, token, err.Error())
		return
	case errors.Is(err, store.ErrNotFound),
		errors.Is(err, store.ErrInviteUsed),
		errors.Is(err, store.ErrInviteExpired):
		s.renderInvite(w, r, http.StatusNotFound, token, printer.T("auth.invite.invalid"))
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}

	if err := s.sessions.StartSession(ctx, w, r, user.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.log.InfoContext(ctx, "account created from an invitation", "user_id", user.ID)

	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// printerFor builds a translator for a request outside the page pipeline, used for
// error messages produced before the page data exists.
func (s *Server) printerFor(r *http.Request) *i18n.Printer {
	return s.bundle.Printer(s.bundle.Resolve(r, ""))
}

// safeReturnTo keeps a redirect target on this site.
//
// Without this check, /login?return_to=https://evil.example would turn the login
// form into an open redirect, which is exactly the shape phishing links want.
func safeReturnTo(target string) string {
	if target == "" {
		return "/"
	}
	// Reject anything that could be read as another origin, including the
	// scheme-relative //host form and backslash variants some parsers accept.
	if strings.HasPrefix(target, "//") || strings.HasPrefix(target, `/\`) {
		return "/"
	}
	parsed, err := url.Parse(target)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/") {
		return "/"
	}
	return parsed.RequestURI()
}
