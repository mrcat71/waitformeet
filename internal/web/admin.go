package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mrcat71/waitformeet/internal/auth"
	"github.com/mrcat71/waitformeet/internal/config"
	"github.com/mrcat71/waitformeet/internal/i18n"
	"github.com/mrcat71/waitformeet/internal/store"
	"github.com/mrcat71/waitformeet/internal/users"
)

// wallClockLayouts are what a browser's date and datetime-local inputs submit.
// They carry no offset, so they are read in the timezone chosen on the form.
var wallClockLayouts = []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02"}

// adminTab identifies the active section for the navigation.
type adminTab string

const (
	tabContent adminTab = "content"
	tabEvents  adminTab = "events"
	tabQuotes  adminTab = "quotes"
	tabUsers   adminTab = "users"
	tabSite    adminTab = "site"
)

// adminPage is shared by every admin screen.
type adminPage struct {
	*page
	Tab adminTab
	// SeedLocked reports that the deployment re-applies its content seed on every
	// start, which makes editing these fields here pointless: the next restart
	// would undo it. The form is shown read-only rather than silently lying.
	SeedLocked bool
}

func (s *Server) newAdminPage(w http.ResponseWriter, r *http.Request, tab adminTab) (*adminPage, *store.Settings, error) {
	settings, err := s.store.Settings(r.Context())
	if err != nil {
		return nil, nil, err
	}
	base, err := s.newPage(w, r, settings)
	if err != nil {
		return nil, nil, err
	}
	base.NoIndex = true
	base.ShowNotes = auth.CanSee(r.Context(), settings.Visibility.Notes)
	base.ShowGallery = auth.CanSee(r.Context(), settings.Visibility.Gallery)
	base.Title = base.T("admin.heading")

	if r.URL.Query().Get("saved") != "" {
		base.Notice = base.T("admin.saved")
	}
	if code := r.URL.Query().Get("error"); code != "" {
		base.Error = base.T(adminErrorKey(code))
	}

	return &adminPage{
		page:       base,
		Tab:        tab,
		SeedLocked: s.cfg.SeedMode == config.SeedAlways && s.cfg.SeedFile != "",
	}, settings, nil
}

// requireSignedIn gates the pages any editor may use.
func (s *Server) requireSignedIn(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !auth.IsSignedIn(r.Context()) {
			// Send them to the login form and back afterwards, so a bookmarked
			// admin URL does not just dead-end.
			target := "/login?return_to=" + url.QueryEscape(r.URL.RequestURI())
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// requireAdmin gates user management and visibility, the two powers an ordinary
// editor does not get.
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return s.requireSignedIn(func(w http.ResponseWriter, r *http.Request) {
		if !auth.IsAdmin(r.Context()) {
			s.forbidden(w, r)
			return
		}
		next(w, r)
	})
}

// contentData backs the main settings screen.
type contentData struct {
	*adminPage
	Timezones []string
}

func (s *Server) handleAdminContent(w http.ResponseWriter, r *http.Request) {
	admin, _, err := s.newAdminPage(w, r, tabContent)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "admin_content", &contentData{
		adminPage: admin,
		Timezones: commonTimezones,
	})
}

