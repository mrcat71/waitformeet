package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

// env builds a Getenv over a map, so tests never touch the real process environment.
func env(pairs map[string]string) Getenv {
	return func(key string) (string, bool) {
		v, ok := pairs[key]
		return v, ok
	}
}

// minimal is the smallest environment that produces a valid configuration.
func minimal(extra map[string]string) map[string]string {
	m := map[string]string{
		"WFM_SESSION_SECRET": strings.Repeat("s", MinSessionSecretLen),
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(minimal(nil)))
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	if got, want := cfg.ListenAddr, ":8080"; got != want {
		t.Errorf("ListenAddr = %q, want %q", got, want)
	}
	if got, want := cfg.DataDir, "/data"; got != want {
		t.Errorf("DataDir = %q, want %q", got, want)
	}
	if got, want := cfg.SeedMode, SeedOnce; got != want {
		t.Errorf("SeedMode = %q, want %q", got, want)
	}
	if got, want := cfg.MaxUploadBytes, int64(16<<20); got != want {
		t.Errorf("MaxUploadBytes = %d, want %d", got, want)
	}
	if !cfg.CookieSecure {
		t.Error("CookieSecure = false, want true by default")
	}
	if !cfg.LocalAuthEnabled {
		t.Error("LocalAuthEnabled = false, want true by default")
	}
	if cfg.OIDC.Enabled {
		t.Error("OIDC.Enabled = true, want false by default")
	}
	if cfg.SessionSecretGenerated {
		t.Error("SessionSecretGenerated = true, want false when a secret is configured")
	}
}

func TestLoadScalars(t *testing.T) {
	tests := []struct {
		name  string
		envs  map[string]string
		check func(*testing.T, *Config)
	}{
		{
			name: "listen address and data dir",
			envs: map[string]string{"WFM_LISTEN_ADDR": "127.0.0.1:9000", "WFM_DATA_DIR": "/var/lib/wfm"},
			check: func(t *testing.T, c *Config) {
				if c.ListenAddr != "127.0.0.1:9000" {
					t.Errorf("ListenAddr = %q", c.ListenAddr)
				}
				if c.DataDir != "/var/lib/wfm" {
					t.Errorf("DataDir = %q", c.DataDir)
				}
			},
		},
		{
			name: "blank value falls back to the default",
			envs: map[string]string{"WFM_LISTEN_ADDR": "   "},
			check: func(t *testing.T, c *Config) {
				if c.ListenAddr != ":8080" {
					t.Errorf("ListenAddr = %q, want the default", c.ListenAddr)
				}
			},
		},
		{
			name: "boolean off",
			envs: map[string]string{"WFM_COOKIE_SECURE": "false"},
			check: func(t *testing.T, c *Config) {
				if c.CookieSecure {
					t.Error("CookieSecure = true, want false")
				}
			},
		},
		{
			name: "duration",
			envs: map[string]string{"WFM_SESSION_TTL": "12h"},
			check: func(t *testing.T, c *Config) {
				if c.SessionTTL != 12*time.Hour {
					t.Errorf("SessionTTL = %v", c.SessionTTL)
				}
			},
		},
		{
			name: "log level",
			envs: map[string]string{"WFM_LOG_LEVEL": "debug"},
			check: func(t *testing.T, c *Config) {
				if c.LogLevel != slog.LevelDebug {
					t.Errorf("LogLevel = %v", c.LogLevel)
				}
			},
		},
		{
			name: "seed mode always",
			envs: map[string]string{"WFM_SEED_MODE": "ALWAYS"},
			check: func(t *testing.T, c *Config) {
				if c.SeedMode != SeedAlways {
					t.Errorf("SeedMode = %q", c.SeedMode)
				}
			},
		},
		{
			name: "base url loses its trailing slash",
			envs: map[string]string{"WFM_BASE_URL": "https://wait.example.com/"},
			check: func(t *testing.T, c *Config) {
				if got, want := c.BaseURL.String(), "https://wait.example.com"; got != want {
					t.Errorf("BaseURL = %q, want %q", got, want)
				}
			},
		},
		{
			name: "session secret is generated when unset",
			envs: map[string]string{"WFM_SESSION_SECRET": ""},
			check: func(t *testing.T, c *Config) {
				if !c.SessionSecretGenerated {
					t.Error("SessionSecretGenerated = false, want true")
				}
				if len(c.SessionSecret) < MinSessionSecretLen {
					t.Errorf("generated secret is %d bytes, want at least %d", len(c.SessionSecret), MinSessionSecretLen)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(env(minimal(tt.envs)))
			if err != nil {
				t.Fatalf("Load() error = %v, want nil", err)
			}
			tt.check(t, cfg)
		})
	}
}

func TestLoadByteSizes(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"1024", 1024},
		{"16mb", 16 << 20},
		{"16MB", 16 << 20},
		{"16 mb", 16 << 20},
		{"8mib", 8 << 20},
		{"2gb", 2 << 30},
		{"512k", 512 << 10},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			cfg, err := Load(env(minimal(map[string]string{"WFM_MAX_UPLOAD_BYTES": tt.in})))
			if err != nil {
				t.Fatalf("Load() error = %v, want nil", err)
			}
			if cfg.MaxUploadBytes != tt.want {
				t.Errorf("MaxUploadBytes = %d, want %d", cfg.MaxUploadBytes, tt.want)
			}
		})
	}
}

