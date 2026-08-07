package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mrcat71/waitformeet/internal/config"
)

// testOIDC builds a client without provider discovery, which is all the state
// handling and allowlist logic needs.
func testOIDC(cfg config.OIDCConfig) *OIDC {
	return &OIDC{
		cfg:      cfg,
		secret:   []byte("a-test-secret-that-is-long-enough-32"),
		basePath: "/",
	}
}

func TestMayAutoProvision(t *testing.T) {
	tests := []struct {
		name   string
		cfg    config.OIDCConfig
		claims Claims
		want   bool
	}{
		{
			name:   "off by default",
			cfg:    config.OIDCConfig{AllowedGroups: []string{"couple"}},
			claims: Claims{Groups: []string{"couple"}},
			want:   false,
		},
		{
			name:   "allowed group matches",
			cfg:    config.OIDCConfig{AutoProvision: true, AllowedGroups: []string{"couple"}},
			claims: Claims{Groups: []string{"other", "couple"}},
			want:   true,
		},
		{
			name:   "group match ignores case",
			cfg:    config.OIDCConfig{AutoProvision: true, AllowedGroups: []string{"Couple"}},
			claims: Claims{Groups: []string{"couple"}},
			want:   true,
		},
		{
			name:   "group not in the list",
			cfg:    config.OIDCConfig{AutoProvision: true, AllowedGroups: []string{"couple"}},
			claims: Claims{Groups: []string{"everyone"}},
			want:   false,
		},
		{
			name:   "verified address in an allowed domain",
			cfg:    config.OIDCConfig{AutoProvision: true, AllowedDomains: []string{"example.com"}},
			claims: Claims{Email: "her@example.com", EmailVerified: true},
			want:   true,
		},
		{
			// The provider saying somebody typed an address is not evidence they
			// control it, so an unverified address must never open an account.
			name:   "unverified address in an allowed domain",
			cfg:    config.OIDCConfig{AutoProvision: true, AllowedDomains: []string{"example.com"}},
			claims: Claims{Email: "her@example.com", EmailVerified: false},
			want:   false,
		},
		{
			name:   "address in another domain",
			cfg:    config.OIDCConfig{AutoProvision: true, AllowedDomains: []string{"example.com"}},
			claims: Claims{Email: "someone@evil.test", EmailVerified: true},
			want:   false,
		},
		{
			// A domain suffix must not match a lookalike host.
			name:   "lookalike domain",
			cfg:    config.OIDCConfig{AutoProvision: true, AllowedDomains: []string{"example.com"}},
			claims: Claims{Email: "someone@notexample.com", EmailVerified: true},
			want:   false,
		},
		{
			name:   "no rules configured",
			cfg:    config.OIDCConfig{AutoProvision: true},
			claims: Claims{Email: "her@example.com", EmailVerified: true, Groups: []string{"couple"}},
			want:   false,
		},
		{
			name:   "no claims at all",
			cfg:    config.OIDCConfig{AutoProvision: true, AllowedGroups: []string{"couple"}, AllowedDomains: []string{"example.com"}},
			claims: Claims{},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MayAutoProvision(tt.cfg, &tt.claims); got != tt.want {
				t.Errorf("MayAutoProvision() = %v, want %v", got, tt.want)
			}
		})
	}
}

// A nil client stands for "single sign-on is switched off" throughout the codebase,
// so every method must tolerate it.
func TestNilOIDCIsSafe(t *testing.T) {
	var o *OIDC

	if o.DisplayName() != "" {
		t.Error("DisplayName() on a nil client returned something")
	}
	if _, err := o.Start(httptest.NewRecorder(), "/"); !errors.Is(err, ErrOIDCDisabled) {
		t.Errorf("Start() on a nil client = %v, want ErrOIDCDisabled", err)
	}
}

func TestFlowStateRoundTrip(t *testing.T) {
	o := testOIDC(config.OIDCConfig{})

	want := flowState{
		State:    "state-value",
		Nonce:    "nonce-value",
		Verifier: "verifier-value",
		ReturnTo: "/admin",
		Expires:  time.Now().Add(time.Minute).Unix(),
	}
	sealed, err := o.sealFlow(want)
	if err != nil {
		t.Fatalf("sealFlow() error = %v", err)
	}

	got, err := o.openFlow(sealed)
	if err != nil {
		t.Fatalf("openFlow() error = %v", err)
	}
	if got != want {
		t.Errorf("openFlow() = %+v, want %+v", got, want)
	}
}

func TestFlowStateRejections(t *testing.T) {
	o := testOIDC(config.OIDCConfig{})

	valid, err := o.sealFlow(flowState{
		State:   "s",
		Expires: time.Now().Add(time.Minute).Unix(),
	})
	if err != nil {
		t.Fatalf("sealFlow() error = %v", err)
	}
	body, sig, _ := strings.Cut(valid, ".")

	expired, err := o.sealFlow(flowState{
		State:   "s",
		Expires: time.Now().Add(-time.Second).Unix(),
	})
	if err != nil {
		t.Fatalf("sealFlow() error = %v", err)
	}

	other := testOIDC(config.OIDCConfig{})
	other.secret = []byte("a-completely-different-secret-3232")
	foreign, err := other.sealFlow(flowState{
		State:   "s",
		Expires: time.Now().Add(time.Minute).Unix(),
	})
	if err != nil {
		t.Fatalf("sealFlow() error = %v", err)
	}

	tests := []struct {
		name  string
		value string
	}{
		{name: "empty", value: ""},
		{name: "no signature", value: body},
		{name: "tampered payload", value: body + "x." + sig},
		{name: "tampered signature", value: body + "." + sig + "x"},
		{name: "signed with another secret", value: foreign},
		{name: "expired", value: expired},
		{name: "not base64", value: "!!!!.@@@@"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := o.openFlow(tt.value); !errors.Is(err, ErrOIDCState) {
				t.Errorf("openFlow() error = %v, want ErrOIDCState", err)
			}
		})
	}
}

// The state cookie has to survive the provider's redirect back, which is a
// cross-site top-level GET. SameSite=Strict would drop it and break every login.
func TestStartSetsALaxStateCookie(t *testing.T) {
	o := testOIDC(config.OIDCConfig{})
	o.secure = true
	rec := httptest.NewRecorder()

	if _, err := o.Start(rec, "/admin"); err != nil {
		// Start builds the redirect URL from an empty endpoint here, which is fine:
		// the cookie is what this test is about.
		t.Fatalf("Start() error = %v", err)
	}

	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == oidcStateCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("Start() set no state cookie")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax so the provider's redirect keeps it", cookie.SameSite)
	}
	if !cookie.HttpOnly {
		t.Error("HttpOnly = false, want true")
	}
	if !cookie.Secure {
		t.Error("Secure = false, want true when cookies are configured secure")
	}
}

// Two logins started back to back must not share state, or one tab could complete
// the other's flow.
func TestStartIssuesFreshStateEachTime(t *testing.T) {
	o := testOIDC(config.OIDCConfig{})

	seen := make(map[string]bool)
	for range 5 {
		rec := httptest.NewRecorder()
		if _, err := o.Start(rec, "/"); err != nil {
			t.Fatalf("Start() error = %v", err)
		}
		for _, c := range rec.Result().Cookies() {
			if c.Name != oidcStateCookie {
				continue
			}
			flow, err := o.openFlow(c.Value)
			if err != nil {
				t.Fatalf("openFlow() error = %v", err)
			}
			if seen[flow.State] {
				t.Fatalf("state %q was issued twice", flow.State)
			}
			seen[flow.State] = true
		}
	}
}
