package web

import (
	"errors"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/mrcat71/waitformeet/internal/auth"
	"github.com/mrcat71/waitformeet/internal/media"
	"github.com/mrcat71/waitformeet/internal/store"
)

// maxNoteLength keeps a note a note. Long enough for something real, short enough
// that the wall stays readable.
const maxNoteLength = 2000

// notesData backs the notes wall.
type notesData struct {
	*page
	Notes []noteView
	// OfTheDay is picked deterministically from the date, so both people see the
	// same one all day rather than a different one on every refresh.
	OfTheDay *noteView
	CanWrite bool
}

type noteView struct {
	store.Note
	DateLabel string
	ByLabel   string
}

// galleryData backs the photo gallery.
type galleryData struct {
	*page
	Media     []store.Media
	CanUpload bool
}

// sectionOf returns the visibility level guarding a section.
type sectionOf func(*store.Settings) store.Visibility

func notesSection(s *store.Settings) store.Visibility   { return s.Visibility.Notes }
func gallerySection(s *store.Settings) store.Visibility { return s.Visibility.Gallery }

// requireSection gates a handler behind a section's visibility setting.
//
// An anonymous visitor is sent to the login form, because for them the section may
// well become visible once they sign in. Somebody already signed in who still may
// not see it gets a refusal, since logging in again would change nothing.
func (s *Server) requireSection(section sectionOf, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		settings, err := s.store.Settings(r.Context())
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if auth.CanSee(r.Context(), section(settings)) {
			next(w, r)
			return
		}
		if auth.IsSignedIn(r.Context()) {
			s.forbidden(w, r)
			return
		}
		http.Redirect(w, r, "/login?return_to="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
	}
}

func (s *Server) handleNotes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	settings, err := s.store.Settings(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	base, err := s.newPage(w, r, settings)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	base.ShowNotes = true
	base.ShowGallery = auth.CanSee(ctx, settings.Visibility.Gallery)
	base.Title = base.T("notes.heading")
	// Only a fully public wall may be indexed; anything else stays out of search.
	base.NoIndex = settings.Visibility.Notes != store.VisPublic

	notes, err := s.store.Notes(ctx, auth.IsSignedIn(ctx))
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	data := &notesData{page: base, CanWrite: auth.IsSignedIn(ctx)}
	for _, n := range notes {
		data.Notes = append(data.Notes, s.noteView(base, n))
	}

	if pick := noteOfTheDay(notes, time.Now().UTC()); pick != nil {
		view := s.noteView(base, *pick)
		data.OfTheDay = &view
	}

	s.render(w, r, http.StatusOK, "notes", data)
}

func (s *Server) noteView(p *page, n store.Note) noteView {
	view := noteView{Note: n, DateLabel: formatDate(p.Locale, n.CreatedAt)}
	if n.AuthorName != "" {
		view.ByLabel = p.T("notes.by", "name", n.AuthorName)
	}
	return view
}

// noteOfTheDay picks one visible note using the date as the seed, so the choice is
// stable for everybody for the whole day. A pinned note always wins.
func noteOfTheDay(notes []store.Note, now time.Time) *store.Note {
	var candidates []store.Note
	for _, n := range notes {
		if n.Visible {
			candidates = append(candidates, n)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	for i := range candidates {
		if candidates[i].Pinned {
			return &candidates[i]
		}
	}

	day := now.Unix() / int64(dayHours*hourMins*minuteSec)
	return &candidates[int(day%int64(len(candidates)))]
}

func (s *Server) handleNoteCreate(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	ctx := r.Context()

	author := auth.UserFrom(ctx)
	body := strings.TrimSpace(r.PostFormValue("body"))
	if body == "" {
		http.Redirect(w, r, "/notes", http.StatusSeeOther)
		return
	}
	if len(body) > maxNoteLength {
		body = body[:maxNoteLength]
	}

	note := &store.Note{
		AuthorID:   &author.ID,
		AuthorName: author.Name(),
		Body:       body,
		Visible:    true,
	}
	if err := s.store.CreateNote(ctx, note); err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/notes", http.StatusSeeOther)
}

func (s *Server) handleNoteDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		s.handleNotFound(w, r)
		return
	}
	if err := s.store.DeleteNote(r.Context(), id); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/notes", http.StatusSeeOther)
}

