package web

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mrcat71/waitformeet/internal/auth"
	"github.com/mrcat71/waitformeet/internal/store"
	"github.com/mrcat71/waitformeet/internal/users"
)

// usersData backs the people screen.
type usersData struct {
	*adminPage
	Users   []userRow
	Invites []inviteRow
	// InviteLink is shown exactly once, right after an invitation is created.
	// It is never stored in a form anyone can read back.
	InviteLink    string
	InviteExpires string
}

type userRow struct {
	store.User
	IsSelf      string
	LastLogin   string
	SSOLinked   bool
	HasPassword bool
}

type inviteRow struct {
	store.Invite
	ExpiresLabel string
}

func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	admin, _, err := s.newAdminPage(w, r, tabUsers)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	accounts, err := s.store.Users(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	invites, err := s.store.PendingInvites(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	me := auth.UserFrom(ctx)
	data := &usersData{adminPage: admin}

	for _, u := range accounts {
		row := userRow{
			User:        u,
			LastLogin:   admin.T("admin.users.never"),
			SSOLinked:   u.OIDCSubject != "",
			HasPassword: u.PasswordHash != "",
		}
		if u.LastLoginAt != nil {
			row.LastLogin = formatDate(admin.Locale, *u.LastLoginAt)
		}
		if me != nil && me.ID == u.ID {
			row.IsSelf = admin.T("admin.users.you")
		}
		data.Users = append(data.Users, row)
	}

	for _, inv := range invites {
		data.Invites = append(data.Invites, inviteRow{
			Invite:       inv,
			ExpiresLabel: admin.T("admin.users.expires", "date", formatDate(admin.Locale, inv.ExpiresAt)),
		})
	}

	// The freshly minted link arrives as a one-shot query parameter. It is not
	// stored anywhere, so a refresh loses it, which is the intended behaviour.
	if token := r.URL.Query().Get("invite_token"); token != "" {
		data.InviteLink = users.InviteURL(s.cfg.BaseURL, token)
		data.InviteExpires = formatDate(admin.Locale, time.Now().UTC().Add(users.InviteTTL))
	}

	s.render(w, r, http.StatusOK, "admin_users", data)
}

func (s *Server) handleAdminInvite(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	ctx := r.Context()

	token, _, err := s.users.Invite(ctx,
		auth.UserFrom(ctx),
		r.PostFormValue("email"),
		strings.TrimSpace(r.PostFormValue("display_name")),
		r.PostFormValue("is_admin") != "",
	)
	if err != nil {
		s.adminError(w, r, tabUsers, err)
		return
	}

	s.log.InfoContext(ctx, "invitation created", "email", store.NormalizeEmail(r.PostFormValue("email")))
	http.Redirect(w, r, "/admin/users?invite_token="+url.QueryEscape(token), http.StatusSeeOther)
}

func (s *Server) handleAdminUserUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	ctx := r.Context()

	id, err := pathID(r, "id")
	if err != nil {
		s.handleNotFound(w, r)
		return
	}
	target, err := s.store.UserByID(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		s.handleNotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	err = s.users.Update(ctx,
		auth.UserFrom(ctx),
		target,
		strings.TrimSpace(r.PostFormValue("display_name")),
		r.PostFormValue("is_admin") != "",
		r.PostFormValue("disabled") != "",
	)
	if err != nil {
		s.adminError(w, r, tabUsers, err)
		return
	}
	http.Redirect(w, r, "/admin/users?saved=1", http.StatusSeeOther)
}

func (s *Server) handleAdminUserDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	ctx := r.Context()

	id, err := pathID(r, "id")
	if err != nil {
		s.handleNotFound(w, r)
		return
	}
	if err := s.users.Delete(ctx, auth.UserFrom(ctx), id); err != nil {
		s.adminError(w, r, tabUsers, err)
		return
	}
	s.log.InfoContext(ctx, "account deleted", "user_id", id)
	http.Redirect(w, r, "/admin/users?saved=1", http.StatusSeeOther)
}

func (s *Server) handleAdminInviteDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		s.handleNotFound(w, r)
		return
	}
	if err := s.store.DeleteInvite(r.Context(), id); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.adminError(w, r, tabUsers, err)
		return
	}
	http.Redirect(w, r, "/admin/users?saved=1", http.StatusSeeOther)
}

// siteData backs the visibility and backup screen.
type siteData struct {
	*adminPage
	Sections []sectionRow
	Levels   []levelOption
}

type sectionRow struct {
	Key     string
	Label   string
	Current store.Visibility
}

