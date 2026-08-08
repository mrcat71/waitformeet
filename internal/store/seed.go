package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// MetaSeedApplied marks that the initial content seed has run. It is what makes
// SeedMode "once" idempotent across restarts.
const MetaSeedApplied = "seed_applied_at"

// Seed is the initial content a deployment ships with, rendered by the Helm chart
// into a ConfigMap and read at startup. It is JSON so that no YAML dependency is
// needed: the chart emits it with toPrettyJson.
//
// Seeding never touches notes or uploaded pictures. Those are only ever created by
// people using the site, so a re-seed cannot destroy them.
type Seed struct {
	SiteTitle     string          `json:"siteTitle"`
	Tagline       string          `json:"tagline"`
	PartnerA      SeedPartner     `json:"partnerA"`
	PartnerB      SeedPartner     `json:"partnerB"`
	AccentColor   string          `json:"accentColor"`
	DefaultLocale string          `json:"defaultLocale"`
	SeparatedAt   string          `json:"separatedAt"`
	Main          *SeedEvent      `json:"main"`
	Quotes        []SeedQuote     `json:"quotes"`
	Visibility    *SeedVisibility `json:"visibility"`
	Features      SeedFeatures    `json:"features"`
	// Timezone is the default used to interpret event times that carry no offset.
	Timezone string `json:"timezone"`
}

// SeedPartner describes one half of the couple.
type SeedPartner struct {
	Name     string `json:"name"`
	City     string `json:"city"`
	Timezone string `json:"timezone"`
}

// SeedEvent is a dated entry. At accepts a full RFC 3339 timestamp, or a local
// wall-clock time such as "2026-12-24T10:00" or "2026-12-24" combined with Timezone,
// which is far easier to write correctly in values.yaml than an offset.
type SeedEvent struct {
	Title       string `json:"title"`
	Emoji       string `json:"emoji"`
	At          string `json:"at"`
	Timezone    string `json:"timezone"`
	Description string `json:"description"`
	Hidden      bool   `json:"hidden"`
}

// SeedQuote is one line of the optional daily message.
type SeedQuote struct {
	Text   string `json:"text"`
	Locale string `json:"locale"`
}

// SeedVisibility overrides the default per-section visibility.
type SeedVisibility struct {
	Countdown string `json:"countdown"`
	Clocks    string `json:"clocks"`
	Notes     string `json:"notes"`
	Gallery   string `json:"gallery"`
}

// SeedFeatures toggles the optional extras.
type SeedFeatures struct {
	Quotes  bool `json:"quotes"`
	Weather bool `json:"weather"`
}

// LoadSeedFile reads and parses a seed document. A missing file is not an error:
// deployments that configure everything through the UI have no seed at all.
func LoadSeedFile(path string) (*Seed, error) {
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: read seed file %s: %w", path, err)
	}

	var seed Seed
	dec := json.NewDecoder(bytes.NewReader(body))
	// Strict: a misspelled key in values.yaml that silently does nothing is a worse
	// failure than refusing to start with a clear message.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&seed); err != nil {
		return nil, fmt.Errorf("store: parse seed file %s: %w", path, err)
	}
	return &seed, nil
}

// SeedApplied reports whether the content seed has already run against this database.
func (s *Store) SeedApplied(ctx context.Context) (bool, error) {
	_, ok, err := s.Meta(ctx, MetaSeedApplied)
	return ok, err
}

