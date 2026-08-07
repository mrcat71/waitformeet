package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// mustUser creates a user or fails the test.
func mustUser(t *testing.T, s *Store, u *User) *User {
	t.Helper()
	if err := s.CreateUser(context.Background(), u); err != nil {
		t.Fatalf("CreateUser(%q) error = %v", u.Email, err)
	}
	return u
}

func TestUserLookupsAreCaseInsensitive(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	mustUser(t, s, &User{Email: "  Andrei@Example.COM ", DisplayName: "Andrei", IsAdmin: true})

	got, err := s.UserByEmail(ctx, "ANDREI@example.com")
	if err != nil {
		t.Fatalf("UserByEmail() error = %v", err)
	}
	if got.Email != "andrei@example.com" {
		t.Errorf("stored email = %q, want it normalised", got.Email)
	}

	if _, err := s.UserByEmail(ctx, "nobody@example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("UserByEmail(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestCreateUserRejectsDuplicates(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	mustUser(t, s, &User{Email: "a@example.com", IsAdmin: true})

	err := s.CreateUser(ctx, &User{Email: "A@EXAMPLE.COM"})
	if !errors.Is(err, ErrEmailTaken) {
		t.Errorf("CreateUser(duplicate email) error = %v, want ErrEmailTaken", err)
	}

	mustUser(t, s, &User{Email: "b@example.com", OIDCSubject: "sub-1"})
	err = s.CreateUser(ctx, &User{Email: "c@example.com", OIDCSubject: "sub-1"})
	if !errors.Is(err, ErrOIDCSubjectTaken) {
		t.Errorf("CreateUser(duplicate subject) error = %v, want ErrOIDCSubjectTaken", err)
	}
}

// The empty-string OIDC subject must not collide across users, otherwise only one
// password-only account could ever exist. The partial unique index handles this.
func TestManyUsersWithoutOIDCSubject(t *testing.T) {
	s := newTestStore(t)

	mustUser(t, s, &User{Email: "a@example.com", IsAdmin: true})
	mustUser(t, s, &User{Email: "b@example.com"})
	mustUser(t, s, &User{Email: "c@example.com"})
}

func TestValidateUserEmail(t *testing.T) {
	tests := []struct {
		name  string
		email string
		want  string
	}{
		{"empty", "", "must not be empty"},
		{"whitespace only", "   ", "must not be empty"},
		{"not an address", "andrei", "is not an email address"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			err := s.CreateUser(context.Background(), &User{Email: tt.email})
			if err == nil {
				t.Fatalf("CreateUser() error = nil, want an error mentioning %q", tt.want)
			}
			if !contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

// The last-admin guard is what stands between a stray click and being permanently
// locked out of your own site, so it gets the most thorough table here.
func TestLastAdminGuard(t *testing.T) {
	tests := []struct {
		name string
		// act performs the change against a store that already holds one admin
		// (admin@example.com) and one non-admin editor (editor@example.com).
		act     func(ctx context.Context, s *Store, admin, editor *User) error
		wantErr bool
	}{
		{
			name: "demoting the only admin is refused",
			act: func(ctx context.Context, s *Store, admin, _ *User) error {
				admin.IsAdmin = false
				return s.UpdateUser(ctx, admin)
			},
			wantErr: true,
		},
		{
			name: "disabling the only admin is refused",
			act: func(ctx context.Context, s *Store, admin, _ *User) error {
				admin.Disabled = true
				return s.UpdateUser(ctx, admin)
			},
			wantErr: true,
		},
		{
			name: "deleting the only admin is refused",
			act: func(ctx context.Context, s *Store, admin, _ *User) error {
				return s.DeleteUser(ctx, admin.ID)
			},
			wantErr: true,
		},
		{
			name: "renaming the only admin is fine",
			act: func(ctx context.Context, s *Store, admin, _ *User) error {
				admin.DisplayName = "The Admin"
				return s.UpdateUser(ctx, admin)
			},
		},
		{
			name: "deleting a non-admin is fine",
			act: func(ctx context.Context, s *Store, _, editor *User) error {
				return s.DeleteUser(ctx, editor.ID)
			},
		},
		{
			name: "demoting is fine once a second admin exists",
			act: func(ctx context.Context, s *Store, admin, editor *User) error {
				editor.IsAdmin = true
				if err := s.UpdateUser(ctx, editor); err != nil {
					return err
				}
				admin.IsAdmin = false
				return s.UpdateUser(ctx, admin)
			},
		},
		{
			name: "a disabled second admin does not count",
			act: func(ctx context.Context, s *Store, admin, editor *User) error {
				editor.IsAdmin = true
				editor.Disabled = true
				if err := s.UpdateUser(ctx, editor); err != nil {
					return err
				}
				admin.IsAdmin = false
				return s.UpdateUser(ctx, admin)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := newTestStore(t)

			admin := mustUser(t, s, &User{Email: "admin@example.com", IsAdmin: true})
			editor := mustUser(t, s, &User{Email: "editor@example.com"})

			err := tt.act(ctx, s, admin, editor)
			switch {
			case tt.wantErr && !errors.Is(err, ErrLastAdmin):
				t.Fatalf("error = %v, want ErrLastAdmin", err)
			case !tt.wantErr && err != nil:
				t.Fatalf("error = %v, want nil", err)
			}

			// Whatever happened, someone must still be able to administer the site.
			n, err := s.CountEnabledAdmins(ctx)
			if err != nil {
				t.Fatalf("CountEnabledAdmins() error = %v", err)
			}
			if n < 1 {
				t.Errorf("enabled admins = %d, want at least 1", n)
			}
		})
	}
}

// A refused change must leave nothing behind, including the session revocation that
// the same transaction performs.
func TestRefusedDisableRollsBackSessions(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	admin := mustUser(t, s, &User{Email: "admin@example.com", IsAdmin: true})
	sess := &Session{ID: "hash-1", UserID: admin.ID, ExpiresAt: fixedNow.Add(time.Hour)}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	admin.Disabled = true
	if err := s.UpdateUser(ctx, admin); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("UpdateUser() error = %v, want ErrLastAdmin", err)
	}

	if _, _, err := s.SessionUser(ctx, "hash-1"); err != nil {
		t.Errorf("session was revoked despite the update being refused: %v", err)
	}
}

func TestSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	u := mustUser(t, s, &User{Email: "admin@example.com", DisplayName: "Admin", IsAdmin: true})
	sess := &Session{ID: "hash-1", UserID: u.ID, ExpiresAt: fixedNow.Add(time.Hour), UserAgent: "test"}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	gotSess, gotUser, err := s.SessionUser(ctx, "hash-1")
	if err != nil {
		t.Fatalf("SessionUser() error = %v", err)
	}
	if gotUser.ID != u.ID || !gotUser.IsAdmin {
		t.Errorf("SessionUser() user = %+v, want admin %d", gotUser, u.ID)
	}
	if gotSess.UserAgent != "test" {
		t.Errorf("UserAgent = %q", gotSess.UserAgent)
	}

	if err := s.DeleteSession(ctx, "hash-1"); err != nil {
		t.Fatalf("DeleteSession() error = %v", err)
	}
	if _, _, err := s.SessionUser(ctx, "hash-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SessionUser() after delete = %v, want ErrNotFound", err)
	}
}

func TestSessionRejectedWhenExpiredOrUserDisabled(t *testing.T) {
	tests := []struct {
		name      string
		expiresAt time.Time
		disable   bool
	}{
		{name: "expired", expiresAt: fixedNow.Add(-time.Second)},
		{name: "expiring exactly now", expiresAt: fixedNow},
		{name: "user disabled", expiresAt: fixedNow.Add(time.Hour), disable: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := newTestStore(t)

			// A second admin exists so that disabling the first is allowed.
			mustUser(t, s, &User{Email: "other-admin@example.com", IsAdmin: true})
			u := mustUser(t, s, &User{Email: "admin@example.com", IsAdmin: true})

			if err := s.CreateSession(ctx, &Session{ID: "h", UserID: u.ID, ExpiresAt: tt.expiresAt}); err != nil {
				t.Fatalf("CreateSession() error = %v", err)
			}
			if tt.disable {
				u.Disabled = true
				if err := s.UpdateUser(ctx, u); err != nil {
					t.Fatalf("UpdateUser() error = %v", err)
				}
			}

			if _, _, err := s.SessionUser(ctx, "h"); !errors.Is(err, ErrNotFound) {
				t.Errorf("SessionUser() = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestDisablingUserRevokesTheirSessions(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	mustUser(t, s, &User{Email: "admin@example.com", IsAdmin: true})
	editor := mustUser(t, s, &User{Email: "editor@example.com"})

	if err := s.CreateSession(ctx, &Session{ID: "h", UserID: editor.ID, ExpiresAt: fixedNow.Add(time.Hour)}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	editor.Disabled = true
	if err := s.UpdateUser(ctx, editor); err != nil {
		t.Fatalf("UpdateUser() error = %v", err)
	}

	var n int
	if err := s.DB().QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE user_id = ?`, editor.ID).Scan(&n); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if n != 0 {
		t.Errorf("sessions remaining = %d, want 0", n)
	}
}

func TestDeleteExpiredSessions(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	u := mustUser(t, s, &User{Email: "admin@example.com", IsAdmin: true})
	for id, exp := range map[string]time.Time{
		"live":  fixedNow.Add(time.Hour),
		"stale": fixedNow.Add(-time.Hour),
	} {
		if err := s.CreateSession(ctx, &Session{ID: id, UserID: u.ID, ExpiresAt: exp}); err != nil {
			t.Fatalf("CreateSession(%q) error = %v", id, err)
		}
	}

	n, err := s.DeleteExpiredSessions(ctx)
	if err != nil {
		t.Fatalf("DeleteExpiredSessions() error = %v", err)
	}
	if n != 1 {
		t.Errorf("deleted %d sessions, want 1", n)
	}
	if _, _, err := s.SessionUser(ctx, "live"); err != nil {
		t.Errorf("live session was removed: %v", err)
	}
}

func TestInviteRedemption(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	admin := mustUser(t, s, &User{Email: "admin@example.com", IsAdmin: true})
	inv := &Invite{
		TokenHash:   "token-hash",
		Email:       "Guest@Example.com",
		DisplayName: "Guest",
		IsAdmin:     true,
		ExpiresAt:   fixedNow.Add(48 * time.Hour),
		CreatedBy:   &admin.ID,
	}
	if err := s.CreateInvite(ctx, inv); err != nil {
		t.Fatalf("CreateInvite() error = %v", err)
	}

	user, err := s.RedeemInvite(ctx, "token-hash", "hashed-password")
	if err != nil {
		t.Fatalf("RedeemInvite() error = %v", err)
	}
	if user.Email != "guest@example.com" {
		t.Errorf("Email = %q, want it normalised", user.Email)
	}
	if !user.IsAdmin {
		t.Error("IsAdmin = false, want the invite's admin flag to carry over")
	}

	// A second use must fail, and must not create a second account.
	if _, err := s.RedeemInvite(ctx, "token-hash", "x"); !errors.Is(err, ErrInviteUsed) {
		t.Errorf("second RedeemInvite() error = %v, want ErrInviteUsed", err)
	}
	users, err := s.Users(ctx)
	if err != nil {
		t.Fatalf("Users() error = %v", err)
	}
	if len(users) != 2 {
		t.Errorf("user count = %d, want 2", len(users))
	}
}

func TestRedeemInviteRejections(t *testing.T) {
	tests := []struct {
		name      string
		hash      string
		expiresAt time.Time
		wantErr   error
	}{
		{name: "unknown token", hash: "nope", expiresAt: fixedNow.Add(time.Hour), wantErr: ErrNotFound},
		{name: "expired", hash: "token", expiresAt: fixedNow.Add(-time.Second), wantErr: ErrInviteExpired},
		{name: "expiring exactly now", hash: "token", expiresAt: fixedNow, wantErr: ErrInviteExpired},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := newTestStore(t)

			if err := s.CreateInvite(ctx, &Invite{
				TokenHash: "token", Email: "guest@example.com", ExpiresAt: tt.expiresAt,
			}); err != nil {
				t.Fatalf("CreateInvite() error = %v", err)
			}

			_, err := s.RedeemInvite(ctx, tt.hash, "hash")
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("RedeemInvite() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// Deleting an account must not erase the notes that person left behind.
func TestDeletingUserKeepsTheirNotes(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	mustUser(t, s, &User{Email: "admin@example.com", IsAdmin: true})
	editor := mustUser(t, s, &User{Email: "editor@example.com", DisplayName: "Editor"})

	note := &Note{AuthorID: &editor.ID, AuthorName: "Editor", Body: "miss you", Visible: true}
	if err := s.CreateNote(ctx, note); err != nil {
		t.Fatalf("CreateNote() error = %v", err)
	}

	if err := s.DeleteUser(ctx, editor.ID); err != nil {
		t.Fatalf("DeleteUser() error = %v", err)
	}

	got, err := s.Note(ctx, note.ID)
	if err != nil {
		t.Fatalf("Note() after deleting its author: %v", err)
	}
	if got.Body != "miss you" {
		t.Errorf("Body = %q, want the note preserved", got.Body)
	}
	if got.AuthorName != "Editor" {
		t.Errorf("AuthorName = %q, want the denormalised name preserved", got.AuthorName)
	}
	if got.AuthorID != nil {
		t.Errorf("AuthorID = %v, want nil after the account was removed", *got.AuthorID)
	}
}