func (s *Server) handleGallery(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	settings, err := s.store.Settings(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	base, err := s.newPage(w, r, settings)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	base.ShowNotes = auth.CanSee(ctx, settings.Visibility.Notes)
	base.ShowGallery = true
	base.Title = base.T("gallery.heading")
	base.NoIndex = settings.Visibility.Gallery != store.VisPublic

	pictures, err := s.store.MediaList(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	s.render(w, r, http.StatusOK, "gallery", &galleryData{
		page:      base,
		Media:     pictures,
		CanUpload: auth.IsSignedIn(ctx),
	})
}

func (s *Server) handleGalleryUpload(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	ctx := r.Context()

	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		s.log.InfoContext(ctx, "rejected an upload", "error", err)
		http.Redirect(w, r, "/gallery?error=upload", http.StatusSeeOther)
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			if err := r.MultipartForm.RemoveAll(); err != nil {
				s.log.WarnContext(ctx, "cleaning up the upload", "error", err)
			}
		}
	}()

	file, _, err := r.FormFile("picture")
	if err != nil {
		http.Redirect(w, r, "/gallery?error=upload", http.StatusSeeOther)
		return
	}
	defer file.Close()

	stored, err := s.media.Save(file)
	if errors.Is(err, media.ErrNotAnImage) || errors.Is(err, media.ErrTooLarge) {
		s.log.InfoContext(ctx, "rejected an upload", "error", err)
		http.Redirect(w, r, "/gallery?error=upload", http.StatusSeeOther)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	uploader := auth.UserFrom(ctx)
	record := &store.Media{
		Filename:      stored.Filename,
		ThumbFilename: stored.ThumbFilename,
		Caption:       strings.TrimSpace(r.PostFormValue("caption")),
		Width:         stored.Width,
		Height:        stored.Height,
		UploadedBy:    &uploader.ID,
	}
	if err := s.store.CreateMedia(ctx, record); err != nil {
		// The row is what makes the file reachable, so without it the file is
		// litter and gets cleaned up straight away.
		if rmErr := s.media.Remove(stored.Filename); rmErr != nil {
			s.log.WarnContext(ctx, "cleaning up an orphaned upload", "error", rmErr)
		}
		s.serverError(w, r, err)
		return
	}

	http.Redirect(w, r, "/gallery", http.StatusSeeOther)
}

func (s *Server) handleGalleryDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	ctx := r.Context()

	id, err := pathID(r)
	if err != nil {
		s.handleNotFound(w, r)
		return
	}

	// The row goes first: a crash afterwards leaves an unreferenced file, which is
	// harmless, whereas the other order would leave the gallery pointing at nothing.
	record, err := s.store.DeleteMedia(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		http.Redirect(w, r, "/gallery", http.StatusSeeOther)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.media.Remove(record.Filename); err != nil {
		s.log.WarnContext(ctx, "removing picture files", "error", err)
	}

	http.Redirect(w, r, "/gallery", http.StatusSeeOther)
}

// handleMedia serves an uploaded picture.
func (s *Server) handleMedia(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	// Only the exact filenames this server generates are acceptable. Anything with
	// a separator or a dot-dot in it is a traversal attempt, not a typo.
	if name != path.Base(name) || strings.Contains(name, "..") || !strings.HasSuffix(name, ".jpg") {
		s.handleNotFound(w, r)
		return
	}

	var file string
	switch r.PathValue("kind") {
	case "thumb":
		file = s.media.ThumbPath(name)
	case "original":
		file = s.media.OriginalPath(name)
	default:
		s.handleNotFound(w, r)
		return
	}

	// Filenames are random and content never changes under one, so a long cache is
	// safe. Private, because the gallery may well be login-gated and a shared proxy
	// must not hand the picture to the next person.
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeFile(w, r, file)
}
