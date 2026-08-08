package web

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"time"

	"github.com/mrcat71/waitformeet/internal/auth"
	"github.com/mrcat71/waitformeet/internal/i18n"
	"github.com/mrcat71/waitformeet/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

// page is the data every template receives. Per-page structs embed it, which also
// promotes the translation methods below.
type page struct {
	// printer is unexported so templates go through the T, N and Forms methods,
	// which read as {{.T "key"}} rather than {{.Printer.T "key"}}.
	printer *i18n.Printer

	Locale  string
	Locales []*i18n.Locale

	Settings *store.Settings
	User     *store.User
	IsAdmin  bool

	CSRFToken string
	// ServerNow anchors the browser's countdown to the server's clock, so two
	// people looking at the page see the same number even if one device's clock
	// is off.
	ServerNow time.Time

	// Title names this page and fills <title>. Individual pages overwrite it, so it
	// is not the name of the site.
	Title string
	// SiteTitle is the name of the site itself, for the header's home link. Kept
	// separate from Title so a page like the login form is not mistaken for the
	// site's own name.
	SiteTitle   string
	Description string
	// Error and Notice carry one-off messages, already translated.
	Error  string
	Notice string

	// ShowNotes and ShowGallery drive the navigation links: a section nobody may
	// see should not be advertised.
	ShowNotes   bool
	ShowGallery bool
	// NoIndex keeps a page out of search engines.
	NoIndex bool

	// SWURL is empty when the service worker should not be registered, which is
	// the case for pages behind a login.
	SWURL string
	// AssetVersion busts caches when a new build ships.
	AssetVersion string
	OIDCName     string
	LocalAuth    bool
}

// T renders a message: {{.T "nav.home"}}.
func (p *page) T(key string, args ...any) string { return p.printer.T(key, args...) }

// N renders a message with a count, choosing the right plural form for the locale.
func (p *page) N(key string, count int, args ...any) string {
	return p.printer.N(key, count, args...)
}

// Forms returns every plural form of a key, for the browser to choose between as a
// counter ticks.
func (p *page) Forms(key string, args ...any) map[string]string {
	return p.printer.Forms(key, args...)
}

// templateFuncs are available inside every template.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		// jsonAttr renders a value as JSON for a data- attribute. html/template
		// escapes the result for attribute context, and the browser undoes that
		// before JSON.parse sees it.
		"jsonAttr": func(v any) (string, error) {
			encoded, err := json.Marshal(v)
			if err != nil {
				return "", fmt.Errorf("web: encode template value as JSON: %w", err)
			}
			return string(encoded), nil
		},
		// rfc3339 renders an instant in the form Date.parse understands.
		"rfc3339": func(t time.Time) string {
			return t.UTC().Format(time.RFC3339)
		},
		// accent turns a validated hex colour into a CSS custom property value.
		// The store rejects anything that is not a hex colour, so this cannot
		// smuggle arbitrary CSS in.
		"accent": func(colour string) template.CSS {
			if !isHexColour(colour) {
				return template.CSS("#e5687f")
			}
			return template.CSS(colour)
		},
		// dateInput renders an optional instant for an <input type="date">.
		"dateInput": func(t *time.Time) string {
			if t == nil || t.IsZero() {
				return ""
			}
			return t.UTC().Format("2006-01-02")
		},
		// newEvent supplies a blank event for the "add a date" form, pre-filled
		// with the timezone the other forms on the page are using.
		"newEvent": func(timezone string) eventForm {
			return eventForm{Timezone: timezone}
		},
		"dict": func(pairs ...any) (map[string]any, error) {
			if len(pairs)%2 != 0 {
				return nil, fmt.Errorf("web: dict needs an even number of arguments, got %d", len(pairs))
			}
			out := make(map[string]any, len(pairs)/2)
			for i := 0; i < len(pairs); i += 2 {
				key, ok := pairs[i].(string)
				if !ok {
					return nil, fmt.Errorf("web: dict keys must be strings, got %T", pairs[i])
				}
				out[key] = pairs[i+1]
			}
			return out, nil
		},
	}
}

// isHexColour mirrors the store's validation. Duplicated deliberately: the template
// layer must not assume the database only ever holds validated values.
func isHexColour(s string) bool {
	if len(s) != 4 && len(s) != 7 {
		return false
	}
	if s[0] != '#' {
		return false
	}
	for _, r := range s[1:] {
		isDigit := r >= '0' && r <= '9'
		isHex := (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
		if !isDigit && !isHex {
			return false
		}
	}
	return true
}

// parseTemplates builds one template set per page, each including the shared
// layout and partials.
func parseTemplates() (map[string]*template.Template, error) {
	shared := []string{"templates/layout.html", "templates/partials.html"}

	pages := []string{
		"home", "login", "invite", "error", "notes", "gallery",
		"admin_content", "admin_events", "admin_quotes", "admin_users", "admin_site",
	}
	out := make(map[string]*template.Template, len(pages))

	for _, name := range pages {
		files := append([]string{"templates/" + name + ".html"}, shared...)
		tmpl, err := template.New(name+".html").
			Funcs(templateFuncs()).
			ParseFS(templateFS, files...)
		if err != nil {
			return nil, fmt.Errorf("web: parse template %s: %w", name, err)
		}
		out[name] = tmpl
	}
	return out, nil
}

// render writes a page.
//
// The template is executed into a buffer first: a template error halfway through
// would otherwise leave a half-written 200 response that cannot be turned into an
// error page.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	tmpl, ok := s.templates[name]
	if !ok {
		s.log.ErrorContext(r.Context(), "unknown template", "name", name)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "layout", data); err != nil {
		s.log.ErrorContext(r.Context(), "rendering template", "name", name, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if _, err := buf.WriteTo(w); err != nil {
		s.log.WarnContext(r.Context(), "writing response", "error", err)
	}
}

// newPage assembles the data common to every page.
func (s *Server) newPage(w http.ResponseWriter, r *http.Request, settings *store.Settings) (*page, error) {
	ctx := r.Context()

	locale := s.bundle.Resolve(r, settings.DefaultLocale)
	printer := s.bundle.Printer(locale)

	token, err := s.sessions.CSRFToken(w, r)
	if err != nil {
		return nil, err
	}

	title := settings.SiteTitle
	if title == "" {
		title = printer.T("app.default_title")
	}

	return &page{
		printer:      printer,
		Locale:       locale,
		Locales:      s.bundle.Locales(),
		Settings:     settings,
		User:         auth.UserFrom(ctx),
		IsAdmin:      auth.IsAdmin(ctx),
		CSRFToken:    token,
		ServerNow:    time.Now().UTC(),
		Title:        title,
		SiteTitle:    title,
		Description:  printer.T("meta.description"),
		AssetVersion: s.assetVersion,
		OIDCName:     s.oidc.DisplayName(),
		LocalAuth:    s.cfg.LocalAuthEnabled,
	}, nil
}
