package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mrcat71/waitformeet/internal/auth"
	"github.com/mrcat71/waitformeet/internal/render"
	"github.com/mrcat71/waitformeet/internal/store"
)

// ogCacheSeconds is how long a link preview may be cached. Long enough that a
// messenger does not re-render it on every paste, short enough that the day count
// it shows is never badly stale.
const ogCacheSeconds = 3600

// handleOGImage renders the preview picture messengers show when the link is pasted.
//
// It is always public, even when the countdown itself is not. It carries only a
// number, and refusing it would leave a broken image where a preview should be.
func (s *Server) handleOGImage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	settings, err := s.store.Settings(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	now := time.Now().UTC()
	opts := render.OGOptions{
		Accent:   settings.AccentColor,
		Title:    settings.SiteTitle,
		FontPath: s.cfg.OGFontPath,
	}

	event, err := s.headlineEvent(ctx, settings, now)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if event != nil {
		opts.Days = int(event.TargetAt.Sub(now).Hours() / dayHours)
		opts.Caption = event.Title
	}

	png, err := render.OG(opts)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(ogCacheSeconds))
	if _, err := w.Write(png); err != nil {
		s.log.WarnContext(ctx, "writing the preview image", "error", err)
	}
}

// handleCalendar exports the dates as an .ics file for a phone's calendar.
func (s *Server) handleCalendar(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	settings, err := s.store.Settings(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// The calendar carries the same dates the page shows, so it follows the same
	// rule: a private countdown must not be downloadable by a stranger.
	if !auth.CanSee(ctx, settings.Visibility.Countdown) {
		s.forbidden(w, r)
		return
	}

	name := settings.SiteTitle
	if name == "" {
		name = "waitformeet"
	}

	var events []render.CalendarEvent

	main, err := s.store.MainEvent(ctx)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		s.serverError(w, r, err)
		return
	default:
		events = append(events, render.CalendarEvent{
			UID:         fmt.Sprintf("event-%d@waitformeet", main.ID),
			Summary:     main.Title,
			Description: main.Description,
			Start:       main.TargetAt,
		})
	}

	if auth.CanSee(ctx, settings.Visibility.Milestones) {
		milestones, err := s.store.Milestones(ctx, false)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		for _, m := range milestones {
			events = append(events, render.CalendarEvent{
				UID:         fmt.Sprintf("event-%d@waitformeet", m.ID),
				Summary:     m.Title,
				Description: m.Description,
				Start:       m.TargetAt,
				// A milestone set to exactly midnight was meant as a day, not a
				// moment, so it becomes an all-day entry.
				AllDay: m.TargetAt.Hour() == 0 && m.TargetAt.Minute() == 0,
			})
		}
	}

	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", render.ContentDisposition(name))
	if _, err := w.Write(render.Calendar(name, events, time.Now().UTC())); err != nil {
		s.log.WarnContext(ctx, "writing the calendar", "error", err)
	}
}

// handleManifest serves the web app manifest, which is what makes the site
// installable on a phone's home screen.
func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.Settings(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	name := settings.SiteTitle
	if name == "" {
		name = s.bundle.Printer(s.bundle.Resolve(r, settings.DefaultLocale)).T("app.default_title")
	}
	accent := settings.AccentColor
	if !isHexColour(accent) {
		accent = "#e5687f"
	}

	manifest := map[string]any{
		"name":             name,
		"short_name":       name,
		"start_url":        "/",
		"scope":            "/",
		"display":          "standalone",
		"background_color": "#fdf8f6",
		"theme_color":      accent,
		"icons": []map[string]any{
			{"src": "/static/icon.svg", "sizes": "any", "type": "image/svg+xml", "purpose": "any"},
		},
	}

	w.Header().Set("Content-Type", "application/manifest+json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	if err := json.NewEncoder(w).Encode(manifest); err != nil {
		s.log.WarnContext(r.Context(), "writing the manifest", "error", err)
	}
}

// handleServiceWorker serves the bundled worker from the site root.
//
// A service worker may only control paths at or below its own URL, so serving it
// from /static/ would limit it to the assets. It has to live at the root.
func (s *Server) handleServiceWorker(w http.ResponseWriter, r *http.Request) {
	body, err := staticFS.ReadFile("static/dist/sw.js")
	if err != nil {
		s.serverError(w, r, fmt.Errorf("web: read the service worker: %w", err))
		return
	}

	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	// Short, so a deploy is picked up quickly rather than being pinned by a cache.
	w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
	w.Header().Set("Service-Worker-Allowed", "/")
	if _, err := w.Write(body); err != nil {
		s.log.WarnContext(r.Context(), "writing the service worker", "error", err)
	}
}

// handleRobots tells crawlers what to do.
//
// A site whose countdown is public is worth indexing; anything more private is not,
// and the personal sections are always disallowed regardless.
func (s *Server) handleRobots(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.Settings(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	var b strings.Builder
	b.WriteString("User-agent: *\n")

	if settings.Visibility.Countdown == store.VisPublic {
		for _, path := range []string{"/admin", "/login", "/invite", "/media", "/notes", "/gallery"} {
			b.WriteString("Disallow: " + path + "\n")
		}
		b.WriteString("Allow: /\n")
	} else {
		b.WriteString("Disallow: /\n")
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if _, err := io.WriteString(w, b.String()); err != nil {
		s.log.WarnContext(r.Context(), "writing robots.txt", "error", err)
	}
}

// handleMetrics exposes a few numbers in the Prometheus text format.
//
// Hand-written rather than pulled from a client library: the interesting values
// here are four gauges, and Go runtime metrics for a site with two users would be
// noise wrapped around a dependency.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !s.cfg.MetricsEnabled {
		s.handleNotFound(w, r)
		return
	}

	var b strings.Builder
	gauge := func(name, help string, value float64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s %g\n", name, help, name, name, value)
	}

	users, err := s.store.Users(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var admins, disabled int
	for _, u := range users {
		if u.IsAdmin {
			admins++
		}
		if u.Disabled {
			disabled++
		}
	}
	gauge("waitformeet_users", "Accounts that exist.", float64(len(users)))
	gauge("waitformeet_admins", "Accounts with admin rights.", float64(admins))
	gauge("waitformeet_users_disabled", "Accounts that are disabled.", float64(disabled))

	notes, err := s.store.Notes(ctx, true)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	gauge("waitformeet_notes", "Notes on the wall.", float64(len(notes)))

	pictures, err := s.store.MediaList(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	gauge("waitformeet_photos", "Pictures in the gallery.", float64(len(pictures)))

	settings, err := s.store.Settings(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	now := time.Now().UTC()
	if event, err := s.headlineEvent(ctx, settings, now); err == nil && event != nil {
		gauge("waitformeet_seconds_remaining",
			"Seconds until the headline countdown reaches zero.",
			max(0, event.TargetAt.Sub(now).Seconds()))
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if _, err := io.WriteString(w, b.String()); err != nil {
		s.log.WarnContext(ctx, "writing metrics", "error", err)
	}
}
