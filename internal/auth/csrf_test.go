package auth

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testManager() *Manager {
	return NewManager(nil, Config{
		SessionTTL:   time.Hour,
		CookieSecure: true,
		Secret:       []byte("a-test-secret-that-is-long-enough-32"),
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// issueToken walks through what a page render does: mint a token and hand back the
// cookies the browser would then hold.
func issueToken(t *testing.T, m *Manager) (token string, cookies []*http.Cookie) {
	t.Helper()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/login", nil)

	token, err := m.CSRFToken(rec, req)
	if err != nil {
		t.Fatalf("CSRFToken() error = %v", err)
	}
	return token, rec.Result().Cookies()
}

// postWith builds a form POST carrying the given cookies and token.
func postWith(cookies []*http.Cookie, token string) *http.Request {
	body := strings.NewReader(CSRFFormField + "=" + token)
	req := httptest.NewRequest(http.MethodPost, "/login", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return req
}

func TestCSRFRoundTrip(t *testing.T) {
	m := testManager()

	token, cookies := issueToken(t, m)
	if token == "" {
		t.Fatal("CSRFToken() returned an empty token")
	}
	if err := m.VerifyCSRF(postWith(cookies, token)); err != nil {
		t.Errorf("VerifyCSRF() with the issued token: %v", err)
	}
}

func TestCSRFRejections(t *testing.T) {
	tests := []struct {
		name    string
		request func(t *testing.T, m *Manager) *http.Request
	}{
		{
			name: "no token at all",
			request: func(t *testing.T, m *Manager) *http.Request {
				_, cookies := issueToken(t, m)
				return postWith(cookies, "")
			},
		},
		{
			name: "wrong token",
			request: func(t *testing.T, m *Manager) *http.Request {
				_, cookies := issueToken(t, m)
				return postWith(cookies, "clearly-not-the-right-token")
			},
		},
		{
			name: "right token but no cookie",
			request: func(t *testing.T, m *Manager) *http.Request {
				token, _ := issueToken(t, m)
				return postWith(nil, token)
			},
		},
		{
			name: "token from a different browser",
			request: func(t *testing.T, m *Manager) *http.Request {
				otherToken, _ := issueToken(t, m)
				_, cookies := issueToken(t, m)
				return postWith(cookies, otherToken)
			},
		},
		{
			name: "token signed with a different secret",
			request: func(t *testing.T, m *Manager) *http.Request {
				attacker := NewManager(nil, Config{
					Secret: []byte("a-completely-different-secret-3232"),
				}, slog.New(slog.NewTextHandler(io.Discard, nil)))
				_, cookies := issueToken(t, m)
				// The attacker knows the subject cookie but not the server secret.
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "/login", nil)
				for _, c := range cookies {
					req.AddCookie(c)
				}
				forged, err := attacker.CSRFToken(rec, req)
				if err != nil {
					t.Fatalf("CSRFToken() error = %v", err)
				}
				return postWith(cookies, forged)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testManager()
			if err := m.VerifyCSRF(tt.request(t, m)); !errors.Is(err, ErrCSRF) {
				t.Errorf("VerifyCSRF() error = %v, want ErrCSRF", err)
			}
		})
	}
}

// A token may also travel in a header, for fetch() calls that send no form body.
func TestCSRFAcceptsHeader(t *testing.T) {
	m := testManager()
	token, cookies := issueToken(t, m)

	req := httptest.NewRequest(http.MethodPost, "/api/thing", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	req.Header.Set(CSRFHeader, token)

	if err := m.VerifyCSRF(req); err != nil {
		t.Errorf("VerifyCSRF() with a header token: %v", err)
	}
}

// Signing in must invalidate tokens minted beforehand, otherwise a token captured
// from the login page would keep working against the authenticated session.
func TestCSRFTokenChangesOnSignIn(t *testing.T) {
	m := testManager()

	anonToken, anonCookies := issueToken(t, m)

	// Simulate the post-login state: the browser now carries a session cookie.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	for _, c := range anonCookies {
		req.AddCookie(c)
	}
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: "a-session-token"})

	sessionToken, err := m.CSRFToken(rec, req)
	if err != nil {
		t.Fatalf("CSRFToken() error = %v", err)
	}
	if sessionToken == anonToken {
		t.Error("the CSRF token did not change after signing in")
	}

	// The old token must no longer verify while the session cookie is present.
	stale := postWith(append(anonCookies, &http.Cookie{Name: SessionCookie, Value: "a-session-token"}), anonToken)
	if err := m.VerifyCSRF(stale); !errors.Is(err, ErrCSRF) {
		t.Errorf("VerifyCSRF() with the pre-login token = %v, want ErrCSRF", err)
	}
}

func TestSafeMethod(t *testing.T) {
	tests := map[string]bool{
		http.MethodGet:     true,
		http.MethodHead:    true,
		http.MethodOptions: true,
		http.MethodTrace:   true,
		"get":              true,
		http.MethodPost:    false,
		http.MethodPut:     false,
		http.MethodPatch:   false,
		http.MethodDelete:  false,
	}

	for method, want := range tests {
		t.Run(method, func(t *testing.T) {
			if got := SafeMethod(method); got != want {
				t.Errorf("SafeMethod(%q) = %v, want %v", method, got, want)
			}
		})
	}
}
