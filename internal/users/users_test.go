package users

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mrcat71/waitformeet/internal/auth"
	"github.com/mrcat71/waitformeet/internal/config"
	"github.com/mrcat71/waitformeet/internal/store"
)

func newTestService(t *testing.T) (*Service, *store.Store) {
	t.Helper()

	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { st.Close() })

	return NewService(st, slog.New(slog.NewTextHandler(io.Discard, nil))), st
}

func testConfig(t *testing.T, email, password string) *config.Config {
	t.Helper()

	base, err := url.Parse("https://wait.example.com")
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	return &config.Config{
		BaseURL:                base,
		BootstrapAdminEmail:    email,
		BootstrapAdminPassword: password,
	}
}

func TestBootstrapWithPassword(t *testing.T) {
	ctx := context.Background()
	svc, st := newTestService(t)

	cfg := testConfig(t, "Admin@Example.com", "a-long-enough-password")
	if err := svc.Bootstrap(ctx, cfg); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}

	user, err := st.UserByEmail(ctx, "admin@example.com")
	if err != nil {
		t.Fatalf("UserByEmail() error = %v", err)
	}
	if !user.IsAdmin {
		t.Error("the bootstrap account is not an admin")
	}
	if err := auth.VerifyPassword(user.PasswordHash, "a-long-enough-password"); err != nil {
		t.Errorf("the configured password does not verify: %v", err)
	}
}

// Without a password the deployment gets an invitation link instead, so the
// password is chosen by the person rather than living in a Secret forever.
func TestBootstrapWithoutPasswordIssuesAnInvite(t *testing.T) {
	ctx := context.Background()
	svc, st := newTestService(t)

	if err := svc.Bootstrap(ctx, testConfig(t, "admin@example.com", "")); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}

	if _, err := st.UserByEmail(ctx, "admin@example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("an account was created, want only an invitation")
	}

	invites, err := st.PendingInvites(ctx)
	if err != nil {
		t.Fatalf("PendingInvites() error = %v", err)
	}
	if len(invites) != 1 {
		t.Fatalf("pending invites = %d, want 1", len(invites))
	}
	if !invites[0].IsAdmin {
		t.Error("the bootstrap invitation does not grant admin")
	}
}

func TestBootstrapIsANoOpWhenAccountsExist(t *testing.T) {
	ctx := context.Background()
	svc, st := newTestService(t)

	if err := st.CreateUser(ctx, &store.User{Email: "someone@example.com", IsAdmin: true}); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}

	if err := svc.Bootstrap(ctx, testConfig(t, "admin@example.com", "a-long-enough-password")); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}

	users, err := st.Users(ctx)
	if err != nil {
		t.Fatalf("Users() error = %v", err)
	}
	if len(users) != 1 {
		t.Errorf("user count = %d, want the existing account untouched", len(users))
	}
}

func TestBootstrapRejectsAWeakPassword(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)

	err := svc.Bootstrap(ctx, testConfig(t, "admin@example.com", "short"))
	if !errors.Is(err, auth.ErrPasswordTooShort) {
		t.Errorf("Bootstrap() error = %v, want ErrPasswordTooShort", err)
	}
}

func TestInviteAndRedeem(t *testing.T) {
	ctx := context.Background()
	svc, st := newTestService(t)

	admin := &store.User{Email: "admin@example.com", IsAdmin: true}
	if err := st.CreateUser(ctx, admin); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}

	token, inv, err := svc.Invite(ctx, admin, "Her@Example.com", "Her", false)
	if err != nil {
		t.Fatalf("Invite() error = %v", err)
	}
	if token == "" {
		t.Fatal("Invite() returned an empty token")
	}
	if inv.TokenHash == token {
		t.Error("the raw token was stored; only its hash should be")
	}

	user, err := svc.Redeem(ctx, token, "a-long-enough-password")
	if err != nil {
		t.Fatalf("Redeem() error = %v", err)
	}
	if user.Email != "her@example.com" {
		t.Errorf("Email = %q, want it normalised", user.Email)
	}
	if user.IsAdmin {
		t.Error("IsAdmin = true, want the invitation's non-admin flag respected")
	}
	if err := auth.VerifyPassword(user.PasswordHash, "a-long-enough-password"); err != nil {
		t.Errorf("the chosen password does not verify: %v", err)
	}
}

