package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/mrcat71/waitformeet/internal/auth"
	"github.com/mrcat71/waitformeet/internal/i18n"
	"github.com/mrcat71/waitformeet/internal/store"
)

const (
	dayHours  = 24
	hourMins  = 60
	minuteSec = 60
	// dayStartHour and dayEndHour decide whether a city gets the sun or the moon.
	dayStartHour = 7
	dayEndHour   = 20
)

// homeData is what the front page template renders.
type homeData struct {
	*page

	Countdown      *countdownView
	Progress       *progressView
	Clocks         []clockView
	Milestones     []milestoneView
	ShowMilestones bool
	Quote          string
}

type countdownView struct {
	Event       *store.Event
	Units       []unitView
	TargetLabel string
}

// unitView is one box of the countdown. Display holds the server-rendered value so
// the page is right before JavaScript runs; Forms lets the browser keep the label
// grammatical as the number changes.
type unitView struct {
	Key     string
	Display string
	Label   string
	Forms   map[string]string
}

type progressView struct {
	SeparatedAt  time.Time
	TargetAt     time.Time
	Fraction     float64
	Percent      int
	PercentLabel string
	ApartLabel   string
	// ApartForms keeps the {count} placeholder in every form, because languages
	// put the number in different places: "5 days apart" but "分开 5 天".
	ApartForms map[string]string
}

type clockView struct {
	Name     string
	City     string
	Timezone string
	Time     string
	Date     string
	IsDay    bool
	// Weather is empty unless the feature is on and the lookup succeeded.
	Weather     string
	WeatherIcon string
}

type milestoneView struct {
	store.Event
	Passed        bool
	DateLabel     string
	RelativeLabel string
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
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
	base.SWURL = "/sw.js"
	base.ShowNotes = auth.CanSee(ctx, settings.Visibility.Notes)
	base.ShowGallery = auth.CanSee(ctx, settings.Visibility.Gallery)

	data := &homeData{page: base}
	now := time.Now().UTC()

	event, err := s.headlineEvent(ctx, settings, now)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	if event != nil && auth.CanSee(ctx, settings.Visibility.Countdown) {
		data.Countdown = buildCountdown(base, event, now)

		if settings.SeparatedAt != nil {
			data.Progress = buildProgress(base, *settings.SeparatedAt, event.TargetAt, now)
		}
	}

	if auth.CanSee(ctx, settings.Visibility.Clocks) {
		data.Clocks = s.buildClocks(settings, now)
		if settings.WeatherEnabled {
			s.addWeather(ctx, data.Clocks)
		}
	}

	if auth.CanSee(ctx, settings.Visibility.Milestones) {
		data.ShowMilestones = true
		milestones, err := s.store.Milestones(ctx, false)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		data.Milestones = buildMilestones(base, milestones, now)
	}

	if settings.QuotesEnabled {
		quote, err := s.quoteOfTheDay(ctx, base.Locale, now)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		data.Quote = quote
	}

	s.render(w, r, http.StatusOK, "home", data)
}

// headlineEvent returns the event the front page counts towards.
//
// With auto-advance on, a main event whose date has passed hands over to the next
// future milestone, so the page keeps counting towards something instead of sitting
// at zero forever.
func (s *Server) headlineEvent(ctx context.Context, settings *store.Settings, now time.Time) (*store.Event, error) {
	main, err := s.store.MainEvent(ctx)
	if errors.Is(err, store.ErrNotFound) {
		main = nil
	} else if err != nil {
		return nil, err
	}

	if main != nil && !main.Passed(now) {
		return main, nil
	}
	if !settings.AutoAdvance {
		return main, nil
	}

	next, err := s.store.NextFutureEvent(ctx, now)
	if errors.Is(err, store.ErrNotFound) {
		return main, nil
	}
	if err != nil {
		return nil, err
	}
	return next, nil
}

