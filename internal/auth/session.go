package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/mrcat71/waitformeet/internal/store"
)

const (
	// SessionCookie holds the session token.
	SessionCookie = "wfm_session"
	// csrfCookie carries a random value for visitors who are not signed in yet, so
	// that the login form itself can be CSRF protected.
	csrfCookie = "wfm_csrf"
	// oidcStateCookie holds the in-flight OIDC login state.
	oidcStateCookie = "wfm_oidc"

	// tokenBytes is the entropy in a session token. 32 bytes is far beyond guessing.
	tokenBytes = 32
	// renewThreshold is how much of a session's life must be gone before touching
	// it extends the expiry. Without it every request would write to the database.
	renewThreshold = 0.5
)

// ErrNoSession reports that the request carries no usable session.
var ErrNoSession = errors.New("auth: no session")

// Config is the subset of the runtime configuration that auth needs.
type Config struct {
	SessionTTL   time.Duration
	CookieSecure bool
	Secret       []byte
	// BasePath is the cookie path, "/" for a normal deployment.
	BasePath string
}

// Manager issues and validates sessions and CSRF tokens.
type Manager struct {
	store *store.Store
	cfg   Config
	log   *slog.Logger
}

// NewManager builds a session manager.
func NewManager(st *store.Store, cfg Config, log *slog.Logger) *Manager {
	if cfg.BasePath == "" {
		cfg.BasePath = "/"
	}
	return &Manager{store: st, cfg: cfg, log: log}
}

// newToken returns a fresh random token and the hash stored against it.
func newToken() (token, hash string, err error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("auth: generate token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(buf)
	return token, hashToken(token), nil
}

// hashToken maps a token to the value stored in the database. Only the hash is
// persisted, so a copy of the database does not hand over live sessions.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// StartSession signs a user in and sets the session cookie.
func (m *Manager) StartSession(ctx context.Context, w http.ResponseWriter, r *http.Request, userID int64) error {
	token, hash, err := newToken()
	if err != nil {
		return err
	}

	expires := time.Now().UTC().Add(m.cfg.SessionTTL)
	sess := &store.Session{
		ID:        hash,
		UserID:    userID,
		ExpiresAt: expires,
		UserAgent: truncate(r.UserAgent(), 200),
	}
	if err := m.store.CreateSession(ctx, sess); err != nil {
		return err
	}
	if err := m.store.TouchLogin(ctx, userID); err != nil {
		// Recording the login is bookkeeping; failing it must not deny the sign-in.
		m.log.WarnContext(ctx, "could not record login time", "error", err, "user_id", userID)
	}

	http.SetCookie(w, m.cookie(SessionCookie, token, expires))

	// A fresh CSRF secret per session, so a token from before sign-in cannot be
	// replayed afterwards (session fixation of the CSRF token).
	m.clearCookie(w, csrfCookie)
	return nil
}

// EndSession signs the current browser out.
func (m *Manager) EndSession(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	if cookie, err := r.Cookie(SessionCookie); err == nil && cookie.Value != "" {
		if err := m.store.DeleteSession(ctx, hashToken(cookie.Value)); err != nil {
			return err
		}
	}
	m.clearCookie(w, SessionCookie)
	m.clearCookie(w, csrfCookie)
	return nil
}

// Current resolves the signed-in user for a request.
//
// It returns ErrNoSession when there is no valid session, which includes an expired
// one and one whose user has since been disabled.
func (m *Manager) Current(ctx context.Context, r *http.Request) (*store.User, *store.Session, error) {
	cookie, err := r.Cookie(SessionCookie)
	if err != nil || cookie.Value == "" {
		return nil, nil, ErrNoSession
	}

	sess, user, err := m.store.SessionUser(ctx, hashToken(cookie.Value))
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, ErrNoSession
	}
	if err != nil {
		return nil, nil, err
	}
	return user, sess, nil
}

// Touch extends a session that is more than half used up, giving sliding expiry
// without a database write on every single request.
//
// The cookie is reissued alongside the database row: extending only one of the two
// would leave the browser dropping a session the server still considers live, or
// the reverse.
func (m *Manager) Touch(ctx context.Context, w http.ResponseWriter, r *http.Request, sess *store.Session) {
	now := time.Now().UTC()
	life := sess.ExpiresAt.Sub(sess.CreatedAt)
	if life <= 0 {
		return
	}
	if float64(sess.ExpiresAt.Sub(now)) > float64(life)*renewThreshold {
		return
	}

	cookie, err := r.Cookie(SessionCookie)
	if err != nil || cookie.Value == "" {
		return
	}

	expires := now.Add(m.cfg.SessionTTL)
	if err := m.store.ExtendSession(ctx, sess.ID, expires); err != nil {
		m.log.WarnContext(ctx, "could not extend session", "error", err)
		return
	}
	http.SetCookie(w, m.cookie(SessionCookie, cookie.Value, expires))
}

func (m *Manager) cookie(name, value string, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     m.cfg.BasePath,
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		Secure:   m.cfg.CookieSecure,
		// Lax rather than Strict: the OIDC provider redirects back with a GET, and
		// Strict would drop the cookie on that navigation and break every SSO login.
		SameSite: http.SameSiteLaxMode,
	}
}

func (m *Manager) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     m.cfg.BasePath,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   m.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