func TestInviteRejections(t *testing.T) {
	ctx := context.Background()
	svc, st := newTestService(t)

	if err := st.CreateUser(ctx, &store.User{Email: "taken@example.com", IsAdmin: true}); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}

	tests := []struct {
		name    string
		email   string
		wantErr error
		wantMsg string
	}{
		{name: "already registered", email: "Taken@Example.com", wantErr: store.ErrEmailTaken},
		{name: "not an address", email: "nonsense", wantMsg: "is not an email address"},
		{name: "empty", email: "", wantMsg: "is not an email address"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := svc.Invite(ctx, nil, tt.email, "", false)
			if err == nil {
				t.Fatal("Invite() error = nil, want an error")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantMsg != "" && !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error %q does not mention %q", err, tt.wantMsg)
			}
		})
	}
}

func TestRedeemRejectsAWeakPassword(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)

	token, _, err := svc.Invite(ctx, nil, "her@example.com", "Her", false)
	if err != nil {
		t.Fatalf("Invite() error = %v", err)
	}

	if _, err := svc.Redeem(ctx, token, "short"); !errors.Is(err, auth.ErrPasswordTooShort) {
		t.Errorf("Redeem() error = %v, want ErrPasswordTooShort", err)
	}

	// The invitation must survive a rejected attempt so the person can try again.
	invites, err := svc.store.PendingInvites(ctx)
	if err != nil {
		t.Fatalf("PendingInvites() error = %v", err)
	}
	if len(invites) != 1 {
		t.Errorf("pending invites = %d, want the invitation still usable", len(invites))
	}
}