func buildCountdown(p *page, event *store.Event, now time.Time) *countdownView {
	remaining := event.TargetAt.Sub(now)
	if remaining < 0 {
		remaining = 0
	}

	total := int(remaining.Seconds())
	days := total / (dayHours * hourMins * minuteSec)
	hours := total / (hourMins * minuteSec) % dayHours
	minutes := total / minuteSec % hourMins
	seconds := total % minuteSec

	units := []struct {
		key   string
		value int
		pad   bool
	}{
		{"days", days, false},
		{"hours", hours, true},
		{"minutes", minutes, true},
		{"seconds", seconds, true},
	}

	view := &countdownView{
		Event:       event,
		TargetLabel: p.T("countdown.target", "date", formatDate(p.Locale, event.TargetAt)),
	}
	for _, u := range units {
		display := fmt.Sprintf("%d", u.value)
		if u.pad {
			display = fmt.Sprintf("%02d", u.value)
		}
		key := "countdown.unit." + u.key
		view.Units = append(view.Units, unitView{
			Key:     u.key,
			Display: display,
			Label:   p.N(key, u.value),
			Forms:   p.Forms(key),
		})
	}
	return view
}

func buildProgress(p *page, start, end, now time.Time) *progressView {
	span := end.Sub(start)
	fraction := 1.0
	if span > 0 {
		fraction = now.Sub(start).Seconds() / span.Seconds()
	}
	fraction = min(1, max(0, fraction))

	apart := int(now.Sub(start).Hours() / dayHours)
	if apart < 0 {
		apart = 0
	}

	percent := int(fraction * 100)
	return &progressView{
		SeparatedAt:  start,
		TargetAt:     end,
		Fraction:     fraction,
		Percent:      percent,
		PercentLabel: p.T("progress.label", "percent", fmt.Sprintf("%d%%", percent)),
		ApartLabel:   p.N("apart.count", apart),
		ApartForms:   p.Forms("apart.count"),
	}
}

func (s *Server) buildClocks(settings *store.Settings, now time.Time) []clockView {
	var out []clockView

	for _, partner := range []store.Partner{settings.PartnerA, settings.PartnerB} {
		if partner.Name == "" && partner.City == "" {
			continue
		}
		loc, err := partner.Location()
		if err != nil {
			// A bad timezone is a settings problem, not a reason to drop the
			// clock: show UTC and say so in the log.
			s.log.Warn("clock falling back to UTC", "city", partner.City, "error", err)
		}
		local := now.In(loc)
		out = append(out, clockView{
			Name:     partner.Name,
			City:     partner.City,
			Timezone: partner.Timezone,
			Time:     local.Format("15:04"),
			Date:     local.Format("Mon 2 Jan"),
			IsDay:    local.Hour() >= dayStartHour && local.Hour() < dayEndHour,
		})
	}
	return out
}

func buildMilestones(p *page, events []store.Event, now time.Time) []milestoneView {
	out := make([]milestoneView, 0, len(events))
	for _, e := range events {
		days := int(e.TargetAt.Sub(now).Hours() / dayHours)

		var relative string
		switch {
		case e.Passed(now):
			relative = p.N("milestones.days_ago", -days)
		case days == 0:
			relative = p.T("milestones.today")
		default:
			relative = p.N("milestones.in_days", days)
		}

		out = append(out, milestoneView{
			Event:         e,
			Passed:        e.Passed(now),
			DateLabel:     formatDate(p.Locale, e.TargetAt),
			RelativeLabel: relative,
		})
	}
	return out
}

// addWeather fills in the current conditions for each city.
//
// Failures are silent on purpose: a weather service being slow or unreachable is
// no reason to show an error on a page about missing someone.
func (s *Server) addWeather(ctx context.Context, clocks []clockView) {
	for i := range clocks {
		conditions, ok := s.weather.For(ctx, clocks[i].City)
		if !ok {
			continue
		}
		clocks[i].Weather = fmt.Sprintf("%.0f°C", conditions.TemperatureC)
		clocks[i].WeatherIcon = conditions.Icon
	}
}

// quoteOfTheDay picks deterministically from the date, so both people see the same
// line all day rather than a different one on every refresh.
func (s *Server) quoteOfTheDay(ctx context.Context, locale string, now time.Time) (string, error) {
	quotes, err := s.store.Quotes(ctx, locale, true)
	if err != nil {
		return "", err
	}
	if len(quotes) == 0 {
		return "", nil
	}

	day := now.Unix() / int64(dayHours*hourMins*minuteSec)
	return quotes[int(day%int64(len(quotes)))].Text, nil
}

// formatDate renders a date in a way that reads naturally in each language without
// pulling in a formatting library.
func formatDate(locale string, t time.Time) string {
	switch i18n.BaseLanguage(locale) {
	case "zh":
		return t.Format("2006年1月2日")
	case "ru", "es":
		return t.Format("02.01.2006")
	default:
		return t.Format("2 Jan 2006")
	}
}
