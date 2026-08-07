package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mrcat71/waitformeet/internal/auth"
	"github.com/mrcat71/waitformeet/internal/config"
	"github.com/mrcat71/waitformeet/internal/i18n"
	"github.com/mrcat71/waitformeet/internal/store"
	"github.com/mrcat71/waitformeet/internal/users"
)

// testServer builds a complete server against a temporary database.
type testServer struct {
	*Server
	store *store.Store
	http  *httptest.Server
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	ctx := context.Background()

	dataDir := t.TempDir()
	st, err := store.Open(ctx, dataDir)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { st.Close() })

	base, err := url.Parse("http://wait.test")
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	cfg := &config.Config{
		ListenAddr:       ":0",
		BaseURL:          base,
		DataDir:          dataDir,
		SessionTTL:       time.Hour,
		CookieSecure:     false,
		SessionSecret:    []byte("a-test-secret-that-is-long-enough-32"),
		LocalAuthEnabled: true,
		SeedMode:         config.SeedNever,
		MaxUploadBytes:   1 << 20,
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bundle, err := i18n.Load(log)
	if err != nil {
		t.Fatalf("i18n.Load() error = %v", err)
	}

	srv, err := New(Options{
		Config: cfg,
		Store:  st,
		Users:  users.NewService(st, log),
		Sessions: auth.NewManager(st, auth.Config{
			SessionTTL:   cfg.SessionTTL,
			CookieSecure: cfg.CookieSecure,
			Secret:       cfg.SessionSecret,
		}, log),
		Bundle:  bundle,
		Logger:  log,
		Version: "test",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	return &testServer{Server: srv, store: st, http: ts}
}

// signIn creates a user with a live session and returns a client that carries it.
func (ts *testServer) signIn(t *testing.T, email string, isAdmin bool) *http.Client {
	t.Helper()
	ctx := context.Background()

	user := &store.User{Email: email, DisplayName: email, IsAdmin: isAdmin}
	if err := ts.store.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser(%q) error = %v", email, err)
	}

	// Go through the real login endpoint so the session and CSRF cookies are the
	// same ones a browser would hold.
	const password = "a-long-enough-password"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if err := ts.store.SetPassword(ctx, user.ID, hash); err != nil {
		t.Fatalf("SetPassword() error = %v", err)
	}

	client := ts.newClient()
	token := ts.csrfToken(t, client, "/login")

	resp, err := client.PostForm(ts.http.URL+"/login", url.Values{
		auth.CSRFFormField: {token},
		"email":            {email},
		"password":         {password},
	})
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sign in returned %d, want a successful redirect chain", resp.StatusCode)
	}
	return client
}

// newClient returns a client with its own cookie jar, so each visitor in a test
// carries their own session.
func (ts *testServer) newClient() *http.Client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		panic("web: build a cookie jar: " + err.Error())
	}
	return &http.Client{Jar: jar}
}

func (ts *testServer) csrfToken(t *testing.T, client *http.Client, path string) string {
	t.Helper()

	resp, err := client.Get(ts.http.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	const marker = `name="csrf_token" value="`
	idx := strings.Index(string(body), marker)
	if idx < 0 {
		t.Fatalf("no CSRF token on %s", path)
	}
	rest := string(body)[idx+len(marker):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		t.Fatalf("malformed CSRF token on %s", path)
	}
	return rest[:end]
}

// get performs a GET without following redirects.
func (ts *testServer) get(t *testing.T, client *http.Client, path string) *http.Response {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, ts.http.URL+path, nil)
	if err != nil {
		t.Fatalf("build request for %s: %v", path, err)
	}

	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// The access matrix is the rule most likely to regress: a new admin page wired to
// the wrong guard is a one-line mistake that hands user management to anyone signed
// in, or the whole site to the public.
func TestRouteAccessMatrix(t *testing.T) {
	// want values are the status an anonymous visitor, a signed-in editor and an
	// admin should each see.
	tests := []struct {
		path      string
		anonymous int
		editor    int
		admin     int
	}{
		// Public.
		{path: "/", anonymous: http.StatusOK, editor: http.StatusOK, admin: http.StatusOK},
		{path: "/login", anonymous: http.StatusOK, editor: http.StatusSeeOther, admin: http.StatusSeeOther},
		{path: "/healthz", anonymous: http.StatusOK, editor: http.StatusOK, admin: http.StatusOK},
		{path: "/readyz", anonymous: http.StatusOK, editor: http.StatusOK, admin: http.StatusOK},
		{path: "/static/app.css", anonymous: http.StatusOK, editor: http.StatusOK, admin: http.StatusOK},

		// Editing: any signed-in person, nobody else.
		{path: "/admin", anonymous: http.StatusSeeOther, editor: http.StatusOK, admin: http.StatusOK},
		{path: "/admin/events", anonymous: http.StatusSeeOther, editor: http.StatusOK, admin: http.StatusOK},
		{path: "/admin/quotes", anonymous: http.StatusSeeOther, editor: http.StatusOK, admin: http.StatusOK},

		// Admin only. An editor must be refused, not merely redirected to a login
		// they already completed.
		{path: "/admin/users", anonymous: http.StatusSeeOther, editor: http.StatusForbidden, admin: http.StatusOK},
		{path: "/admin/site", anonymous: http.StatusSeeOther, editor: http.StatusForbidden, admin: http.StatusOK},
		{path: "/admin/export", anonymous: http.StatusSeeOther, editor: http.StatusForbidden, admin: http.StatusOK},

		// Unknown paths.
		{path: "/nope", anonymous: http.StatusNotFound, editor: http.StatusNotFound, admin: http.StatusNotFound},
	}

	ts := newTestServer(t)
	anonymous := ts.newClient()
	editor := ts.signIn(t, "editor@example.com", false)
	admin := ts.signIn(t, "admin@example.com", true)

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			for _, who := range []struct {
				name   string
				client *http.Client
				want   int
			}{
				{"anonymous", anonymous, tt.anonymous},
				{"editor", editor, tt.editor},
				{"admin", admin, tt.admin},
			} {
				resp := ts.get(t, who.client, tt.path)
				if resp.StatusCode != who.want {
					t.Errorf("%s GET %s = %d, want %d", who.name, tt.path, resp.StatusCode, who.want)
				}
			}
		})
	}
}

