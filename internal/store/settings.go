package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Visibility says who may see a section of the site.
type Visibility string

const (
	// VisPublic shows the section to everyone, including logged-out visitors.
	VisPublic Visibility = "public"
	// VisLoggedIn shows the section only to signed-in users.
	VisLoggedIn Visibility = "logged-in"
	// VisAdmin shows the section only to admins.
	VisAdmin Visibility = "admin"
)

// Visibilities lists every valid value, in order of decreasing openness.
var Visibilities = []Visibility{VisPublic, VisLoggedIn, VisAdmin}

// Valid reports whether v is a known visibility level.
func (v Visibility) Valid() bool {
	for _, known := range Visibilities {
		if v == known {
			return true
		}
	}
	return false
}

// ParseVisibility validates a value coming from a form or a seed file.
func ParseVisibility(s string) (Visibility, error) {
	v := Visibility(strings.ToLower(strings.TrimSpace(s)))
	if !v.Valid() {
		return "", fmt.Errorf("store: unknown visibility %q, want one of public, logged-in, admin", s)
	}
	return v, nil
}

// Partner is one half of the couple the site is about.
type Partner struct {
	Name     string
	City     string
	Timezone string
}

// Location resolves the partner's IANA timezone. An unknown or empty zone falls back
// to UTC with an error, so the caller can surface a bad setting instead of silently
// showing the wrong clock.
func (p Partner) Location() (*time.Location, error) {
	if p.Timezone == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		return time.UTC, fmt.Errorf("store: unknown timezone %q: %w", p.Timezone, err)
	}
	return loc, nil
}

// SectionVisibility holds the per-section visibility switches that admins control.
type SectionVisibility struct {
	Countdown Visibility
	Clocks    Visibility
	Notes     Visibility
	Gallery   Visibility
}

// Settings is the single row of site-wide configuration owned by the admin UI.
type Settings struct {
	SiteTitle         string
	Tagline           string
	PartnerA          Partner
	PartnerB          Partner
	AccentColor       string
	DefaultLocale     string
	BackgroundMediaID *int64
	// SeparatedAt anchors the "apart for N days" counter and the progress bar.
	SeparatedAt    *time.Time
	Visibility     SectionVisibility
	QuotesEnabled  bool
	WeatherEnabled bool
	UpdatedAt      time.Time
}

const settingsColumns = `site_title, tagline,
	partner_a_name, partner_a_city, partner_a_timezone,
	partner_b_name, partner_b_city, partner_b_timezone,
	accent_color, default_locale, background_media_id, separated_at,
	vis_countdown, vis_clocks, vis_notes, vis_gallery,
	quotes_enabled, weather_enabled, updated_at`

// Settings reads the site configuration.
func (s *Store) Settings(ctx context.Context) (*Settings, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+settingsColumns+` FROM settings WHERE id = 1`)

	var (
		set         Settings
		separatedAt *int64
		updatedAt   int64
	)
	err := row.Scan(
		&set.SiteTitle, &set.Tagline,
		&set.PartnerA.Name, &set.PartnerA.City, &set.PartnerA.Timezone,
		&set.PartnerB.Name, &set.PartnerB.City, &set.PartnerB.Timezone,
		&set.AccentColor, &set.DefaultLocale, &set.BackgroundMediaID, &separatedAt,
		&set.Visibility.Countdown, &set.Visibility.Clocks,
		&set.Visibility.Notes, &set.Visibility.Gallery,
		&set.QuotesEnabled, &set.WeatherEnabled, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		// The initial migration inserts row 1, so its absence means the database was
		// tampered with rather than merely empty.
		return nil, fmt.Errorf("store: settings row is missing: %w", ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: read settings: %w", err)
	}

	set.SeparatedAt = timePtr(separatedAt)
	set.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return &set, nil
}

// SaveSettings replaces the site configuration and stamps UpdatedAt.
func (s *Store) SaveSettings(ctx context.Context, set *Settings) error {
	if err := set.validate(); err != nil {
		return err
	}
	set.UpdatedAt = s.Now()

	_, err := s.db.ExecContext(ctx, `UPDATE settings SET
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
		set.QuotesEnabled, set.WeatherEnabled, set.UpdatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("store: save settings: %w", err)
	}
	return nil
}

func (set *Settings) validate() error {
	var errs []error

	for name, v := range map[string]Visibility{
		"countdown": set.Visibility.Countdown,
		"clocks":    set.Visibility.Clocks,
		"notes":     set.Visibility.Notes,
		"gallery":   set.Visibility.Gallery,
	} {
		if !v.Valid() {
			errs = append(errs, fmt.Errorf("store: %s visibility %q is not one of public, logged-in, admin", name, v))
		}
	}

	// A bad timezone would silently render the wrong clock, which is the one thing
	// this site exists to get right.
	for name, p := range map[string]Partner{"partner A": set.PartnerA, "partner B": set.PartnerB} {
		if _, err := p.Location(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}

	if set.AccentColor != "" && !isHexColor(set.AccentColor) {
		errs = append(errs, fmt.Errorf("store: accent color %q is not a hex colour such as #e5687f", set.AccentColor))
	}

	return errors.Join(errs...)
}

// isHexColor accepts the #rgb and #rrggbb spellings a colour input produces.
func isHexColor(s string) bool {
	rest, ok := strings.CutPrefix(s, "#")
	if !ok || (len(rest) != 3 && len(rest) != 6) {
		return false
	}
	for _, r := range rest {
		isDigit := r >= '0' && r <= '9'
		isHex := (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
		if !isDigit && !isHex {
			return false
		}
	}
	return true
}
