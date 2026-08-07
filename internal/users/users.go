// Package users manages accounts: invitations, bootstrapping the first admin, and
// the rules about who may change whom.
package users

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/mrcat71/waitformeet/internal/auth"
	"github.com/mrcat71/waitformeet/internal/config"
	"github.com/mrcat71/waitformeet/internal/store"
)

// InviteTTL is how long an invitation link stays usable.
const InviteTTL = 7 * 24 * time.Hour

const inviteTokenBytes = 32

var (
	// ErrSelfDemotion reports an admin trying to remove their own admin rights.
	// The last-admin guard in the store would catch the dangerous case anyway, but
	// refusing here gives a message that explains what to do instead.
	ErrSelfDemotion = errors.New("users: ask another admin to change your own admin rights")
	// ErrSelfDeletion reports an admin trying to delete their own account.
	ErrSelfDeletion = errors.New("users: you cannot delete your own account")
)

// Service holds the account operations that span more than one store call.
type Service struct {
	store *store.Store
	log   *slog.Logger
}

// NewService builds the account service.
func NewService(st *store.Store, log *slog.Logger) *Service {
	return &Service{store: st, log: log}
}

// Invite creates an invitation and returns the single-use token to put in the link.
// Only the token's hash is stored, so a copy of the database cannot be used to
// redeem outstanding invitations.
func (s *Service) Invite(ctx context.Context, actor *store.User, email, displayName string, isAdmin bool) (token string, inv *store.Invite, err error) {
	email = store.NormalizeEmail(email)
	if email == "" || !strings.Contains(email, "@") {
		return "", nil, fmt.Errorf("users: %q is not an email address", email)
	}

	// Catch the duplicate here rather than at redemption time, where the person
	// following the link would be the one to see the error.
	if _, err := s.store.UserByEmail(ctx, email); err == nil {
		return "", nil, store.ErrEmailTaken
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", nil, err
	}

	buf := make([]byte, inviteTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, fmt.Errorf("users: generate invite token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(buf)

	inv = &store.Invite{
		TokenHash:   HashInviteToken(token),
		Email:       email,
		DisplayName: displayName,
		IsAdmin:     isAdmin,
		ExpiresAt:   time.Now().UTC().Add(InviteTTL),
	}
	if actor != nil {
		inv.CreatedBy = &actor.ID
	}
	if err := s.store.CreateInvite(ctx, inv); err != nil {
		return "", nil, err
	}
	return token, inv, nil
}

// Redeem turns an invitation token plus a chosen password into an account.
func (s *Service) Redeem(ctx context.Context, token, password string) (*store.User, error) {
	hash, err := auth.HashPassword(password)
	if err != nil {
		return nil, err
	}
	return s.store.RedeemInvite(ctx, HashInviteToken(token), hash)
}

// HashInviteToken maps an invitation token to its stored form.
func HashInviteToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// InviteURL builds the link to send to the invitee.
func InviteURL(base *url.URL, token string) string {
	u := *base
	u.Path = strings.TrimSuffix(u.Path, "/") + "/invite"
	u.RawQuery = url.Values{"token": {token}}.Encode()
	return u.String()
}

// Update applies an admin's changes to another account.
//
// The store enforces the invariant that an enabled admin must always remain; this
// adds the softer rule that admins do not change their own rights, so the common
// mistake produces a helpful message instead of a constraint error.
func (s *Service) Update(ctx context.Context, actor, target *store.User, displayName string, isAdmin, disabled bool) error {
	if actor != nil && actor.ID == target.ID {
		if isAdmin != target.IsAdmin || disabled {
			return ErrSelfDemotion
		}
	}

	target.DisplayName = displayName
	target.IsAdmin = isAdmin
	target.Disabled = disabled
	return s.store.UpdateUser(ctx, target)
}

// Delete removes another person's account.
func (s *Service) Delete(ctx context.Context, actor *store.User, targetID int64) error {
	if actor != nil && actor.ID == targetID {
		return ErrSelfDeletion
	}
	return s.store.DeleteUser(ctx, targetID)
}

// SetPassword changes an account's password and signs it out everywhere, so that a
// password reset actually ends any session an attacker might be holding.
func (s *Service) SetPassword(ctx context.Context, userID int64, password string) error {
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	if err := s.store.SetPassword(ctx, userID, hash); err != nil {
		return err
	}
	return s.store.DeleteUserSessions(ctx, userID)
}

// Bootstrap creates the first admin on an empty deployment.
//
// With a password configured it creates the account directly. Without one it issues
// an invitation and logs the link, which is the better default: the password is then
// chosen by the person rather than living in a Kubernetes Secret forever.
func (s *Service) Bootstrap(ctx context.Context, cfg *config.Config) error {
	count, err := s.store.UserCount(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	if cfg.BootstrapAdminEmail == "" {
		s.log.Warn("no accounts exist and no bootstrap admin is configured; " +
			"nobody can sign in or edit the site. Set WFM_BOOTSTRAP_ADMIN_EMAIL " +
			"(optionally with WFM_BOOTSTRAP_ADMIN_PASSWORD) and restart")
		return nil
	}

	if cfg.BootstrapAdminPassword != "" {
		hash, err := auth.HashPassword(cfg.BootstrapAdminPassword)
		if err != nil {
			return fmt.Errorf("users: bootstrap admin password rejected: %w", err)
		}
		user := &store.User{
			Email:        cfg.BootstrapAdminEmail,
			DisplayName:  "Admin",
			IsAdmin:      true,
			PasswordHash: hash,
		}
		if err := s.store.CreateUser(ctx, user); err != nil {
			return fmt.Errorf("users: create bootstrap admin: %w", err)
		}
		s.log.Info("created the first admin account from configuration", "email", user.Email)
		return nil
	}

	token, _, err := s.Invite(ctx, nil, cfg.BootstrapAdminEmail, "Admin", true)
	if err != nil {
		return fmt.Errorf("users: create bootstrap invitation: %w", err)
	}
	// Logged once, on an otherwise empty deployment. Anyone who can read the pod
	// log can already read the Secret, so this reveals nothing new.
	s.log.Info("no accounts exist yet: open this one-time link to set your admin password",
		"url", InviteURL(cfg.BaseURL, token),
		"email", store.NormalizeEmail(cfg.BootstrapAdminEmail),
		"valid_for", InviteTTL.String())
	return nil
}

// ResolveOIDC finds or creates the account behind a verified set of OIDC claims.
//
// The order matters. A subject already bound to an account wins outright. Otherwise
// a verified email address may adopt an existing account, which is how an admin
// pre-authorises somebody before their first single sign-on. Only if neither matches
// does auto-provisioning get a say, and it is off by default.
func (s *Service) ResolveOIDC(ctx context.Context, oidcCfg config.OIDCConfig, claims *auth.Claims) (*store.User, error) {
	user, err := s.store.UserByOIDCSubject(ctx, claims.Subject)
	switch {
	case err == nil:
		return user, nil
	case !errors.Is(err, store.ErrNotFound):
		return nil, err
	}

	if claims.Email != "" && claims.EmailVerified {
		user, err := s.store.UserByEmail(ctx, claims.Email)
		switch {
		case err == nil:
			// First federated sign-in for a pre-authorised person: bind the subject
			// so future logins match on it directly.
			if user.OIDCSubject == "" {
				if err := s.store.BindOIDCSubject(ctx, user.ID, claims.Subject); err != nil {
					return nil, err
				}
				user.OIDCSubject = claims.Subject
				s.log.InfoContext(ctx, "linked an identity to an existing account",
					"user_id", user.ID, "email", user.Email)
			}
			return user, nil
		case !errors.Is(err, store.ErrNotFound):
			return nil, err
		}
	}

	if !auth.MayAutoProvision(oidcCfg, claims) {
		return nil, auth.ErrOIDCNotAllowed
	}

	created := &store.User{
		Email:       claims.Email,
		DisplayName: displayNameFor(claims),
		OIDCSubject: claims.Subject,
		// Auto-provisioned accounts are editors. Promoting somebody to admin stays
		// a deliberate act performed by an existing admin.
		IsAdmin: false,
	}
	if created.Email == "" {
		return nil, fmt.Errorf("%w: the provider supplied no email address", auth.ErrOIDCNotAllowed)
	}
	if err := s.store.CreateUser(ctx, created); err != nil {
		return nil, err
	}
	s.log.InfoContext(ctx, "auto-provisioned an account from single sign-on",
		"user_id", created.ID, "email", created.Email)
	return created, nil
}

func displayNameFor(claims *auth.Claims) string {
	if claims.Name != "" {
		return claims.Name
	}
	name, _, _ := strings.Cut(claims.Email, "@")
	return name
}