// Unsafe methods must all reject a missing token, without exception. A handler that
// forgets the check is the classic way a CSRF hole gets in.
func TestUnsafeMethodsRequireCSRF(t *testing.T) {
	paths := []string{
		"/login",
		"/logout",
		"/invite",
		"/auth/oidc/start",
		"/admin",
		"/admin/events/main",
		"/admin/events/milestone",
		"/admin/quotes",
		"/admin/users/invite",
		"/admin/site/visibility",
	}

	ts := newTestServer(t)
	admin := ts.signIn(t, "admin@example.com", true)

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			noRedirect := *admin
			noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			}

			resp, err := noRedirect.PostForm(ts.http.URL+path, url.Values{"title": {"x"}})
			if err != nil {
				t.Fatalf("POST %s: %v", path, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("POST %s without a token = %d, want %d",
					path, resp.StatusCode, http.StatusForbidden)
			}
		})
	}
}

// Section visibility is what keeps the couple's photos off the open internet, so
// each level is checked against each kind of visitor.
func TestSectionVisibility(t *testing.T) {
	tests := []struct {
		level         store.Visibility
		anonymousSees bool
		editorSees    bool
		adminSees     bool
	}{
		{store.VisPublic, true, true, true},
		{store.VisLoggedIn, false, true, true},
		{store.VisAdmin, false, false, true},
	}

	for _, tt := range tests {
		t.Run(string(tt.level), func(t *testing.T) {
			ctx := context.Background()
			ts := newTestServer(t)

			settings, err := ts.store.Settings(ctx)
			if err != nil {
				t.Fatalf("Settings() error = %v", err)
			}
			settings.Visibility.Clocks = tt.level
			settings.PartnerA = store.Partner{Name: "A", City: "Belgrade", Timezone: "Europe/Belgrade"}
			settings.PartnerB = store.Partner{Name: "B", City: "Shanghai", Timezone: "Asia/Shanghai"}
			if err := ts.store.SaveSettings(ctx, settings); err != nil {
				t.Fatalf("SaveSettings() error = %v", err)
			}

			for _, who := range []struct {
				name   string
				client *http.Client
				want   bool
			}{
				{"anonymous", ts.newClient(), tt.anonymousSees},
				{"editor", ts.signIn(t, "editor@example.com", false), tt.editorSees},
				{"admin", ts.signIn(t, "admin@example.com", true), tt.adminSees},
			} {
				resp := ts.get(t, who.client, "/")
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatalf("read the home page: %v", err)
				}
				// The city name only appears when the clocks section renders.
				got := strings.Contains(string(body), "Shanghai")
				if got != who.want {
					t.Errorf("%s sees the clocks = %v, want %v (visibility %q)",
						who.name, got, who.want, tt.level)
				}
			}
		})
	}
}

// An open redirect on the login form is exactly the shape a phishing link wants, so
// every way of expressing "somewhere else" is checked.
func TestSafeReturnTo(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: "/"},
		{name: "plain path", in: "/admin", want: "/admin"},
		{name: "path with a query", in: "/admin/events?saved=1", want: "/admin/events?saved=1"},
		{name: "absolute http", in: "http://evil.example/x", want: "/"},
		{name: "absolute https", in: "https://evil.example/x", want: "/"},
		{name: "scheme relative", in: "//evil.example/x", want: "/"},
		{name: "backslash scheme relative", in: `/\evil.example/x`, want: "/"},
		{name: "javascript", in: "javascript:alert(1)", want: "/"},
		{name: "data url", in: "data:text/html,<script>", want: "/"},
		{name: "not rooted", in: "admin", want: "/"},
		{name: "with credentials", in: "https://user:pass@evil.example/", want: "/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := safeReturnTo(tt.in); got != tt.want {
				t.Errorf("safeReturnTo(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// Every response carries the headers that keep a stray script or an embedding frame
// from working, so a missing one is caught here rather than in a browser months on.
func TestSecurityHeaders(t *testing.T) {
	ts := newTestServer(t)
	resp := ts.get(t, ts.newClient(), "/")

	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "strict-origin-when-cross-origin",
	} {
		if got := resp.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}

	csp := resp.Header.Get("Content-Security-Policy")
	for _, directive := range []string{"default-src 'self'", "frame-ancestors 'none'", "object-src 'none'"} {
		if !strings.Contains(csp, directive) {
			t.Errorf("CSP %q is missing %q", csp, directive)
		}
	}
}

// Pages behind a login must not be indexed even if a crawler somehow reaches them.
func TestPrivatePagesAreNoIndex(t *testing.T) {
	ts := newTestServer(t)
	admin := ts.signIn(t, "admin@example.com", true)

	for _, path := range []string{"/admin", "/admin/users", "/login"} {
		t.Run(path, func(t *testing.T) {
			client := admin
			if path == "/login" {
				client = ts.newClient()
			}
			resp := ts.get(t, client, path)
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			if !strings.Contains(string(body), `name="robots"`) {
				t.Errorf("%s carries no robots meta tag", path)
			}
		})
	}
}