func (s *Server) handleAdminContentSave(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	ctx := r.Context()

	settings, err := s.store.Settings(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if s.cfg.SeedMode == config.SeedAlways && s.cfg.SeedFile != "" {
		// Accepting the edit would be a lie: the next restart re-applies the seed.
		s.forbidden(w, r)
		return
	}

	settings.SiteTitle = strings.TrimSpace(r.PostFormValue("site_title"))
	settings.Tagline = strings.TrimSpace(r.PostFormValue("tagline"))
	settings.AccentColor = strings.TrimSpace(r.PostFormValue("accent_color"))
	settings.PartnerA = partnerFromForm(r, "partner_a")
	settings.PartnerB = partnerFromForm(r, "partner_b")
	settings.QuotesEnabled = r.PostFormValue("quotes_enabled") != ""
	settings.WeatherEnabled = r.PostFormValue("weather_enabled") != ""

	if locale := r.PostFormValue("default_locale"); s.bundle.Supported(locale) {
		settings.DefaultLocale = locale
	}

	switch raw := strings.TrimSpace(r.PostFormValue("separated_at")); raw {
	case "":
		settings.SeparatedAt = nil
	default:
		parsed, err := time.ParseInLocation("2006-01-02", raw, time.UTC)
		if err != nil {
			s.adminError(w, r, tabContent, err)
			return
		}
		settings.SeparatedAt = &parsed
	}

	if err := s.store.SaveSettings(ctx, settings); err != nil {
		s.adminError(w, r, tabContent, err)
		return
	}
	http.Redirect(w, r, "/admin?saved=1", http.StatusSeeOther)
}

func partnerFromForm(r *http.Request, prefix string) store.Partner {
	return store.Partner{
		Name:     strings.TrimSpace(r.PostFormValue(prefix + "_name")),
		City:     strings.TrimSpace(r.PostFormValue(prefix + "_city")),
		Timezone: strings.TrimSpace(r.PostFormValue(prefix + "_timezone")),
	}
}

// eventsData backs the dates screen.
type eventsData struct {
	*adminPage
	Main      *eventForm
	Timezones []string
}

// eventForm holds an event as the form shows it: a wall-clock time plus the zone it
// should be read in, rather than the UTC instant stored in the database.
type eventForm struct {
	ID          int64
	Title       string
	Emoji       string
	Local       string
	Timezone    string
	Description string
	Hidden      bool
}

func newEventForm(e store.Event, tz string) eventForm {
	loc := time.UTC
	if tz != "" {
		if parsed, err := time.LoadLocation(tz); err == nil {
			loc = parsed
		}
	}
	return eventForm{
		ID:          e.ID,
		Title:       e.Title,
		Emoji:       e.Emoji,
		Local:       e.TargetAt.In(loc).Format("2006-01-02T15:04"),
		Timezone:    tz,
		Description: e.Description,
		Hidden:      !e.Visible,
	}
}

func (s *Server) handleAdminEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	admin, settings, err := s.newAdminPage(w, r, tabEvents)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	// Default the form's timezone to the one the other person lives in: dates are
	// almost always agreed in terms of when they land there.
	defaultTZ := settings.PartnerB.Timezone
	if defaultTZ == "" {
		defaultTZ = settings.PartnerA.Timezone
	}
	if defaultTZ == "" {
		defaultTZ = "UTC"
	}

	data := &eventsData{adminPage: admin, Timezones: commonTimezones}

	main, err := s.store.MainEvent(ctx)
	switch {
	case errors.Is(err, store.ErrNotFound):
		data.Main = &eventForm{Timezone: defaultTZ}
	case err != nil:
		s.serverError(w, r, err)
		return
	default:
		form := newEventForm(*main, defaultTZ)
		data.Main = &form
	}

	s.render(w, r, http.StatusOK, "admin_events", data)
}

func (s *Server) handleAdminMainEvent(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}

	target, err := parseFormInstant(r.PostFormValue("at"), r.PostFormValue("timezone"))
	if err != nil {
		s.adminError(w, r, tabEvents, err)
		return
	}

	event := &store.Event{
		Kind:        store.KindMain,
		Title:       strings.TrimSpace(r.PostFormValue("title")),
		Emoji:       strings.TrimSpace(r.PostFormValue("emoji")),
		TargetAt:    target,
		Description: strings.TrimSpace(r.PostFormValue("description")),
		Visible:     r.PostFormValue("hidden") == "",
	}
	if err := s.store.SetMainEvent(r.Context(), event); err != nil {
		s.adminError(w, r, tabEvents, err)
		return
	}
	http.Redirect(w, r, "/admin/events?saved=1", http.StatusSeeOther)
}

// quotesData backs the daily-line screen.
type quotesData struct {
	*adminPage
	Quotes  []store.Quote
	Locales []*i18n.Locale
}

func (s *Server) handleAdminQuotes(w http.ResponseWriter, r *http.Request) {
	admin, _, err := s.newAdminPage(w, r, tabQuotes)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	quotes, err := s.store.Quotes(r.Context(), "", false)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "admin_quotes", &quotesData{
		adminPage: admin,
		Quotes:    quotes,
		Locales:   s.bundle.Locales(),
	})
}

