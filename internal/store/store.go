// Package store owns persistence: the SQLite database on the PersistentVolume,
// its schema migrations, and typed access to every table.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure Go driver, keeps CGO_ENABLED=0
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// ErrNotFound is returned when a lookup by id or key matches no row.
var ErrNotFound = errors.New("store: not found")

// DBFileName is the database file created inside the configured data directory.
const DBFileName = "waitformeet.db"

// Store is the handle on the database. It is safe for concurrent use.
type Store struct {
	db *sql.DB
	// now is swappable so tests can pin time without sleeping.
	now func() time.Time
}

// Open opens (creating it if needed) the database inside dataDir and applies all
// pending migrations.
func Open(ctx context.Context, dataDir string) (*Store, error) {
	path := filepath.Join(dataDir, DBFileName)

	// WAL keeps readers from blocking the writer. busy_timeout covers the brief
	// contention window during checkpoints. foreign_keys must be requested per
	// connection; SQLite leaves it off by default.
	dsn := "file:" + url.PathEscape(path) + "?" + strings.Join([]string{
		"_pragma=journal_mode(WAL)",
		"_pragma=busy_timeout(5000)",
		"_pragma=foreign_keys(1)",
		"_pragma=synchronous(NORMAL)",
	}, "&")

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}

	// One connection. This site serves a couple of people, so throughput is
	// irrelevant, and serialising access removes every SQLITE_BUSY race at the
	// cost of nothing we care about.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: connect to %s: %w", path, err)
	}

	s := &Store{db: db, now: time.Now}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error {
	return s.db.Close()
}

// DB exposes the underlying handle for the export/backup path, which streams the
// database file rather than going through typed accessors.
func (s *Store) DB() *sql.DB { return s.db }

// Now returns the store's clock. Handlers use it so tests can pin time in one place.
func (s *Store) Now() time.Time { return s.now().UTC() }

// SetClock replaces the store's clock. Intended for tests.
func (s *Store) SetClock(now func() time.Time) { s.now = now }

// migrate applies every embedded migration that has not run yet, in filename order.
// Each migration runs inside its own transaction, so a failure leaves the schema at
// the last complete version instead of halfway through.
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name       TEXT PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}

	applied, err := s.appliedMigrations(ctx)
	if err != nil {
		return err
	}

	names, err := migrationNames()
	if err != nil {
		return err
	}

	for _, name := range names {
		if applied[name] {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("store: read migration %s: %w", name, err)
		}
		if err := s.applyMigration(ctx, name, string(body)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) appliedMigrations(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("store: read schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("store: scan schema_migrations: %w", err)
		}
		applied[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate schema_migrations: %w", err)
	}
	return applied, nil
}

func (s *Store) applyMigration(ctx context.Context, name, body string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin migration %s: %w", name, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once the transaction is committed

	if _, err := tx.ExecContext(ctx, body); err != nil {
		return fmt.Errorf("store: apply migration %s: %w", name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`,
		name, s.now().UTC().Unix(),
	); err != nil {
		return fmt.Errorf("store: record migration %s: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit migration %s: %w", name, err)
	}
	return nil
}

func migrationNames() ([]string, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("store: list migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// tx runs fn inside a transaction, rolling back on any error.
func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin transaction: %w", err)
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return errors.Join(err, fmt.Errorf("store: rollback: %w", rbErr))
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

// unixPtr converts an optional instant to the nullable INTEGER the schema uses.
func unixPtr(t *time.Time) *int64 {
	if t == nil || t.IsZero() {
		return nil
	}
	v := t.UTC().Unix()
	return &v
}

// timePtr converts a nullable INTEGER column back to an optional instant.
func timePtr(v *int64) *time.Time {
	if v == nil {
		return nil
	}
	t := time.Unix(*v, 0).UTC()
	return &t
}