func TestInviteURL(t *testing.T) {
	tests := []struct {
		name string
		base string
		want string
	}{
		{
			name: "plain host",
			base: "https://wait.example.com",
			want: "https://wait.example.com/invite?token=abc",
		},
		{
			name: "trailing slash",
			base: "https://wait.example.com/",
			want: "https://wait.example.com/invite?token=abc",
		},
		{
			name: "sub path",
			base: "https://example.com/wait",
			want: "https://example.com/wait/invite?token=abc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base, err := url.Parse(tt.base)
			if err != nil {
				t.Fatalf("url.Parse() error = %v", err)
			}
			if got := InviteURL(base, "abc"); got != tt.want {
				t.Errorf("InviteURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSelfServiceGuards(t *testing.T) {
	ctx := context.Background()
	svc, st := newTestService(t)

	admin := &store.User{Email: "admin@example.com", DisplayName: "Admin", IsAdmin: true}
	if err := st.CreateUser(ctx, admin); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	other := &store.User{Email: "other@example.com", IsAdmin: true}
	if err := st.CreateUser(ctx, other); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}

	t.Run("cannot remove own admin rights", func(t *testing.T) {
		err := svc.Update(ctx, admin, admin, "Admin", false, false)
		if !errors.Is(err, ErrSelfDemotion) {
			t.Errorf("Update() error = %v, want ErrSelfDemotion", err)
		}
	})

	t.Run("cannot disable oneself", func(t *testing.T) {
		err := svc.Update(ctx, admin, admin, "Admin", true, true)
		if !errors.Is(err, ErrSelfDemotion) {
			t.Errorf("Update() error = %v, want ErrSelfDemotion", err)
		}
	})

	t.Run("cannot delete oneself", func(t *testing.T) {
		if err := svc.Delete(ctx, admin, admin.ID); !errors.Is(err, ErrSelfDeletion) {
			t.Errorf("Delete() error = %v, want ErrSelfDeletion", err)
		}
	})

	t.Run("renaming oneself is fine", func(t *testing.T) {
		if err := svc.Update(ctx, admin, admin, "Renamed", true, false); err != nil {
			t.Errorf("Update() error = %v, want nil", err)
		}
	})

	t.Run("changing someone else is fine", func(t *testing.T) {
		if err := svc.Update(ctx, admin, other, "Other", false, false); err != nil {
			t.Errorf("Update() error = %v, want nil", err)
		}
	})
}

func TestSetPasswordSignsTheUserOut(t *testing.T) {
	ctx := context.Background()
	svc, st := newTestService(t)

	user := &store.User{Email: "her@example.com", IsAdmin: true}
	if err := st.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	if err := st.CreateSession(ctx, &store.Session{
		ID: "hash", UserID: user.ID, ExpiresAt: st.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	if err := svc.SetPassword(ctx, user.ID, "a-brand-new-password"); err != nil {
		t.Fatalf("SetPassword() error = %v", err)
	}

	if _, _, err := st.SessionUser(ctx, "hash"); !errors.Is(err, store.ErrNotFound) {
		t.Error("the old session survived a password change")
	}
}

// ResolveOIDC decides who gets in through single sign-on, so its precedence order
// is worth pinning down exactly.
func TestResolveOIDC(t *testing.T) {
	tests := []struct {
		name string
		// seed prepares the accounts that already exist.
		seed    func(t *testing.T, ctx context.Context, st *store.Store)
		oidcCfg config.OIDCConfig
		claims  auth.Claims
		wantErr error
		check   func(t *testing.T, ctx context.Context, st *store.Store, got *store.User)
	}{
		{
			name: "a bound subject wins outright",
			seed: func(t *testing.T, ctx context.Context, st *store.Store) {
				mustCreate(t, ctx, st, &store.User{
					Email: "her@example.com", OIDCSubject: "sub-1", DisplayName: "Her",
				})
			},
			claims: auth.Claims{Subject: "sub-1", Email: "somethingelse@example.com"},
			check: func(t *testing.T, _ context.Context, _ *store.Store, got *store.User) {
				if got.Email != "her@example.com" {
					t.Errorf("Email = %q, want the account the subject is bound to", got.Email)
				}
			},
		},
		{
			name: "a pre-authorised address adopts the account and binds the subject",
			seed: func(t *testing.T, ctx context.Context, st *store.Store) {
				mustCreate(t, ctx, st, &store.User{Email: "her@example.com", DisplayName: "Her"})
			},
			claims: auth.Claims{Subject: "sub-1", Email: "her@example.com", EmailVerified: true},
			check: func(t *testing.T, ctx context.Context, st *store.Store, got *store.User) {
				if got.OIDCSubject != "sub-1" {
					t.Errorf("OIDCSubject = %q, want it bound on first sign-in", got.OIDCSubject)
				}
				stored, err := st.UserByOIDCSubject(ctx, "sub-1")
				if err != nil {
					t.Fatalf("UserByOIDCSubject() error = %v", err)
				}
				if stored.ID != got.ID {
					t.Error("the binding was not persisted")
				}
			},
		},
		{
			// An unverified address is the provider reporting what somebody typed.
			// Adopting an account on that basis would be an account takeover.
			name: "an unverified address adopts nothing",
			seed: func(t *testing.T, ctx context.Context, st *store.Store) {
				mustCreate(t, ctx, st, &store.User{Email: "her@example.com", IsAdmin: true})
			},
			claims:  auth.Claims{Subject: "sub-1", Email: "her@example.com", EmailVerified: false},
			wantErr: auth.ErrOIDCNotAllowed,
		},
		{
			name:    "an unknown identity is refused when auto-provisioning is off",
			claims:  auth.Claims{Subject: "sub-1", Email: "stranger@example.com", EmailVerified: true},
			wantErr: auth.ErrOIDCNotAllowed,
		},
		{
			name:    "an unknown identity is admitted when a group matches",
			oidcCfg: config.OIDCConfig{AutoProvision: true, AllowedGroups: []string{"couple"}},
			claims: auth.Claims{
				Subject: "sub-1", Email: "her@example.com", EmailVerified: true,
				Name: "Her", Groups: []string{"couple"},
			},
			check: func(t *testing.T, _ context.Context, _ *store.Store, got *store.User) {
				if got.IsAdmin {
					t.Error("an auto-provisioned account was made admin; promotion must stay deliberate")
				}
				if got.DisplayName != "Her" {
					t.Errorf("DisplayName = %q, want the name claim", got.DisplayName)
				}
			},
		},
		{
			name:    "auto-provisioning still needs an address",
			oidcCfg: config.OIDCConfig{AutoProvision: true, AllowedGroups: []string{"couple"}},
			claims:  auth.Claims{Subject: "sub-1", Groups: []string{"couple"}},
			wantErr: auth.ErrOIDCNotAllowed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			svc, st := newTestService(t)
			if tt.seed != nil {
				tt.seed(t, ctx, st)
			}

			oidcCfg := tt.oidcCfg
			oidcCfg.Enabled = true

			got, err := svc.ResolveOIDC(ctx, oidcCfg, &tt.claims)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ResolveOIDC() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveOIDC() error = %v", err)
			}
			if tt.check != nil {
				tt.check(t, ctx, st, got)
			}
		})
	}
}

func mustCreate(t *testing.T, ctx context.Context, st *store.Store, u *store.User) {
	t.Helper()
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatalf("CreateUser(%q) error = %v", u.Email, err)
	}
}