func (s *Server) handleAdminQuoteCreate(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}

	locale := r.PostFormValue("locale")
	if locale != "" && !s.bundle.Supported(locale) {
		locale = ""
	}

	quote := &store.Quote{
		Text:    strings.TrimSpace(r.PostFormValue("text")),
		Locale:  locale,
		Enabled: true,
	}
	if err := s.store.CreateQuote(r.Context(), quote); err != nil {
		s.adminError(w, r, tabQuotes, err)
		return
	}
	http.Redirect(w, r, "/admin/quotes?saved=1", http.StatusSeeOther)
}

func (s *Server) handleAdminQuoteDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		s.handleNotFound(w, r)
		return
	}
	if err := s.store.DeleteQuote(r.Context(), id); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.adminError(w, r, tabQuotes, err)
		return
	}
	http.Redirect(w, r, "/admin/quotes?saved=1", http.StatusSeeOther)
}

// adminError re-renders the relevant admin screen with a message.
//
// Validation problems are the user's to fix, so they are shown rather than turned
// into a 500. Anything unexpected still goes through serverError.
func (s *Server) adminError(w http.ResponseWriter, r *http.Request, tab adminTab, err error) {
	s.log.InfoContext(r.Context(), "admin change rejected", "tab", string(tab), "error", err)

	target := map[adminTab]string{
		tabContent: "/admin",
		tabEvents:  "/admin/events",
		tabQuotes:  "/admin/quotes",
		tabUsers:   "/admin/users",
		tabSite:    "/admin/site",
	}[tab]

	http.Redirect(w, r, target+"?error="+url.QueryEscape(userFacing(err)), http.StatusSeeOther)
}

// userFacing maps an internal error onto a short code carried in the redirect, so
// that no SQL text or file path ever reaches the browser.
func userFacing(err error) string {
	switch {
	case errors.Is(err, store.ErrLastAdmin):
		return "last_admin"
	case errors.Is(err, store.ErrEmailTaken):
		return "email_taken"
	case errors.Is(err, users.ErrSelfDemotion):
		return "self"
	case errors.Is(err, users.ErrSelfDeletion):
		return "self_delete"
	default:
		return "invalid"
	}
}

// adminErrorKey turns that code back into a message key. An unknown code falls
// back to the generic one rather than rendering the raw code on the page.
func adminErrorKey(code string) string {
	switch code {
	case "last_admin", "email_taken", "self", "self_delete":
		return "admin.error." + code
	default:
		return "admin.error.invalid"
	}
}

// parseFormInstant reads a wall-clock value from a form together with the timezone
// the person chose, and returns the instant it denotes.
func parseFormInstant(value, timezone string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, errors.New("web: a date and time is required")
	}

	loc := time.UTC
	if timezone = strings.TrimSpace(timezone); timezone != "" {
		parsed, err := time.LoadLocation(timezone)
		if err != nil {
			return time.Time{}, fmt.Errorf("web: unknown timezone %q: %w", timezone, err)
		}
		loc = parsed
	}

	for _, layout := range wallClockLayouts {
		if t, err := time.ParseInLocation(layout, value, loc); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("web: cannot read %q as a date and time", value)
}

func pathID(r *http.Request, name string) (int64, error) {
	return strconv.ParseInt(r.PathValue(name), 10, 64)
}

// commonTimezones populates the timezone pickers. It is a short curated list rather
// than every IANA name, because a select with six hundred entries helps nobody; the
// field also accepts anything typed in, so unusual zones are still reachable.
var commonTimezones = []string{
	"UTC",
	"Europe/Belgrade",
	"Europe/Berlin",
	"Europe/Lisbon",
	"Europe/London",
	"Europe/Madrid",
	"Europe/Moscow",
	"Europe/Warsaw",
	"Asia/Almaty",
	"Asia/Bangkok",
	"Asia/Dubai",
	"Asia/Hong_Kong",
	"Asia/Jerusalem",
	"Asia/Kolkata",
	"Asia/Seoul",
	"Asia/Shanghai",
	"Asia/Singapore",
	"Asia/Tbilisi",
	"Asia/Tokyo",
	"Australia/Sydney",
	"America/Argentina/Buenos_Aires",
	"America/Bogota",
	"America/Chicago",
	"America/Los_Angeles",
	"America/Mexico_City",
	"America/New_York",
	"America/Sao_Paulo",
}
