package web

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/mrcat71/waitformeet/internal/auth"
	"github.com/mrcat71/waitformeet/internal/store"
)

// maxBackupBytes bounds an uploaded backup. Well past any realistic gallery, but
// far short of letting one request fill the volume.
const maxBackupBytes = 512 << 20

// restoredTables are copied out of the backup, in an order that reads sensibly.
// Foreign keys are deferred to commit, so the order does not have to satisfy them
// step by step.
//
// schema_migrations is deliberately absent: the running binary owns the schema, and
// a backup must never move it backwards. sessions is absent too, and is cleared
// instead, because resurrecting old sessions after swapping the user table would
// hand access to whoever held them.
var restoredTables = []string{
	"settings",
	"users",
	"invites",
	"media",
	"events",
	"notes",
	"quotes",
	"meta",
}

func (s *Server) handleAdminImport(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	ctx := r.Context()

	r.Body = http.MaxBytesReader(w, r.Body, maxBackupBytes)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		s.adminError(w, r, tabSite, fmt.Errorf("web: read the uploaded backup: %w", err))
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			if err := r.MultipartForm.RemoveAll(); err != nil {
				s.log.WarnContext(ctx, "cleaning up the upload", "error", err)
			}
		}
	}()

	file, _, err := r.FormFile("backup")
	if err != nil {
		s.adminError(w, r, tabSite, fmt.Errorf("web: no backup file was uploaded: %w", err))
		return
	}
	defer file.Close()

	staging, err := os.MkdirTemp("", "waitformeet-restore-")
	if err != nil {
		s.serverError(w, r, fmt.Errorf("web: create a staging directory: %w", err))
		return
	}
	defer func() {
		if err := os.RemoveAll(staging); err != nil {
			s.log.WarnContext(ctx, "cleaning up the staging directory", "error", err)
		}
	}()

	dbPath, mediaDir, err := s.unpackBackup(file, staging)
	if err != nil {
		s.adminError(w, r, tabSite, err)
		return
	}

	if err := s.restoreFrom(ctx, dbPath, mediaDir); err != nil {
		s.adminError(w, r, tabSite, err)
		return
	}

	s.log.InfoContext(ctx, "backup restored", "user_id", auth.UserFrom(ctx).ID)
	// Every session was cleared, this one included, so the browser lands on the
	// login form rather than an admin page it no longer has access to.
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// unpackBackup extracts the archive into staging and returns the paths to the
// database snapshot and the media tree.
func (s *Server) unpackBackup(file io.Reader, staging string) (dbPath, mediaDir string, err error) {
	archivePath := filepath.Join(staging, "backup.zip")
	if err := writeFile(archivePath, file); err != nil {
		return "", "", err
	}

	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", "", fmt.Errorf("web: that file is not a readable zip archive: %w", err)
	}
	defer reader.Close()

	extracted := filepath.Join(staging, "extracted")
	mediaDir = filepath.Join(extracted, "media")

	var total uint64
	for _, entry := range reader.File {
		// Reject anything trying to escape the staging directory. A crafted
		// archive with ../../ entries would otherwise write anywhere the process
		// can reach.
		clean := path.Clean(entry.Name)
		if strings.HasPrefix(clean, "..") || path.IsAbs(clean) || strings.Contains(clean, "../") {
			return "", "", fmt.Errorf("web: the archive contains an unsafe path %q", entry.Name)
		}
		if entry.FileInfo().IsDir() {
			continue
		}

		// A zip bomb declares a small archive that expands enormously, so the
		// running total is what has to be bounded, not the file on disk. The sum
		// stays in uint64 and is tested before it grows: narrowing a declared size
		// to int64 first would let a crafted header wrap the total negative and
		// walk straight past this limit.
		if entry.UncompressedSize64 > maxBackupBytes-total {
			return "", "", errors.New("web: the archive expands to more than this site accepts")
		}
		total += entry.UncompressedSize64

		target := filepath.Join(extracted, filepath.FromSlash(clean))
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return "", "", fmt.Errorf("web: prepare %s: %w", target, err)
		}

		src, err := entry.Open()
		if err != nil {
			return "", "", fmt.Errorf("web: read %s from the archive: %w", entry.Name, err)
		}
		err = writeFile(target, io.LimitReader(src, maxBackupBytes))
		src.Close()
		if err != nil {
			return "", "", err
		}
	}

	dbPath = filepath.Join(extracted, store.DBFileName)
	if _, err := os.Stat(dbPath); err != nil {
		return "", "", fmt.Errorf("web: the archive has no %s in it", store.DBFileName)
	}
	return dbPath, mediaDir, nil
}

