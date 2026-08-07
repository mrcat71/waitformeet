package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrEmailTaken reports a duplicate email address.
	ErrEmailTaken = errors.New("store: that email is already registered")
	// ErrOIDCSubjectTaken reports an OIDC identity already bound to another user.
	ErrOIDCSubjectTaken = errors.New("store: that identity is already linked to another user")
	// ErrLastAdmin reports a change that would leave the site with no enabled admin.
	// Nobody would then be able to manage users or visibility, which means being
	// permanently locked out of your own site.
	ErrLastAdmin = errors.New("store: this would leave no enabled admin")
)

// User is someone allowed to sign in and edit the site. Everyone who can sign in may
// edit content; IsAdmin additionally grants user management and visibility control.
//
// A user may authenticate by password, by OIDC, or both. An empty PasswordHash means
// password login is simply unavailable for them.
type User struct {
	ID           int64
	Email        string
	DisplayName  string
	IsAdmin      bool
	PasswordHash string
	OIDCSubject  string
	Disabled     bool
	CreatedAt    time.Time
	LastLoginAt  *time.Time
	CreatedBy    *int64
}

// Name returns the best available human label for the user.
func (u User) Name() string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Email
}

// NormalizeEmail lowercases and trims an address so lookups are case insensitive.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

const userColumns = `id, email, display_name, is_admin, password_hash, oidc_subject,
	disabled, created_at, last_login_at, created_by`

func scanUser(sc interface{ Scan(...any) error }) (User, error) {
	var (
		u           User
		createdAt   int64
		lastLoginAt *int64
	)
	if err := sc.Scan(&u.ID, &u.Email, &u.DisplayName, &u.IsAdmin, &u.PasswordHash,
		&u.OIDCSubject, &u.Disabled, &createdAt, &lastLoginAt, &u.CreatedBy); err != nil {
		return User{}, err
	}
	u.CreatedAt = time.Unix(createdAt, 0).UTC()
	u.LastLoginAt = timePtr(lastLoginAt)
	return u, nil
}

// UserByID returns one user.
func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	return s.userWhere(ctx, `id = ?`, id)
}

// UserByEmail looks a user up by address, case insensitively.
func (s *Store) UserByEmail(ctx context.Context, email string) (*User, error) {
	return s.userWhere(ctx, `email = ?`, NormalizeEmail(email))
}

// UserByOIDCSubject finds the user bound to an OIDC subject claim.
func (s *Store) UserByOIDCSubject(ctx context.Context, subject string) (*User, error) {
	if strings.TrimSpace(subject) == "" {
		return nil, ErrNotFound
	}
	return s.userWhere(ctx, `oidc_subject = ?`, subject)
}

func (s *Store) userWhere(ctx context.Context, where string, args ...any) (*User, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE `+where, args...)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: read user: %w", err)
	}
	return &u, nil
}

// Users lists everyone, admins first and then alphabetically.
func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+userColumns+` FROM users ORDER BY is_admin DESC, email ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list users: %w", err)
	}
	defer rows.Close()

	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan user: %w", err)
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate users: %w", err)
	}
	return out, nil
}

