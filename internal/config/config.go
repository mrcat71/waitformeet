// Package config loads and validates runtime configuration from the environment.
//
// Runtime configuration (listen address, storage, auth, OIDC) is env-only and comes
// from the Helm chart. Content configuration (names, dates, cities) is not here: it is
// seeded into the database and then owned by the admin UI. See internal/store.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// EnvPrefix is prepended to every environment variable this package reads.
const EnvPrefix = "WFM_"

// SeedMode controls how the content seed interacts with the database.
type SeedMode string

const (
	// SeedOnce applies the seed only to an empty database. The admin UI is then
	// the source of truth. This is the default.
	SeedOnce SeedMode = "once"
	// SeedAlways re-applies the seed on every start and makes the seeded fields
	// read-only in the admin UI, for GitOps-style deployments.
	SeedAlways SeedMode = "always"
	// SeedNever skips seeding entirely.
	SeedNever SeedMode = "never"
)

// MinSessionSecretLen is the shortest accepted session secret. It keys the CSRF and
// OIDC state HMACs, so anything shorter is not worth accepting.
const MinSessionSecretLen = 32

// Config is the complete runtime configuration.
type Config struct {
	ListenAddr     string
	BaseURL        *url.URL
	DataDir        string
	TrustedProxies []netip.Prefix
	LogLevel       slog.Level

	CookieSecure  bool
	SessionTTL    time.Duration
	SessionSecret []byte
	// SessionSecretGenerated reports that no secret was configured and a random one
	// was generated for this process. Callers should warn: CSRF tokens and in-flight
	// OIDC logins do not survive a restart.
	SessionSecretGenerated bool

	LocalAuthEnabled       bool
	BootstrapAdminEmail    string
	BootstrapAdminPassword string

	OIDC OIDCConfig

	SeedMode SeedMode
	SeedFile string

	MaxUploadBytes int64
	OGFontPath     string
	MetricsEnabled bool
}

// OIDCConfig holds the settings for login through an external identity provider
// such as Authentik.
type OIDCConfig struct {
	Enabled      bool
	Issuer       string
	ClientID     string
	ClientSecret string
	// DisplayName labels the login button, e.g. "Authentik".
	DisplayName string
	Scopes      []string
	// GroupsClaim names the ID token claim holding group membership. Authentik
	// emits "groups".
	GroupsClaim string
	// AutoProvision creates a user record on first login for anyone satisfying
	// AllowedGroups or AllowedDomains. Off by default: normally an admin
	// pre-authorizes the person in the UI.
	AutoProvision  bool
	AllowedGroups  []string
	AllowedDomains []string
}

// RedirectURL returns the absolute OIDC callback URL derived from the base URL.
func (c *Config) RedirectURL() string {
	return strings.TrimSuffix(c.BaseURL.String(), "/") + "/auth/oidc/callback"
}

// Getenv looks up an environment variable. os.LookupEnv satisfies it.
type Getenv func(string) (string, bool)

// Load reads configuration from getenv, applying defaults and validating the result.
// All validation problems are reported together rather than one per run.
func Load(getenv Getenv) (*Config, error) {
	r := &reader{getenv: getenv}

	cfg := &Config{
		ListenAddr:     r.str("LISTEN_ADDR", ":8080"),
		DataDir:        r.str("DATA_DIR", "/data"),
		TrustedProxies: r.prefixList("TRUSTED_PROXIES"),
		LogLevel:       r.logLevel("LOG_LEVEL", slog.LevelInfo),

		CookieSecure: r.boolean("COOKIE_SECURE", true),
		SessionTTL:   r.duration("SESSION_TTL", 30*24*time.Hour),

		LocalAuthEnabled:       r.boolean("LOCAL_AUTH_ENABLED", true),
		BootstrapAdminEmail:    strings.TrimSpace(r.str("BOOTSTRAP_ADMIN_EMAIL", "")),
		BootstrapAdminPassword: r.str("BOOTSTRAP_ADMIN_PASSWORD", ""),

		SeedFile: r.str("SEED_FILE", ""),

		MaxUploadBytes: r.bytes("MAX_UPLOAD_BYTES", 16<<20),
		OGFontPath:     r.str("OG_FONT_PATH", ""),
		MetricsEnabled: r.boolean("METRICS_ENABLED", true),
	}

	cfg.BaseURL = r.baseURL("BASE_URL", "http://localhost:8080")
	cfg.SeedMode = r.seedMode("SEED_MODE", SeedOnce)
	cfg.SessionSecret, cfg.SessionSecretGenerated = r.sessionSecret("SESSION_SECRET")

	cfg.OIDC = OIDCConfig{
		Enabled:        r.boolean("OIDC_ENABLED", false),
		Issuer:         strings.TrimSuffix(strings.TrimSpace(r.str("OIDC_ISSUER", "")), "/"),
		ClientID:       strings.TrimSpace(r.str("OIDC_CLIENT_ID", "")),
		ClientSecret:   r.str("OIDC_CLIENT_SECRET", ""),
		DisplayName:    r.str("OIDC_DISPLAY_NAME", "SSO"),
		Scopes:         r.list("OIDC_SCOPES", []string{"openid", "profile", "email"}),
		GroupsClaim:    r.str("OIDC_GROUPS_CLAIM", "groups"),
		AutoProvision:  r.boolean("OIDC_AUTO_PROVISION", false),
		AllowedGroups:  r.list("OIDC_ALLOWED_GROUPS", nil),
		AllowedDomains: lowerAll(r.list("OIDC_ALLOWED_DOMAINS", nil)),
	}

	r.errs = append(r.errs, cfg.validate()...)
	if len(r.errs) > 0 {
		return nil, fmt.Errorf("invalid configuration: %w", errors.Join(r.errs...))
	}
	return cfg, nil
}