// restoreFrom replaces the site's content with the backup's.
//
// The database is swapped row by row inside one transaction using ATTACH, rather
// than by replacing the file. That means no process restart, no window where the
// site serves half of each, and a failure part way through rolls back to exactly
// what was there before.
func (s *Server) restoreFrom(ctx context.Context, dbPath, mediaDir string) error {
	if err := s.checkBackupSchema(ctx, dbPath); err != nil {
		return err
	}

	db := s.store.DB()
	if _, err := db.ExecContext(ctx, `ATTACH DATABASE ? AS restore`, dbPath); err != nil {
		return fmt.Errorf("web: open the backup database: %w", err)
	}
	defer func() {
		if _, err := db.ExecContext(ctx, `DETACH DATABASE restore`); err != nil {
			s.log.WarnContext(ctx, "detaching the backup database", "error", err)
		}
	}()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("web: begin the restore: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	// Foreign keys cannot be switched off inside a transaction, but they can be
	// deferred to commit time, which is what a wholesale table swap needs.
	if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys = ON`); err != nil {
		return fmt.Errorf("web: defer foreign key checks: %w", err)
	}

	// Everyone is signed out: after replacing the user table, an old session is
	// either meaningless or a way in for the wrong person.
	if _, err := tx.ExecContext(ctx, `DELETE FROM main.sessions`); err != nil {
		return fmt.Errorf("web: clear sessions: %w", err)
	}

	for _, table := range restoredTables {
		// #nosec G202 -- restoredTables is a fixed list of identifiers declared in
		// this file. Table names cannot be bound as parameters.
		if _, err := tx.ExecContext(ctx, `DELETE FROM main.`+table); err != nil {
			return fmt.Errorf("web: clear %s: %w", table, err)
		}
		// #nosec G202 -- same fixed list of identifiers.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO main.`+table+` SELECT * FROM restore.`+table); err != nil {
			return fmt.Errorf("web: restore %s: %w", table, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("web: commit the restore: %w", err)
	}

	return s.restoreMedia(ctx, mediaDir)
}

// checkBackupSchema refuses a backup written by a different version of the schema.
//
// Copying rows between mismatched layouts would either fail loudly or, worse,
// silently shift values between columns.
func (s *Server) checkBackupSchema(ctx context.Context, dbPath string) error {
	backup, err := store.Open(ctx, filepath.Dir(dbPath))
	if err != nil {
		return fmt.Errorf("web: that backup is not a readable waitformeet database: %w", err)
	}
	defer backup.Close()

	var backupCount, liveCount int
	if err := backup.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM schema_migrations`).Scan(&backupCount); err != nil {
		return fmt.Errorf("web: read the backup's schema version: %w", err)
	}
	if err := s.store.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM schema_migrations`).Scan(&liveCount); err != nil {
		return fmt.Errorf("web: read this site's schema version: %w", err)
	}
	if backupCount > liveCount {
		return fmt.Errorf(
			"web: that backup was made by a newer version of waitformeet (schema %d against %d); upgrade first",
			backupCount, liveCount)
	}
	return nil
}

// restoreMedia swaps the pictures on the volume for the ones in the backup.
func (s *Server) restoreMedia(ctx context.Context, mediaDir string) error {
	live := filepath.Join(s.cfg.DataDir, "media")

	if _, err := os.Stat(mediaDir); errors.Is(err, os.ErrNotExist) {
		// A backup with no pictures in it is valid; nothing to swap.
		return nil
	}

	// The old tree is moved aside rather than deleted outright, so a failure part
	// way through leaves the previous pictures recoverable next to the new ones.
	retired := live + ".replaced"
	if err := os.RemoveAll(retired); err != nil {
		return fmt.Errorf("web: clear the previous media backup: %w", err)
	}
	if err := os.Rename(live, retired); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("web: move the current media aside: %w", err)
	}

	if err := os.Rename(mediaDir, live); err != nil {
		// Put things back the way they were before giving up.
		if rbErr := os.Rename(retired, live); rbErr != nil {
			return errors.Join(
				fmt.Errorf("web: install the restored media: %w", err),
				fmt.Errorf("web: and the previous media is now at %s: %w", retired, rbErr))
		}
		return fmt.Errorf("web: install the restored media: %w", err)
	}

	if err := os.RemoveAll(retired); err != nil {
		s.log.WarnContext(ctx, "could not remove the replaced media directory",
			"path", retired, "error", err)
	}
	return ensureMediaDirs(live)
}

func ensureMediaDirs(root string) error {
	for _, dir := range []string{"original", "thumb"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o750); err != nil {
			return fmt.Errorf("web: prepare %s: %w", dir, err)
		}
	}
	return nil
}

func writeFile(path string, src io.Reader) error {
	// #nosec G304 -- callers pass either a staging path they built themselves or
	// an archive entry already rejected above unless it stays inside staging.
	dst, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("web: create %s: %w", path, err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return fmt.Errorf("web: write %s: %w", path, err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("web: close %s: %w", path, err)
	}
	return nil
}