// ApplySeed writes the seed into the database.
//
// The seed owns settings, the main event and quotes; it replaces them
// wholesale so that a GitOps deployment converges rather than accumulating
// duplicates on every restart. Notes and pictures are left untouched.
func (s *Store) ApplySeed(ctx context.Context, seed *Seed) error {
	if seed == nil {
		return nil
	}

	settings, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	if err := seed.applyTo(settings); err != nil {
		return err
	}

	mainEvent, err := seed.mainEvent()
	if err != nil {
		return err
	}

	now := s.Now()
	return s.tx(ctx, func(tx *sql.Tx) error {
		if err := s.saveSettingsTx(ctx, tx, settings, now); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM quotes`); err != nil {
			return fmt.Errorf("store: clear seeded quotes: %w", err)
		}

		if mainEvent != nil {
			if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE kind = ?`, KindMain); err != nil {
				return fmt.Errorf("store: clear seeded main event: %w", err)
			}
			if err := insertEventTx(ctx, tx, mainEvent, now); err != nil {
				return err
			}
		}

		for _, q := range seed.Quotes {
			if q.Text == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO quotes (text, locale, enabled, created_at) VALUES (?, ?, 1, ?)`,
				q.Text, q.Locale, now.Unix()); err != nil {
				return fmt.Errorf("store: insert seeded quote: %w", err)
			}
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO meta (key, value) VALUES (?, ?)
			 ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
			MetaSeedApplied, now.Format(time.RFC3339)); err != nil {
			return fmt.Errorf("store: record seed marker: %w", err)
		}
		return nil
	})
}

// applyTo overlays the seed onto existing settings. Empty seed fields leave the
// current value alone, so a partial seed is a valid partial override.
func (seed *Seed) applyTo(set *Settings) error {
	setIfNotEmpty(&set.SiteTitle, seed.SiteTitle)
	setIfNotEmpty(&set.Tagline, seed.Tagline)
	setIfNotEmpty(&set.PartnerA.Name, seed.PartnerA.Name)
	setIfNotEmpty(&set.PartnerA.City, seed.PartnerA.City)
	setIfNotEmpty(&set.PartnerA.Timezone, seed.PartnerA.Timezone)
	setIfNotEmpty(&set.PartnerB.Name, seed.PartnerB.Name)
	setIfNotEmpty(&set.PartnerB.City, seed.PartnerB.City)
	setIfNotEmpty(&set.PartnerB.Timezone, seed.PartnerB.Timezone)
	setIfNotEmpty(&set.AccentColor, seed.AccentColor)
	setIfNotEmpty(&set.DefaultLocale, seed.DefaultLocale)

	if seed.SeparatedAt != "" {
		t, err := parseSeedTime(seed.SeparatedAt, seed.Timezone)
		if err != nil {
			return fmt.Errorf("store: seed separatedAt: %w", err)
		}
		set.SeparatedAt = &t
	}

	if v := seed.Visibility; v != nil {
		for _, f := range []struct {
			raw  string
			dest *Visibility
			name string
		}{
			{v.Countdown, &set.Visibility.Countdown, "countdown"},
			{v.Clocks, &set.Visibility.Clocks, "clocks"},
			{v.Notes, &set.Visibility.Notes, "notes"},
			{v.Gallery, &set.Visibility.Gallery, "gallery"},
		} {
			if f.raw == "" {
				continue
			}
			parsed, err := ParseVisibility(f.raw)
			if err != nil {
				return fmt.Errorf("store: seed visibility %s: %w", f.name, err)
			}
			*f.dest = parsed
		}
	}

	set.QuotesEnabled = seed.Features.Quotes
	set.WeatherEnabled = seed.Features.Weather

	return set.validate()
}

// mainEvent converts the seed's headline date into a store event.
func (seed *Seed) mainEvent() (*Event, error) {
	if seed.Main == nil {
		return nil, nil
	}
	e, err := seed.Main.toEvent(KindMain, seed.Timezone)
	if err != nil {
		return nil, fmt.Errorf("store: seed main event: %w", err)
	}
	return &e, nil
}

func (se SeedEvent) toEvent(kind EventKind, defaultTZ string) (Event, error) {
	tz := se.Timezone
	if tz == "" {
		tz = defaultTZ
	}
	at, err := parseSeedTime(se.At, tz)
	if err != nil {
		return Event{}, err
	}
	e := Event{
		Kind:        kind,
		Title:       se.Title,
		Emoji:       se.Emoji,
		TargetAt:    at,
		Description: se.Description,
		Visible:     !se.Hidden,
	}
	if err := e.validate(); err != nil {
		return Event{}, err
	}
	return e, nil
}

// seedTimeLayouts are tried in order after RFC 3339. Accepting a bare wall-clock
// time plus an IANA zone means values.yaml can say "2026-12-24T10:00" with
// timezone "Asia/Shanghai" instead of hand-computing an offset.
var seedTimeLayouts = []string{
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
}

func parseSeedTime(value, timezone string) (time.Time, error) {
	if value == "" {
		return time.Time{}, errors.New("time must not be empty")
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t.UTC(), nil
	}

	loc := time.UTC
	if timezone != "" {
		var err error
		if loc, err = time.LoadLocation(timezone); err != nil {
			return time.Time{}, fmt.Errorf("unknown timezone %q: %w", timezone, err)
		}
	}
	for _, layout := range seedTimeLayouts {
		if t, err := time.ParseInLocation(layout, value, loc); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf(
		"cannot parse %q: use RFC 3339 such as 2026-12-24T10:00:00+08:00, "+
			"or a local time such as 2026-12-24T10:00 together with a timezone", value)
}

func insertEventTx(ctx context.Context, tx *sql.Tx, e *Event, now time.Time) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO events (kind, title, emoji, target_at, description, visible, sort_order, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Kind, e.Title, e.Emoji, e.TargetAt.UTC().Unix(),
		e.Description, e.Visible, e.SortOrder, now.Unix())
	if err != nil {
		return fmt.Errorf("store: insert seeded event %q: %w", e.Title, err)
	}
	return nil
}

func (s *Store) saveSettingsTx(ctx context.Context, tx *sql.Tx, set *Settings, now time.Time) error {
	set.UpdatedAt = now
	_, err := tx.ExecContext(ctx, `UPDATE settings SET
		site_title = ?, tagline = ?,
		partner_a_name = ?, partner_a_city = ?, partner_a_timezone = ?,
		partner_b_name = ?, partner_b_city = ?, partner_b_timezone = ?,
		accent_color = ?, default_locale = ?, background_media_id = ?, separated_at = ?,
		vis_countdown = ?, vis_clocks = ?, vis_notes = ?, vis_gallery = ?,
		quotes_enabled = ?, weather_enabled = ?, updated_at = ?
		WHERE id = 1`,
		set.SiteTitle, set.Tagline,
		set.PartnerA.Name, set.PartnerA.City, set.PartnerA.Timezone,
		set.PartnerB.Name, set.PartnerB.City, set.PartnerB.Timezone,
		set.AccentColor, set.DefaultLocale, set.BackgroundMediaID, unixPtr(set.SeparatedAt),
		set.Visibility.Countdown, set.Visibility.Clocks,
		set.Visibility.Notes, set.Visibility.Gallery,
		set.QuotesEnabled, set.WeatherEnabled, set.UpdatedAt.Unix())
	if err != nil {
		return fmt.Errorf("store: save seeded settings: %w", err)
	}
	return nil
}

func setIfNotEmpty(dest *string, value string) {
	if value != "" {
		*dest = value
	}
}
