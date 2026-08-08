package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// EventKind tags a row in the events table.
type EventKind string

const (
	// KindMain is the headline countdown. At most one row may hold it, enforced by
	// a partial unique index rather than by convention.
	KindMain EventKind = "main"
)

// Event is a dated thing the site counts towards.
type Event struct {
	ID          int64
	Kind        EventKind
	Title       string
	Emoji       string
	TargetAt    time.Time
	Description string
	Visible     bool
	SortOrder   int
	CreatedAt   time.Time
}

// Passed reports whether the event's target is in the past relative to now.
func (e Event) Passed(now time.Time) bool { return !e.TargetAt.After(now) }

const eventColumns = `id, kind, title, emoji, target_at, description, visible, sort_order, created_at`

func scanEvent(sc interface{ Scan(...any) error }) (Event, error) {
	var (
		e         Event
		targetAt  int64
		createdAt int64
	)
	if err := sc.Scan(&e.ID, &e.Kind, &e.Title, &e.Emoji, &targetAt,
		&e.Description, &e.Visible, &e.SortOrder, &createdAt); err != nil {
		return Event{}, err
	}
	e.TargetAt = time.Unix(targetAt, 0).UTC()
	e.CreatedAt = time.Unix(createdAt, 0).UTC()
	return e, nil
}

// MainEvent returns the headline countdown, or ErrNotFound when none is configured.
func (s *Store) MainEvent(ctx context.Context) (*Event, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+eventColumns+` FROM events WHERE kind = ?`, KindMain)
	e, err := scanEvent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: read main event: %w", err)
	}
	return &e, nil
}

// Event returns one event by id.
func (s *Store) Event(ctx context.Context, id int64) (*Event, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+eventColumns+` FROM events WHERE id = ?`, id)
	e, err := scanEvent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: read event %d: %w", id, err)
	}
	return &e, nil
}

func (s *Store) CreateEvent(ctx context.Context, e *Event) error {
	if err := e.validate(); err != nil {
		return err
	}
	e.CreatedAt = s.Now()

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO events (kind, title, emoji, target_at, description, visible, sort_order, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Kind, e.Title, e.Emoji, e.TargetAt.UTC().Unix(),
		e.Description, e.Visible, e.SortOrder, e.CreatedAt.Unix())
	if err != nil {
		return fmt.Errorf("store: create event: %w", wrapSingleMain(err))
	}
	if e.ID, err = res.LastInsertId(); err != nil {
		return fmt.Errorf("store: create event: read id: %w", err)
	}
	return nil
}

// UpdateEvent writes an existing event back.
func (s *Store) UpdateEvent(ctx context.Context, e *Event) error {
	if err := e.validate(); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE events SET kind = ?, title = ?, emoji = ?, target_at = ?,
		 description = ?, visible = ?, sort_order = ? WHERE id = ?`,
		e.Kind, e.Title, e.Emoji, e.TargetAt.UTC().Unix(),
		e.Description, e.Visible, e.SortOrder, e.ID)
	if err != nil {
		return fmt.Errorf("store: update event %d: %w", e.ID, wrapSingleMain(err))
	}
	return requireAffected(res, fmt.Sprintf("store: update event %d", e.ID))
}

// DeleteEvent removes an event.
func (s *Store) DeleteEvent(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete event %d: %w", id, err)
	}
	return requireAffected(res, fmt.Sprintf("store: delete event %d", id))
}

// SetMainEvent creates or replaces the headline countdown in one step, so callers do
// not have to know whether one already exists.
func (s *Store) SetMainEvent(ctx context.Context, e *Event) error {
	e.Kind = KindMain
	if err := e.validate(); err != nil {
		return err
	}

	return s.tx(ctx, func(tx *sql.Tx) error {
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM events WHERE kind = ?`, KindMain).Scan(&id)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			now := s.Now()
			res, err := tx.ExecContext(ctx,
				`INSERT INTO events (kind, title, emoji, target_at, description, visible, sort_order, created_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				KindMain, e.Title, e.Emoji, e.TargetAt.UTC().Unix(),
				e.Description, e.Visible, e.SortOrder, now.Unix())
			if err != nil {
				return fmt.Errorf("store: insert main event: %w", err)
			}
			if e.ID, err = res.LastInsertId(); err != nil {
				return fmt.Errorf("store: insert main event: read id: %w", err)
			}
			e.CreatedAt = now
			return nil
		case err != nil:
			return fmt.Errorf("store: look up main event: %w", err)
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE events SET title = ?, emoji = ?, target_at = ?, description = ?,
			 visible = ?, sort_order = ? WHERE id = ?`,
			e.Title, e.Emoji, e.TargetAt.UTC().Unix(),
			e.Description, e.Visible, e.SortOrder, id); err != nil {
			return fmt.Errorf("store: update main event: %w", err)
		}
		e.ID = id
		return nil
	})
}

func (e *Event) validate() error {
	var errs []error
	if e.Kind != KindMain {
		errs = append(errs, fmt.Errorf("store: event kind %q must be %q", e.Kind, KindMain))
	}
	if strings.TrimSpace(e.Title) == "" {
		errs = append(errs, errors.New("store: event title must not be empty"))
	}
	if e.TargetAt.IsZero() {
		errs = append(errs, errors.New("store: event target time must be set"))
	}
	return errors.Join(errs...)
}

// ErrMainEventExists reports an attempt to create a second headline countdown.
var ErrMainEventExists = errors.New("store: a main event already exists")

// wrapSingleMain turns the partial unique index violation into a named error, since
// "UNIQUE constraint failed: events.kind" tells a caller nothing useful.
func wrapSingleMain(err error) error {
	if err != nil && strings.Contains(err.Error(), "events.kind") {
		return errors.Join(ErrMainEventExists, err)
	}
	return err
}

func requireAffected(res sql.Result, what string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: read rows affected: %w", what, err)
	}
	if n == 0 {
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	}
	return nil
}