func (c *Config) validate() []error {
	var errs []error

	if c.ListenAddr == "" {
		errs = append(errs, errors.New(EnvPrefix+"LISTEN_ADDR must not be empty"))
	}
	if c.DataDir == "" {
		errs = append(errs, errors.New(EnvPrefix+"DATA_DIR must not be empty"))
	}
	if c.SessionTTL <= 0 {
		errs = append(errs, errors.New(EnvPrefix+"SESSION_TTL must be positive"))
	}
	if c.MaxUploadBytes <= 0 {
		errs = append(errs, errors.New(EnvPrefix+"MAX_UPLOAD_BYTES must be positive"))
	}

	if !c.LocalAuthEnabled && !c.OIDC.Enabled {
		errs = append(errs, errors.New("no login method enabled: set "+EnvPrefix+
			"LOCAL_AUTH_ENABLED=true or "+EnvPrefix+"OIDC_ENABLED=true, otherwise nobody can edit the site"))
	}

	if c.OIDC.Enabled {
		if c.OIDC.Issuer == "" {
			errs = append(errs, errors.New(EnvPrefix+"OIDC_ISSUER is required when OIDC is enabled"))
		} else if u, err := url.Parse(c.OIDC.Issuer); err != nil || u.Scheme != "https" {
			// Authentik and every other conformant provider serves discovery over https.
			// Allowing http here would silently downgrade token verification.
			errs = append(errs, fmt.Errorf("%sOIDC_ISSUER must be an https URL, got %q", EnvPrefix, c.OIDC.Issuer))
		}
		if c.OIDC.ClientID == "" {
			errs = append(errs, errors.New(EnvPrefix+"OIDC_CLIENT_ID is required when OIDC is enabled"))
		}
		if c.OIDC.ClientSecret == "" {
			errs = append(errs, errors.New(EnvPrefix+"OIDC_CLIENT_SECRET is required when OIDC is enabled"))
		}
		if !slices.Contains(c.OIDC.Scopes, "openid") {
			errs = append(errs, errors.New(EnvPrefix+"OIDC_SCOPES must include \"openid\""))
		}
		if c.OIDC.AutoProvision && len(c.OIDC.AllowedGroups) == 0 && len(c.OIDC.AllowedDomains) == 0 {
			errs = append(errs, errors.New(EnvPrefix+"OIDC_AUTO_PROVISION requires "+
				EnvPrefix+"OIDC_ALLOWED_GROUPS or "+EnvPrefix+"OIDC_ALLOWED_DOMAINS, "+
				"otherwise anyone your provider authenticates becomes an editor"))
		}
	}

	if c.BootstrapAdminPassword != "" && c.BootstrapAdminEmail == "" {
		errs = append(errs, errors.New(EnvPrefix+"BOOTSTRAP_ADMIN_PASSWORD is set but "+
			EnvPrefix+"BOOTSTRAP_ADMIN_EMAIL is empty"))
	}

	return errs
}

// reader pulls prefixed environment variables and accumulates parse failures so that
// a misconfigured deployment reports every problem at once.
type reader struct {
	getenv Getenv
	errs   []error
}

func (r *reader) lookup(key string) (string, bool) {
	v, ok := r.getenv(EnvPrefix + key)
	if !ok {
		return "", false
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return "", false
	}
	return v, true
}

func (r *reader) fail(key string, value string, err error) {
	r.errs = append(r.errs, fmt.Errorf("%s%s: invalid value %q: %w", EnvPrefix, key, value, err))
}

