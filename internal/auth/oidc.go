package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/mrcat71/waitformeet/internal/config"
)

const (
	// oidcFlowTTL bounds how long a login may sit half-finished.
	oidcFlowTTL = 10 * time.Minute
	// verifierBytes is the PKCE code verifier length before encoding.
	verifierBytes = 32
)

var (
	// ErrOIDCDisabled reports an OIDC endpoint reached while OIDC is switched off.
	ErrOIDCDisabled = errors.New("auth: single sign-on is not enabled")
	// ErrOIDCState reports a callback whose state does not match the one issued,
	// which is either a stale tab or a forged request.
	ErrOIDCState = errors.New("auth: this login attempt has expired, please try again")
	// ErrOIDCNotAllowed reports an identity the provider authenticated but that this
	// site has no account for.
	ErrOIDCNotAllowed = errors.New("auth: this account is not allowed to sign in here")
)

// OIDC wraps the identity provider.
type OIDC struct {
	cfg      config.OIDCConfig
	secret   []byte
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauth    oauth2.Config
	secure   bool
	basePath string
}

// Claims are the fields taken from a verified ID token.
type Claims struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	Groups        []string
}

// NewOIDC performs provider discovery and builds the client.
//
// Discovery is a network call, so it happens once at startup: a provider that is
// unreachable should stop the deployment loudly rather than failing on the first
// person who tries to sign in.
func NewOIDC(ctx context.Context, cfg *config.Config) (*OIDC, error) {
	if !cfg.OIDC.Enabled {
		return nil, nil
	}

	provider, err := oidc.NewProvider(ctx, cfg.OIDC.Issuer)
	if err != nil {
		return nil, fmt.Errorf("auth: discover OIDC provider at %s: %w", cfg.OIDC.Issuer, err)
	}

	return &OIDC{
		cfg:      cfg.OIDC,
		secret:   cfg.SessionSecret,
		provider: provider,
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.OIDC.ClientID}),
		oauth: oauth2.Config{
			ClientID:     cfg.OIDC.ClientID,
			ClientSecret: cfg.OIDC.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  cfg.RedirectURL(),
			Scopes:       cfg.OIDC.Scopes,
		},
		secure:   cfg.CookieSecure,
		basePath: "/",
	}, nil
}

// DisplayName labels the sign-in button.
func (o *OIDC) DisplayName() string {
	if o == nil {
		return ""
	}
	return o.cfg.DisplayName
}

// flowState is what the browser carries between the redirect out and the callback.
// It is signed rather than stored server side, so a restart mid-login is harmless.
type flowState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	ReturnTo string `json:"r"`
	Expires  int64  `json:"e"`
}

// Start begins a login, returning the URL to send the browser to.
func (o *OIDC) Start(w http.ResponseWriter, returnTo string) (string, error) {
	if o == nil {
		return "", ErrOIDCDisabled
	}

	state, err := randomString()
	if err != nil {
		return "", err
	}
	nonce, err := randomString()
	if err != nil {
		return "", err
	}
	verifier, err := randomString()
	if err != nil {
		return "", err
	}

	flow := flowState{
		State:    state,
		Nonce:    nonce,
		Verifier: verifier,
		ReturnTo: returnTo,
		Expires:  time.Now().Add(oidcFlowTTL).Unix(),
	}
	cookie, err := o.sealFlow(flow)
	if err != nil {
		return "", err
	}

	http.SetCookie(w, &http.Cookie{
		Name:     oidcStateCookie,
		Value:    cookie,
		Path:     o.basePath,
		MaxAge:   int(oidcFlowTTL.Seconds()),
		HttpOnly: true,
		Secure:   o.secure,
		// The provider redirects back with a top-level GET, which Strict would
		// strip the cookie from, breaking every sign-in.
		SameSite: http.SameSiteLaxMode,
	})

	// S256 rather than plain: the challenge is a hash, so intercepting the redirect
	// does not reveal the verifier needed to redeem the code.
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	return o.oauth.AuthCodeURL(state,
		oidc.Nonce(nonce),
		oauth2.SetAuthURLParam("code_challenge", challenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	), nil
}

// Complete finishes a login: it validates the state, exchanges the code and
// verifies the ID token. It returns the claims and where to send the browser next.
func (o *OIDC) Complete(ctx context.Context, w http.ResponseWriter, r *http.Request) (*Claims, string, error) {
	if o == nil {
		return nil, "", ErrOIDCDisabled
	}
	defer o.clearFlow(w)

	cookie, err := r.Cookie(oidcStateCookie)
	if err != nil || cookie.Value == "" {
		return nil, "", ErrOIDCState
	}
	flow, err := o.openFlow(cookie.Value)
	if err != nil {
		return nil, "", err
	}

	if subtle.ConstantTimeCompare([]byte(flow.State), []byte(r.URL.Query().Get("state"))) != 1 {
		return nil, "", ErrOIDCState
	}

	// The provider reports its own failures here; surface the reason rather than a
	// generic error, because "access_denied" is genuinely useful to the person.
	if errCode := r.URL.Query().Get("error"); errCode != "" {
		desc := r.URL.Query().Get("error_description")
		if desc == "" {
			desc = errCode
		}
		return nil, "", fmt.Errorf("auth: the identity provider refused the login: %s", desc)
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		return nil, "", ErrOIDCState
	}

	token, err := o.oauth.Exchange(ctx, code, oauth2.SetAuthURLParam("code_verifier", flow.Verifier))
	if err != nil {
		return nil, "", fmt.Errorf("auth: exchange authorization code: %w", err)
	}

	rawID, ok := token.Extra("id_token").(string)
	if !ok || rawID == "" {
		return nil, "", errors.New("auth: the provider returned no ID token")
	}

	idToken, err := o.verifier.Verify(ctx, rawID)
	if err != nil {
		return nil, "", fmt.Errorf("auth: verify ID token: %w", err)
	}
	// The nonce ties this token to the redirect this browser started, which is what
	// stops a token obtained elsewhere from being replayed here.
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(flow.Nonce)) != 1 {
		return nil, "", ErrOIDCState
	}

	claims, err := o.extractClaims(idToken)
	if err != nil {
		return nil, "", err
	}
	return claims, flow.ReturnTo, nil
}

