package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var (
	// ErrInviteUsed reports an invite link that has already been redeemed.
	ErrInviteUsed = errors.New("store: this invite has already been used")
	// ErrInviteExpired reports an invite link past its expiry.
	ErrInviteExpired = errors.New("store: this invite has expired")
)

// Invite is a one-time link that lets someone set their own password instead of an
// admin choosing one and sending it over a chat app.
//
// TokenHash is the SHA-256 of the token in the link. The token itself is shown once,
// at creation time, and is never stored.
type Invite struct {
	ID          int64
	TokenHash   string
	Email       string
	DisplayName string
	IsAdmin     bool
	CreatedAt   time.Time
	ExpiresAt   time.Time
	UsedAt      *time.Time
	CreatedBy   *int64
}

// Usable reports whether the invite can still be redeemed at the given time.
func (i Invite) Usable(now time.Time) error {
	if i.UsedAt != nil {
		return ErrInviteUsed
	}
	if !i.ExpiresAt.After(now) {
		return ErrInviteExpired
	}
	return nil
}

const inviteColumns = `id, token_hash, email, display_name, is_admin,
	created_at, expires_at, used_at, created_by`

func scanInvite(sc interface{ Scan(...any) error }) (Invite, error) {
	var (
		inv       Invite
		createdAt int64
		expiresAt int64
		usedAt    *int64
	)
	if err := sc.Scan(&inv.ID, &inv.TokenHash, &inv.Email, &inv.DisplayName, &inv.IsAdmin,
		&createdAt, &expiresAt, &usedAt, &inv.CreatedBy); err != nil {
		return Invite{}, err
	}
	inv.CreatedAt = time.Unix(createdAt, 0).UTC()
	inv.ExpiresAt = time.Unix(expiresAt, 0).UTC()
	inv.UsedAt = timePtr(usedAt)
	return inv, nil
}

// CreateInvite stores a pending invite.
func (s *Store) CreateInvite(ctx context.Context, inv *Invite) error {
	inv.Email = NormalizeEmail(inv.Email)
	if inv.TokenHash == "" {
		return errors.New("store: invite token hash must not be empty")
	}
	if inv.ExpiresAt.IsZero() {
		return errors.New("store: invite expiry must be set")
	}
	inv.CreatedAt = s.Now()

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO invites (token_hash, email, display_name, is_admin,
		 created_at, expires_at, created_by) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		inv.TokenHash, inv.Email, inv.DisplayName, inv.IsAdmin,
		inv.CreatedAt.Unix(), inv.ExpiresAt.UTC().Unix(), inv.CreatedBy)
	if err != nil {
		return fmt.Errorf("store: create invite: %w", err)
	}
	if inv.ID, err = res.LastInsertId(); err != nil {
		return fmt.Errorf("store: create invite: read id: %w", err)
	}
	return nil
}

// InviteByTokenHash looks up an invite by the hash of its token.
func (s *Store) InviteByTokenHash(ctx context.Context, hash string) (*Invite, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+inviteColumns+` FROM invites WHERE token_hash = ?`, hash)
	inv, err := scanInvite(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: read invite: %w", err)
	}
	return &inv, nil
}

// PendingInvites lists invites that have not been redeemed yet, newest first.
func (s *Store) PendingInvites(ctx context.Context) ([]Invite, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+inviteColumns+` FROM invites WHERE used_at IS NULL ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list invites: %w", err)
	}
	defer rows.Close()

	var out []Invite
	for rows.Next() {
		inv, err := scanInvite(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan invite: %w", err)
		}
		out = append(out, inv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate invites: %w", err)
	}
	return out, nil
}

// RedeemInvite marks an invite used and creates the account it describes, in one
// transaction so a crash cannot leave a consumed invite with no user behind it.
//
// passwordHash is the hash of the password the invitee just chose.
func (s *Store) RedeemInvite(ctx context.Context, hash, passwordHash string) (*User, error) {
	var created *User

	err := s.tx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `SELECT `+inviteColumns+` FROM invites WHERE token_hash = ?`, hash)
		inv, err := scanInvite(row)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("store: read invite: %w", err)
		}

		now := s.Now()
		if err := inv.Usable(now); err != nil {
			return err
		}

		res, err := tx.ExecContext(ctx,
			`INSERT INTO users (email, display_name, is_admin, password_hash, created_at, created_by)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			inv.Email, inv.DisplayName, inv.IsAdmin, passwordHash, now.Unix(), inv.CreatedBy)
		if err != nil {
			return fmt.Errorf("store: create user from invite: %w", wrapUserConflict(err))
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("store: create user from invite: read id: %w", err)
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE invites SET used_at = ? WHERE id = ?`, now.Unix(), inv.ID); err != nil {
			return fmt.Errorf("store: mark invite used: %w", err)
		}

		created = &User{
			ID:           id,
			Email:        inv.Email,
			DisplayName:  inv.DisplayName,
			IsAdmin:      inv.IsAdmin,
			PasswordHash: passwordHash,
			CreatedAt:    now,
			CreatedBy:    inv.CreatedBy,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// DeleteInvite revokes a pending invite.
func (s *Store) DeleteInvite(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM invites WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete invite %d: %w", id, err)
	}
	return requireAffected(res, fmt.Sprintf("store: delete invite %d", id))
}

// DeleteExpiredInvites clears invites that can no longer be redeemed.
func (s *Store) DeleteExpiredInvites(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM invites WHERE used_at IS NULL AND expires_at <= ?`, s.Now().Unix())
	if err != nil {
		return 0, fmt.Errorf("store: delete expired invites: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: delete expired invites: read rows affected: %w", err)
	}
	return n, nil
}
