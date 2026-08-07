package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	// CSRFFormField is the hidden input every unsafe form must carry.
	CSRFFormField = "csrf_token"
	// CSRFHeader lets fetch() calls send the token without a form body.
	CSRFHeader = "X-CSRF-Token"

	csrfSubjectBytes = 32
	csrfCookieMaxAge = 12 * time.Hour
)

// ErrCSRF reports a missing or wrong CSRF token.
var ErrCSRF = errors.New("auth: CSRF token missing or invalid")

// CSRFToken returns the token to embed in forms for this request, setting the
// backing cookie when the visitor does not have one yet.
//
// The token is an HMAC over a per-browser subject rather than the raw subject, so a
// token cannot be forged by anyone who can set cookies on a sibling subdomain but
// does not know the server secret.
//
// Signed-in visitors are bound to their session id, which means the token stops
// working the moment they sign out.
func (m *Manager) CSRFToken(w http.ResponseWriter, r *http.Request) (string, error) {
	subject, err := m.csrfSubject(w, r)
	if err != nil {
		return "", err
	}
	return m.signCSRF(subject), nil
}

// VerifyCSRF checks the token supplied by an unsafe request.
func (m *Manager) VerifyCSRF(r *http.Request) error {
	subject, ok := m.existingCSRFSubject(r)
	if !ok {
		return ErrCSRF
	}

	supplied := r.Header.Get(CSRFHeader)
	if supplied == "" {
		supplied = r.PostFormValue(CSRFFormField)
	}
	if supplied == "" {
		return ErrCSRF
	}

	expected := m.signCSRF(subject)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(supplied)) != 1 {
		return ErrCSRF
	}
	return nil
}

// csrfSubject returns the value the token is bound to, creating a cookie for
// visitors who are not signed in.
func (m *Manager) csrfSubject(w http.ResponseWriter, r *http.Request) (string, error) {
	if subject, ok := m.existingCSRFSubject(r); ok {
		return subject, nil
	}

	buf := make([]byte, csrfSubjectBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("auth: generate CSRF subject: %w", err)
	}
	subject := base64.RawURLEncoding.EncodeToString(buf)

	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookie,
		Value:    subject,
		Path:     m.cfg.BasePath,
		MaxAge:   int(csrfCookieMaxAge.Seconds()),
		HttpOnly: true,
		Secure:   m.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	return "anon:" + subject, nil
}

// existingCSRFSubject reads the subject already available on the request: the
// session token for signed-in visitors, otherwise the anonymous cookie.
func (m *Manager) existingCSRFSubject(r *http.Request) (string, bool) {
	if cookie, err := r.Cookie(SessionCookie); err == nil && cookie.Value != "" {
		return "session:" + hashToken(cookie.Value), true
	}
	if cookie, err := r.Cookie(csrfCookie); err == nil && cookie.Value != "" {
		return "anon:" + cookie.Value, true
	}
	return "", false
}

func (m *Manager) signCSRF(subject string) string {
	mac := hmac.New(sha256.New, m.cfg.Secret)
	mac.Write([]byte("csrf\x00"))
	mac.Write([]byte(subject))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// SafeMethod reports whether a method is defined as read-only and therefore exempt
// from CSRF checks.
func SafeMethod(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	default:
		return false
	}
}
