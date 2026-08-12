package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Note is a short message left on the wall.
type Note struct {
	ID       int64
	AuthorID *int64
	// AuthorName is stored alongside the id so that deleting an account does not
	// blank out the notes that person left.
	AuthorName string
	Body       string
	CreatedAt  time.Time
	Pinned     bool
	Visible    bool
}

const noteColumns = `id, author_id, author_name, body, created_at, pinned, visible`

func scanNote(sc interface{ Scan(...any) error }) (Note, error) {
	var (
		n         Note
		createdAt int64
	)
	if err := sc.Scan(&n.ID, &n.AuthorID, &n.AuthorName, &n.Body, &createdAt,
		&n.Pinned, &n.Visible); err != nil {
		return Note{}, err
	}
	n.CreatedAt = time.Unix(createdAt, 0).UTC()
	return n, nil
}

// Notes lists notes newest first, pinned ones ahead of the rest.
func (s *Store) Notes(ctx context.Context, includeHidden bool) ([]Note, error) {
	query := `SELECT ` + noteColumns + ` FROM notes`
	if !includeHidden {
		query += ` WHERE visible = 1`
	}
	query += ` ORDER BY pinned DESC, created_at DESC, id DESC`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("store: list notes: %w", err)
	}
	defer rows.Close()

	var out []Note
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan note: %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate notes: %w", err)
	}
	return out, nil
}

// Note returns one note by id.
func (s *Store) Note(ctx context.Context, id int64) (*Note, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+noteColumns+` FROM notes WHERE id = ?`, id)
	n, err := scanNote(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: read note %d: %w", id, err)
	}
	return &n, nil
}

// CreateNote adds a note and fills in its ID.
func (s *Store) CreateNote(ctx context.Context, n *Note) error {
	if strings.TrimSpace(n.Body) == "" {
		return errors.New("store: note body must not be empty")
	}
	n.CreatedAt = s.Now()

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO notes (author_id, author_name, body, created_at, pinned, visible)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		n.AuthorID, n.AuthorName, n.Body, n.CreatedAt.Unix(), n.Pinned, n.Visible)
	if err != nil {
		return fmt.Errorf("store: create note: %w", err)
	}
	if n.ID, err = res.LastInsertId(); err != nil {
		return fmt.Errorf("store: create note: read id: %w", err)
	}
	return nil
}

// UpdateNote writes an edited note back.
func (s *Store) UpdateNote(ctx context.Context, n *Note) error {
	if strings.TrimSpace(n.Body) == "" {
		return errors.New("store: note body must not be empty")
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE notes SET body = ?, pinned = ?, visible = ? WHERE id = ?`,
		n.Body, n.Pinned, n.Visible, n.ID)
	if err != nil {
		return fmt.Errorf("store: update note %d: %w", n.ID, err)
	}
	return requireAffected(res, fmt.Sprintf("store: update note %d", n.ID))
}

// DeleteNote removes a note.
func (s *Store) DeleteNote(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM notes WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete note %d: %w", id, err)
	}
	return requireAffected(res, fmt.Sprintf("store: delete note %d", id))
}

// Media is one uploaded picture and its generated thumbnail.
type Media struct {
	ID            int64
	Filename      string
	ThumbFilename string
	Caption       string
	Width         int
	Height        int
	UploadedBy    *int64
	CreatedAt     time.Time
	SortOrder     int
}

const mediaColumns = `id, filename, thumb_filename, caption, width, height,
	uploaded_by, created_at, sort_order`

func scanMedia(sc interface{ Scan(...any) error }) (Media, error) {
	var (
		m         Media
		createdAt int64
	)
	if err := sc.Scan(&m.ID, &m.Filename, &m.ThumbFilename, &m.Caption, &m.Width, &m.Height,
		&m.UploadedBy, &createdAt, &m.SortOrder); err != nil {
		return Media{}, err
	}
	m.CreatedAt = time.Unix(createdAt, 0).UTC()
	return m, nil
}

// MediaList returns the gallery in display order.
func (s *Store) MediaList(ctx context.Context) ([]Media, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+mediaColumns+` FROM media ORDER BY sort_order ASC, created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list media: %w", err)
	}
	defer rows.Close()

	var out []Media
	for rows.Next() {
		m, err := scanMedia(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan media: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate media: %w", err)
	}
	return out, nil
}

// Media returns one picture by id.
func (s *Store) Media(ctx context.Context, id int64) (*Media, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+mediaColumns+` FROM media WHERE id = ?`, id)
	m, err := scanMedia(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: read media %d: %w", id, err)
	}
	return &m, nil
}