// CountEnabledAdmins returns how many admins can currently sign in.
func (s *Store) CountEnabledAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM users WHERE is_admin = 1 AND disabled = 0`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count admins: %w", err)
	}
	return n, nil
}

// UserCount returns the total number of accounts, which tells the bootstrap path
// whether this is a brand new deployment.
func (s *Store) UserCount(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count users: %w", err)
	}
	return n, nil
}

// CreateUser inserts a user and fills in the assigned ID.
func (s *Store) CreateUser(ctx context.Context, u *User) error {
	u.Email = NormalizeEmail(u.Email)
	if err := u.validate(); err != nil {
		return err
	}
	u.CreatedAt = s.Now()

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (email, display_name, is_admin, password_hash, oidc_subject,
		 disabled, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		u.Email, u.DisplayName, u.IsAdmin, u.PasswordHash, u.OIDCSubject,
		u.Disabled, u.CreatedAt.Unix(), u.CreatedBy)
	if err != nil {
		return fmt.Errorf("store: create user: %w", wrapUserConflict(err))
	}
	if u.ID, err = res.LastInsertId(); err != nil {
		return fmt.Errorf("store: create user: read id: %w", err)
	}
	return nil
}

// UpdateUser writes profile, role and status changes back.
//
// The guard is expressed as a post-condition rather than a list of forbidden cases:
// whatever the change was, it must not leave zero enabled admins.
func (s *Store) UpdateUser(ctx context.Context, u *User) error {
	u.Email = NormalizeEmail(u.Email)
	if err := u.validate(); err != nil {
		return err
	}

	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE users SET email = ?, display_name = ?, is_admin = ?, oidc_subject = ?,
			 disabled = ? WHERE id = ?`,
			u.Email, u.DisplayName, u.IsAdmin, u.OIDCSubject, u.Disabled, u.ID)
		if err != nil {
			return fmt.Errorf("store: update user %d: %w", u.ID, wrapUserConflict(err))
		}
		if err := requireAffected(res, fmt.Sprintf("store: update user %d", u.ID)); err != nil {
			return err
		}

		// Losing admin rights or being disabled must also end that person's sessions,
		// otherwise an open tab keeps the privileges they no longer have.
		if u.Disabled {
			if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, u.ID); err != nil {
				return fmt.Errorf("store: revoke sessions for user %d: %w", u.ID, err)
			}
		}
		return assertAdminRemains(ctx, tx)
	})
}

// DeleteUser removes an account, its sessions, and its unused invites.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("store: delete user %d: %w", id, err)
		}
		if err := requireAffected(res, fmt.Sprintf("store: delete user %d", id)); err != nil {
			return err
		}
		return assertAdminRemains(ctx, tx)
	})
}

// SetPassword stores a new password hash, or clears it when hash is empty, which
// disables password login for that user without touching their OIDC identity.
func (s *Store) SetPassword(ctx context.Context, id int64, hash string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, id)
	if err != nil {
		return fmt.Errorf("store: set password for user %d: %w", id, err)
	}
	return requireAffected(res, fmt.Sprintf("store: set password for user %d", id))
}

// BindOIDCSubject links an OIDC identity to a user on their first federated login.
func (s *Store) BindOIDCSubject(ctx context.Context, id int64, subject string) error {
	if strings.TrimSpace(subject) == "" {
		return errors.New("store: refusing to bind an empty OIDC subject")
	}
	res, err := s.db.ExecContext(ctx, `UPDATE users SET oidc_subject = ? WHERE id = ?`, subject, id)
	if err != nil {
		return fmt.Errorf("store: bind identity to user %d: %w", id, wrapUserConflict(err))
	}
	return requireAffected(res, fmt.Sprintf("store: bind identity to user %d", id))
}

// TouchLogin records a successful sign-in.
func (s *Store) TouchLogin(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET last_login_at = ? WHERE id = ?`, s.Now().Unix(), id)
	if err != nil {
		return fmt.Errorf("store: record login for user %d: %w", id, err)
	}
	return nil
}

// assertAdminRemains rolls the surrounding transaction back when a change would
// leave the site without anyone able to administer it.
func assertAdminRemains(ctx context.Context, tx *sql.Tx) error {
	var n int
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM users WHERE is_admin = 1 AND disabled = 0`).Scan(&n); err != nil {
		return fmt.Errorf("store: count admins: %w", err)
	}
	if n == 0 {
		return ErrLastAdmin
	}
	return nil
}

func (u *User) validate() error {
	var errs []error
	if u.Email == "" {
		errs = append(errs, errors.New("store: user email must not be empty"))
	} else if !strings.Contains(u.Email, "@") {
		errs = append(errs, fmt.Errorf("store: %q is not an email address", u.Email))
	}
	return errors.Join(errs...)
}

// wrapUserConflict names the unique index violations, since the raw SQLite message
// is not something to show a person.
func wrapUserConflict(err error) error {
	if err == nil {
		return nil
	}
	switch msg := err.Error(); {
	case strings.Contains(msg, "users.email"):
		return errors.Join(ErrEmailTaken, err)
	case strings.Contains(msg, "users.oidc_subject"):
		return errors.Join(ErrOIDCSubjectTaken, err)
	default:
		return err
	}
}
