package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Session is a signed-in browser. ID is the SHA-256 of the cookie token, never the
// token itself, so reading the database does not let anyone impersonate a user.
type Session struct {
	ID        string
	UserID    int64
	CreatedAt time.Time
	ExpiresAt time.Time
	UserAgent string
}

// CreateSession records a new session keyed by the hash of its cookie token.
func (s *Store) CreateSession(ctx context.Context, sess *Session) error {
	if sess.ID == "" {
		return errors.New("store: session id must not be empty")
	}
	if sess.ExpiresAt.IsZero() {
		return errors.New("store: session expiry must be set")
	}
	sess.CreatedAt = s.Now()

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (id, user_id, created_at, expires_at, user_agent)
		 VALUES (?, ?, ?, ?, ?)`,
		sess.ID, sess.UserID, sess.CreatedAt.Unix(), sess.ExpiresAt.UTC().Unix(), sess.UserAgent)
	if err != nil {
		return fmt.Errorf("store: create session: %w", err)
	}
	return nil
}

// SessionUser resolves a session token hash to its session and user in one query.
//
// Expired sessions and disabled users are treated as absent, so revoking access is
// immediate rather than waiting for a cookie to expire.
func (s *Store) SessionUser(ctx context.Context, id string) (*Session, *User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT s.id, s.user_id, s.created_at, s.expires_at, s.user_agent,
		        u.id, u.email, u.display_name, u.is_admin, u.password_hash, u.oidc_subject,
		        u.disabled, u.created_at, u.last_login_at, u.created_by
		 FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.id = ? AND s.expires_at > ? AND u.disabled = 0`,
		id, s.Now().Unix())

	var (
		sess         Session
		u            User
		sCreatedAt   int64
		sExpiresAt   int64
		uCreatedAt   int64
		uLastLoginAt *int64
	)
	err := row.Scan(
		&sess.ID, &sess.UserID, &sCreatedAt, &sExpiresAt, &sess.UserAgent,
		&u.ID, &u.Email, &u.DisplayName, &u.IsAdmin, &u.PasswordHash, &u.OIDCSubject,
		&u.Disabled, &uCreatedAt, &uLastLoginAt, &u.CreatedBy,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("store: read session: %w", err)
	}

	sess.CreatedAt = time.Unix(sCreatedAt, 0).UTC()
	sess.ExpiresAt = time.Unix(sExpiresAt, 0).UTC()
	u.CreatedAt = time.Unix(uCreatedAt, 0).UTC()
	u.LastLoginAt = timePtr(uLastLoginAt)
	return &sess, &u, nil
}

// ExtendSession pushes an active session's expiry out, giving sliding sessions.
func (s *Store) ExtendSession(ctx context.Context, id string, expiresAt time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET expires_at = ? WHERE id = ?`, expiresAt.UTC().Unix(), id)
	if err != nil {
		return fmt.Errorf("store: extend session: %w", err)
	}
	return requireAffected(res, "store: extend session")
}

// DeleteSession signs one browser out.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete session: %w", err)
	}
	return nil
}

// DeleteUserSessions signs a user out everywhere. Used when an admin disables an
// account or resets a password.
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("store: delete sessions for user %d: %w", userID, err)
	}
	return nil
}

// DeleteExpiredSessions clears rows whose expiry has passed. Called periodically so
// the table does not grow without bound.
func (s *Store) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, s.Now().Unix())
	if err != nil {
		return 0, fmt.Errorf("store: delete expired sessions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: delete expired sessions: read rows affected: %w", err)
	}
	return n, nil
}