// CreateMedia records an uploaded picture and fills in its ID.
func (s *Store) CreateMedia(ctx context.Context, m *Media) error {
	if m.Filename == "" || m.ThumbFilename == "" {
		return errors.New("store: media filenames must not be empty")
	}
	m.CreatedAt = s.Now()

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO media (filename, thumb_filename, caption, width, height,
		 uploaded_by, created_at, sort_order) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.Filename, m.ThumbFilename, m.Caption, m.Width, m.Height,
		m.UploadedBy, m.CreatedAt.Unix(), m.SortOrder)
	if err != nil {
		return fmt.Errorf("store: create media: %w", err)
	}
	if m.ID, err = res.LastInsertId(); err != nil {
		return fmt.Errorf("store: create media: read id: %w", err)
	}
	return nil
}

// UpdateMediaMeta changes the caption and ordering of a picture.
func (s *Store) UpdateMediaMeta(ctx context.Context, id int64, caption string, sortOrder int) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE media SET caption = ?, sort_order = ? WHERE id = ?`, caption, sortOrder, id)
	if err != nil {
		return fmt.Errorf("store: update media %d: %w", id, err)
	}
	return requireAffected(res, fmt.Sprintf("store: update media %d", id))
}

// DeleteMedia removes the database row and returns it, so the caller can delete the
// files on disk afterwards. Doing it in this order means a crash leaves an orphan
// file rather than a gallery entry pointing at nothing.
func (s *Store) DeleteMedia(ctx context.Context, id int64) (*Media, error) {
	m, err := s.Media(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM media WHERE id = ?`, id); err != nil {
		return nil, fmt.Errorf("store: delete media %d: %w", id, err)
	}
	return m, nil
}

// Quote is one line of the optional rotating daily message.
type Quote struct {
	ID   int64
	Text string
	// Locale limits the quote to one language. Empty means every language.
	Locale    string
	Enabled   bool
	CreatedAt time.Time
}

// Quotes lists every quote, optionally filtered to those usable for a locale.
func (s *Store) Quotes(ctx context.Context, locale string, enabledOnly bool) ([]Quote, error) {
	query := `SELECT id, text, locale, enabled, created_at FROM quotes`
	var (
		conds []string
		args  []any
	)
	if enabledOnly {
		conds = append(conds, `enabled = 1`)
	}
	if locale != "" {
		conds = append(conds, `(locale = '' OR locale = ?)`)
		args = append(args, locale)
	}
	if len(conds) > 0 {
		// #nosec G202 -- every element of conds is a literal defined just above;
		// the locale itself travels as a bound parameter in args.
		query += ` WHERE ` + strings.Join(conds, ` AND `)
	}
	query += ` ORDER BY id ASC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list quotes: %w", err)
	}
	defer rows.Close()

	var out []Quote
	for rows.Next() {
		var (
			q         Quote
			createdAt int64
		)
		if err := rows.Scan(&q.ID, &q.Text, &q.Locale, &q.Enabled, &createdAt); err != nil {
			return nil, fmt.Errorf("store: scan quote: %w", err)
		}
		q.CreatedAt = time.Unix(createdAt, 0).UTC()
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate quotes: %w", err)
	}
	return out, nil
}

// CreateQuote adds a quote and fills in its ID.
func (s *Store) CreateQuote(ctx context.Context, q *Quote) error {
	if strings.TrimSpace(q.Text) == "" {
		return errors.New("store: quote text must not be empty")
	}
	q.CreatedAt = s.Now()

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO quotes (text, locale, enabled, created_at) VALUES (?, ?, ?, ?)`,
		q.Text, q.Locale, q.Enabled, q.CreatedAt.Unix())
	if err != nil {
		return fmt.Errorf("store: create quote: %w", err)
	}
	if q.ID, err = res.LastInsertId(); err != nil {
		return fmt.Errorf("store: create quote: read id: %w", err)
	}
	return nil
}

// DeleteQuote removes a quote.
func (s *Store) DeleteQuote(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM quotes WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete quote %d: %w", id, err)
	}
	return requireAffected(res, fmt.Sprintf("store: delete quote %d", id))
}

// Meta reads a bookkeeping value, returning ok=false when the key is absent.
func (s *Store) Meta(ctx context.Context, key string) (value string, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: read meta %q: %w", key, err)
	}
	return value, true, nil
}

// SetMeta writes a bookkeeping value.
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO meta (key, value) VALUES (?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("store: write meta %q: %w", key, err)
	}
	return nil
}