func TestLoadTrustedProxies(t *testing.T) {
	cfg, err := Load(env(minimal(map[string]string{
		"WFM_TRUSTED_PROXIES": "10.0.0.0/8, 192.168.1.5 ,fd00::/8",
	})))
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	want := []string{"10.0.0.0/8", "192.168.1.5/32", "fd00::/8"}
	if len(cfg.TrustedProxies) != len(want) {
		t.Fatalf("got %d prefixes, want %d: %v", len(cfg.TrustedProxies), len(want), cfg.TrustedProxies)
	}
	for i, w := range want {
		if got := cfg.TrustedProxies[i].String(); got != w {
			t.Errorf("TrustedProxies[%d] = %q, want %q", i, got, w)
		}
	}
}

func TestLoadOIDC(t *testing.T) {
	cfg, err := Load(env(minimal(map[string]string{
		"WFM_OIDC_ENABLED":         "true",
		"WFM_OIDC_ISSUER":          "https://auth.example.com/application/o/waitformeet/",
		"WFM_OIDC_CLIENT_ID":       "wfm",
		"WFM_OIDC_CLIENT_SECRET":   "shh",
		"WFM_OIDC_ALLOWED_DOMAINS": "Example.COM",
		"WFM_BASE_URL":             "https://wait.example.com",
	})))
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	if got, want := cfg.OIDC.Issuer, "https://auth.example.com/application/o/waitformeet/"; got != want {
		t.Errorf("Issuer = %q, want %q (preserved exactly for OIDC discovery)", got, want)
	}
	if got, want := cfg.OIDC.AllowedDomains[0], "example.com"; got != want {
		t.Errorf("AllowedDomains[0] = %q, want %q (lowercased)", got, want)
	}
	if got, want := cfg.OIDC.GroupsClaim, "groups"; got != want {
		t.Errorf("GroupsClaim = %q, want %q", got, want)
	}
	if got, want := cfg.RedirectURL(), "https://wait.example.com/auth/oidc/callback"; got != want {
		t.Errorf("RedirectURL() = %q, want %q", got, want)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name     string
		envs     map[string]string
		wantErrs []string
	}{
		{
			name:     "unparseable boolean",
			envs:     map[string]string{"WFM_COOKIE_SECURE": "yes-please"},
			wantErrs: []string{"WFM_COOKIE_SECURE"},
		},
		{
			name:     "unparseable duration",
			envs:     map[string]string{"WFM_SESSION_TTL": "forever"},
			wantErrs: []string{"WFM_SESSION_TTL"},
		},
		{
			name:     "unknown log level",
			envs:     map[string]string{"WFM_LOG_LEVEL": "chatty"},
			wantErrs: []string{"WFM_LOG_LEVEL"},
		},
		{
			name:     "unknown seed mode",
			envs:     map[string]string{"WFM_SEED_MODE": "sometimes"},
			wantErrs: []string{"WFM_SEED_MODE"},
		},
		{
			name:     "base url without a scheme",
			envs:     map[string]string{"WFM_BASE_URL": "wait.example.com"},
			wantErrs: []string{"WFM_BASE_URL"},
		},
		{
			name:     "malformed trusted proxy",
			envs:     map[string]string{"WFM_TRUSTED_PROXIES": "not-an-ip"},
			wantErrs: []string{"WFM_TRUSTED_PROXIES"},
		},
		{
			name:     "short session secret",
			envs:     map[string]string{"WFM_SESSION_SECRET": "tooshort"},
			wantErrs: []string{"WFM_SESSION_SECRET", "at least 32"},
		},
		{
			name: "every login method disabled locks everyone out",
			envs: map[string]string{"WFM_LOCAL_AUTH_ENABLED": "false"},
			wantErrs: []string{
				"no login method enabled",
			},
		},
		{
			name: "oidc enabled without issuer, client id or secret",
			envs: map[string]string{"WFM_OIDC_ENABLED": "true"},
			wantErrs: []string{
				"WFM_OIDC_ISSUER is required",
				"WFM_OIDC_CLIENT_ID is required",
				"WFM_OIDC_CLIENT_SECRET is required",
			},
		},
		{
			name: "oidc issuer must be https",
			envs: map[string]string{
				"WFM_OIDC_ENABLED":       "true",
				"WFM_OIDC_ISSUER":        "http://auth.example.com",
				"WFM_OIDC_CLIENT_ID":     "wfm",
				"WFM_OIDC_CLIENT_SECRET": "shh",
			},
			wantErrs: []string{"must be an https URL"},
		},
		{
			name: "oidc scopes must include openid",
			envs: map[string]string{
				"WFM_OIDC_ENABLED":       "true",
				"WFM_OIDC_ISSUER":        "https://auth.example.com",
				"WFM_OIDC_CLIENT_ID":     "wfm",
				"WFM_OIDC_CLIENT_SECRET": "shh",
				"WFM_OIDC_SCOPES":        "profile,email",
			},
			wantErrs: []string{"must include"},
		},
		{
			name: "auto provisioning without any restriction",
			envs: map[string]string{
				"WFM_OIDC_ENABLED":        "true",
				"WFM_OIDC_ISSUER":         "https://auth.example.com",
				"WFM_OIDC_CLIENT_ID":      "wfm",
				"WFM_OIDC_CLIENT_SECRET":  "shh",
				"WFM_OIDC_AUTO_PROVISION": "true",
			},
			wantErrs: []string{"OIDC_AUTO_PROVISION requires"},
		},
		{
			name:     "bootstrap password without an email",
			envs:     map[string]string{"WFM_BOOTSTRAP_ADMIN_PASSWORD": "hunter2hunter2"},
			wantErrs: []string{"BOOTSTRAP_ADMIN_EMAIL is empty"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(env(minimal(tt.envs)))
			if err == nil {
				t.Fatalf("Load() error = nil, want an error mentioning %v", tt.wantErrs)
			}
			if cfg != nil {
				t.Error("Load() returned a config alongside an error, want nil")
			}
			for _, want := range tt.wantErrs {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

// A misconfigured deployment should learn about every problem in one restart rather
// than discovering them one at a time.
func TestLoadReportsAllErrorsAtOnce(t *testing.T) {
	_, err := Load(env(minimal(map[string]string{
		"WFM_COOKIE_SECURE": "maybe",
		"WFM_SESSION_TTL":   "soon",
		"WFM_LOG_LEVEL":     "loud",
	})))
	if err == nil {
		t.Fatal("Load() error = nil, want an error")
	}
	for _, want := range []string{"WFM_COOKIE_SECURE", "WFM_SESSION_TTL", "WFM_LOG_LEVEL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}