func (r *reader) str(key, def string) string {
	if v, ok := r.lookup(key); ok {
		return v
	}
	return def
}

func (r *reader) boolean(key string, def bool) bool {
	v, ok := r.lookup(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		r.fail(key, v, errors.New("expected a boolean such as true or false"))
		return def
	}
	return b
}

func (r *reader) duration(key string, def time.Duration) time.Duration {
	v, ok := r.lookup(key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		r.fail(key, v, errors.New("expected a Go duration such as 720h or 30m"))
		return def
	}
	return d
}

// bytes accepts a plain byte count or a size suffix (kb, mb, gb, and the binary
// spellings), because "16mb" is far easier to get right in values.yaml than 16777216.
func (r *reader) bytes(key string, def int64) int64 {
	v, ok := r.lookup(key)
	if !ok {
		return def
	}
	norm := strings.ToLower(v)
	mult := int64(1)
	for _, s := range []struct {
		suffix string
		mult   int64
	}{
		{"kib", 1 << 10}, {"mib", 1 << 20}, {"gib", 1 << 30},
		{"kb", 1 << 10}, {"mb", 1 << 20}, {"gb", 1 << 30},
		{"k", 1 << 10}, {"m", 1 << 20}, {"g", 1 << 30},
	} {
		if rest, found := strings.CutSuffix(norm, s.suffix); found {
			norm, mult = strings.TrimSpace(rest), s.mult
			break
		}
	}
	n, err := strconv.ParseInt(norm, 10, 64)
	if err != nil {
		r.fail(key, v, errors.New("expected a byte count such as 16mb or 16777216"))
		return def
	}
	return n * mult
}

func (r *reader) list(key string, def []string) []string {
	v, ok := r.lookup(key)
	if !ok {
		return def
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return def
	}
	return out
}

func (r *reader) prefixList(key string) []netip.Prefix {
	raw := r.list(key, nil)
	out := make([]netip.Prefix, 0, len(raw))
	for _, item := range raw {
		// Accept both a CIDR and a bare address, so a single proxy IP just works.
		if p, err := netip.ParsePrefix(item); err == nil {
			out = append(out, p)
			continue
		}
		addr, err := netip.ParseAddr(item)
		if err != nil {
			r.fail(key, item, errors.New("expected a CIDR such as 10.0.0.0/8 or a bare IP address"))
			continue
		}
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return out
}

func (r *reader) logLevel(key string, def slog.Level) slog.Level {
	v, ok := r.lookup(key)
	if !ok {
		return def
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(v)); err != nil {
		r.fail(key, v, errors.New("expected one of debug, info, warn, error"))
		return def
	}
	return lvl
}

func (r *reader) baseURL(key, def string) *url.URL {
	v := r.str(key, def)
	u, err := url.Parse(v)
	if err != nil {
		r.fail(key, v, err)
		return &url.URL{Scheme: "http", Host: "localhost:8080"}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		r.fail(key, v, errors.New("expected an absolute http or https URL"))
	} else if u.Host == "" {
		r.fail(key, v, errors.New("missing host"))
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	return u
}

func (r *reader) seedMode(key string, def SeedMode) SeedMode {
	v, ok := r.lookup(key)
	if !ok {
		return def
	}
	switch m := SeedMode(strings.ToLower(v)); m {
	case SeedOnce, SeedAlways, SeedNever:
		return m
	default:
		r.fail(key, v, errors.New("expected one of once, always, never"))
		return def
	}
}

// sessionSecret returns the configured secret, or a freshly generated one when unset.
// A short secret is a configuration error rather than something to silently stretch.
func (r *reader) sessionSecret(key string) (secret []byte, generated bool) {
	v, ok := r.lookup(key)
	if !ok {
		return randomSecret(), true
	}
	if len(v) < MinSessionSecretLen {
		r.errs = append(r.errs, fmt.Errorf("%s%s must be at least %d characters, got %d",
			EnvPrefix, key, MinSessionSecretLen, len(v)))
		return randomSecret(), true
	}
	return []byte(v), false
}

func lowerAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToLower(s)
	}
	return out
}

// randomSecret produces a session secret for deployments that did not configure one.
// A failing CSPRNG means every token this process issues would be forgeable, so there
// is nothing sensible to degrade to.
func randomSecret() []byte {
	buf := make([]byte, MinSessionSecretLen)
	if _, err := rand.Read(buf); err != nil {
		panic("config: no usable source of randomness: " + err.Error())
	}
	return []byte(base64.RawURLEncoding.EncodeToString(buf))
}