func (o *OIDC) extractClaims(idToken *oidc.IDToken) (*Claims, error) {
	// The groups claim is provider specific, so it is pulled out by configured name
	// from the raw map rather than assumed to be called "groups".
	var raw map[string]any
	if err := idToken.Claims(&raw); err != nil {
		return nil, fmt.Errorf("auth: read ID token claims: %w", err)
	}

	claims := &Claims{Subject: idToken.Subject}
	if v, ok := raw["email"].(string); ok {
		claims.Email = strings.ToLower(strings.TrimSpace(v))
	}
	if v, ok := raw["email_verified"].(bool); ok {
		claims.EmailVerified = v
	}
	if v, ok := raw["name"].(string); ok {
		claims.Name = v
	}
	if v, ok := raw["preferred_username"].(string); ok && claims.Name == "" {
		claims.Name = v
	}

	if o.cfg.GroupsClaim != "" {
		if list, ok := raw[o.cfg.GroupsClaim].([]any); ok {
			for _, item := range list {
				if s, ok := item.(string); ok {
					claims.Groups = append(claims.Groups, s)
				}
			}
		}
	}

	if claims.Subject == "" {
		return nil, errors.New("auth: the provider returned no subject claim")
	}
	return claims, nil
}

// MayAutoProvision reports whether an identity with no existing account is allowed
// to have one created automatically.
//
// This is the gate between "my provider authenticated you" and "you may edit our
// site", so it fails closed: auto-provisioning off, or no rule matching, means no.
//
// It is a plain function over configuration and claims rather than a method on
// OIDC, because the decision needs no network client and callers should not have to
// build one to ask the question.
func MayAutoProvision(cfg config.OIDCConfig, claims *Claims) bool {
	if !cfg.AutoProvision {
		return false
	}

	for _, want := range cfg.AllowedGroups {
		for _, got := range claims.Groups {
			if strings.EqualFold(want, got) {
				return true
			}
		}
	}

	if len(cfg.AllowedDomains) > 0 {
		// An unverified address is not evidence of anything: the provider is saying
		// the person typed it, not that they control it.
		if !claims.EmailVerified {
			return false
		}
		_, domain, found := strings.Cut(claims.Email, "@")
		if found {
			for _, want := range cfg.AllowedDomains {
				if strings.EqualFold(want, domain) {
					return true
				}
			}
		}
	}

	return false
}

func (o *OIDC) sealFlow(flow flowState) (string, error) {
	payload, err := json.Marshal(flow)
	if err != nil {
		return "", fmt.Errorf("auth: encode login state: %w", err)
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + o.sign(body), nil
}

func (o *OIDC) openFlow(value string) (flowState, error) {
	body, sig, found := strings.Cut(value, ".")
	if !found {
		return flowState{}, ErrOIDCState
	}
	if subtle.ConstantTimeCompare([]byte(o.sign(body)), []byte(sig)) != 1 {
		return flowState{}, ErrOIDCState
	}

	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return flowState{}, ErrOIDCState
	}
	var flow flowState
	if err := json.Unmarshal(payload, &flow); err != nil {
		return flowState{}, ErrOIDCState
	}
	if time.Now().Unix() > flow.Expires {
		return flowState{}, ErrOIDCState
	}
	return flow, nil
}

func (o *OIDC) sign(body string) string {
	mac := hmac.New(sha256.New, o.secret)
	mac.Write([]byte("oidc\x00"))
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (o *OIDC) clearFlow(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     oidcStateCookie,
		Value:    "",
		Path:     o.basePath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   o.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func randomString() (string, error) {
	buf := make([]byte, verifierBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("auth: generate random value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