type levelOption struct {
	Value store.Visibility
	Label string
}

func (s *Server) handleAdminSite(w http.ResponseWriter, r *http.Request) {
	admin, settings, err := s.newAdminPage(w, r, tabSite)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	data := &siteData{
		adminPage: admin,
		Levels: []levelOption{
			{store.VisPublic, admin.T("admin.visibility.public")},
			{store.VisLoggedIn, admin.T("admin.visibility.logged_in")},
			{store.VisAdmin, admin.T("admin.visibility.admin")},
		},
	}
	for _, section := range visibilitySections(settings) {
		data.Sections = append(data.Sections, sectionRow{
			Key:     section.key,
			Label:   admin.T("admin.section." + section.key),
			Current: *section.field,
		})
	}

	s.render(w, r, http.StatusOK, "admin_site", data)
}

// visibilitySections pairs each switch with the settings field behind it, so the
// read and write paths cannot drift apart.
func visibilitySections(settings *store.Settings) []struct {
	key   string
	field *store.Visibility
} {
	return []struct {
		key   string
		field *store.Visibility
	}{
		{"countdown", &settings.Visibility.Countdown},
		{"clocks", &settings.Visibility.Clocks},
		{"notes", &settings.Visibility.Notes},
		{"gallery", &settings.Visibility.Gallery},
	}
}

func (s *Server) handleAdminVisibilitySave(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	ctx := r.Context()

	settings, err := s.store.Settings(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	for _, section := range visibilitySections(settings) {
		raw := r.PostFormValue("visibility_" + section.key)
		level, err := store.ParseVisibility(raw)
		if err != nil {
			s.adminError(w, r, tabSite, err)
			return
		}
		*section.field = level
	}

	if err := s.store.SaveSettings(ctx, settings); err != nil {
		s.adminError(w, r, tabSite, err)
		return
	}
	s.log.InfoContext(ctx, "section visibility changed",
		"countdown", settings.Visibility.Countdown,
		"notes", settings.Visibility.Notes,
		"gallery", settings.Visibility.Gallery)

	http.Redirect(w, r, "/admin/site?saved=1", http.StatusSeeOther)
}

// handleAdminExport streams a zip of the database and every uploaded picture.
//
// This is the backup story for the volume: one file, downloadable from a phone,
// restorable through the form next to the button.
func (s *Server) handleAdminExport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// A plain file copy of a live SQLite database can catch a torn write. VACUUM
	// INTO writes a consistent snapshot instead, and works while the site is up.
	snapshot := filepath.Join(os.TempDir(), fmt.Sprintf("waitformeet-export-%d.db", time.Now().UnixNano()))
	if _, err := s.store.DB().ExecContext(ctx, `VACUUM INTO ?`, snapshot); err != nil {
		s.serverError(w, r, fmt.Errorf("web: snapshot the database: %w", err))
		return
	}
	defer func() {
		if err := os.Remove(snapshot); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.log.WarnContext(ctx, "could not remove the export snapshot", "error", err)
		}
	}()

	filename := fmt.Sprintf("waitformeet-%s.zip", time.Now().UTC().Format("2006-01-02"))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)

	zw := zip.NewWriter(w)
	defer zw.Close()

	if err := addFileToZip(zw, snapshot, store.DBFileName); err != nil {
		// The response is already streaming, so the only honest thing left is to
		// log it; the truncated zip will fail to open, which is the signal.
		s.log.ErrorContext(ctx, "writing the database into the export", "error", err)
		return
	}

	mediaRoot := filepath.Join(s.cfg.DataDir, "media")
	if err := addTreeToZip(zw, mediaRoot, "media"); err != nil {
		s.log.ErrorContext(ctx, "writing media into the export", "error", err)
		return
	}

	s.log.InfoContext(ctx, "backup downloaded", "user_id", auth.UserFrom(ctx).ID)
}

func addFileToZip(zw *zip.Writer, path, name string) error {
	src, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("web: open %s: %w", path, err)
	}
	defer src.Close()

	dst, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("web: create zip entry %s: %w", name, err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("web: copy %s into the archive: %w", name, err)
	}
	return nil
}

func addTreeToZip(zw *zip.Writer, root, prefix string) error {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("web: list %s: %w", root, err)
	}

	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		name := prefix + "/" + entry.Name()

		if entry.IsDir() {
			if err := addTreeToZip(zw, path, name); err != nil {
				return err
			}
			continue
		}
		if err := addFileToZip(zw, path, name); err != nil {
			return err
		}
	}
	return nil
}
