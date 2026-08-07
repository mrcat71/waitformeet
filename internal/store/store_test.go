package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixedNow pins the clock so tests never depend on wall time.
var fixedNow = time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

func newTestStore(t *testing.T) *Store {
	t.Helper()

	s, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	s.SetClock(func() time.Time { return fixedNow })
	return s
}

func TestOpenAppliesMigrationsIdempotently(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	if _, err := s.Settings(ctx); err != nil {
		t.Fatalf("Settings() after first open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	// Re-opening must not try to re-apply migrations.
	s2, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	defer s2.Close()

	var applied int
	if err := s2.DB().QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	names, err := migrationNames()
	if err != nil {
		t.Fatalf("migrationNames() error = %v", err)
	}
	if applied != len(names) {
		t.Errorf("applied %d migrations, want %d", applied, len(names))
	}

	if _, err := filepath.Glob(filepath.Join(dir, DBFileName)); err != nil {
		t.Errorf("database file missing from %s", dir)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	set, err := s.Settings(ctx)
	if err != nil {
		t.Fatalf("Settings() error = %v", err)
	}
	if got, want := set.Visibility.Countdown, VisPublic; got != want {
		t.Errorf("default countdown visibility = %q, want %q", got, want)
	}
	if got, want := set.Visibility.Gallery, VisLoggedIn; got != want {
		t.Errorf("default gallery visibility = %q, want %q", got, want)
	}

	separated := time.Date(2026, 3, 1, 8, 30, 0, 0, time.UTC)
	set.SiteTitle = "Until we meet"
	set.PartnerA = Partner{Name: "A", City: "Belgrade", Timezone: "Europe/Belgrade"}
	set.PartnerB = Partner{Name: "B", City: "Shanghai", Timezone: "Asia/Shanghai"}
	set.SeparatedAt = &separated
	set.Visibility.Gallery = VisPublic
	set.QuotesEnabled = true

	if err := s.SaveSettings(ctx, set); err != nil {
		t.Fatalf("SaveSettings() error = %v", err)
	}

	got, err := s.Settings(ctx)
	if err != nil {
		t.Fatalf("Settings() after save: %v", err)
	}
	if got.SiteTitle != "Until we meet" {
		t.Errorf("SiteTitle = %q", got.SiteTitle)
	}
	if got.PartnerB.Timezone != "Asia/Shanghai" {
		t.Errorf("PartnerB.Timezone = %q", got.PartnerB.Timezone)
	}
	if got.SeparatedAt == nil || !got.SeparatedAt.Equal(separated) {
		t.Errorf("SeparatedAt = %v, want %v", got.SeparatedAt, separated)
	}
	if got.Visibility.Gallery != VisPublic {
		t.Errorf("Visibility.Gallery = %q", got.Visibility.Gallery)
	}
	if !got.QuotesEnabled {
		t.Error("QuotesEnabled = false, want true")
	}
	if !got.UpdatedAt.Equal(fixedNow) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, fixedNow)
	}
}

func TestSaveSettingsRejectsBadValues(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Settings)
		wantMsg string
	}{
		{
			name:    "unknown visibility",
			mutate:  func(s *Settings) { s.Visibility.Notes = "everyone" },
			wantMsg: "notes visibility",
		},
		{
			name:    "unknown timezone",
			mutate:  func(s *Settings) { s.PartnerA.Timezone = "Mars/Olympus" },
			wantMsg: "unknown timezone",
		},
		{
			name:    "bad accent colour",
			mutate:  func(s *Settings) { s.AccentColor = "hot pink" },
			wantMsg: "hex colour",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := newTestStore(t)

			set, err := s.Settings(ctx)
			if err != nil {
				t.Fatalf("Settings() error = %v", err)
			}
			tt.mutate(set)

			err = s.SaveSettings(ctx, set)
			if err == nil {
				t.Fatalf("SaveSettings() error = nil, want an error mentioning %q", tt.wantMsg)
			}
			if !contains(err.Error(), tt.wantMsg) {
				t.Errorf("error %q does not mention %q", err, tt.wantMsg)
			}
		})
	}
}

func TestEventsSingleMainInvariant(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	target := fixedNow.Add(30 * 24 * time.Hour)
	first := &Event{Kind: KindMain, Title: "Reunion", TargetAt: target, Visible: true}
	if err := s.CreateEvent(ctx, first); err != nil {
		t.Fatalf("CreateEvent(main) error = %v", err)
	}

	second := &Event{Kind: KindMain, Title: "Another", TargetAt: target, Visible: true}
	err := s.CreateEvent(ctx, second)
	if !errors.Is(err, ErrMainEventExists) {
		t.Fatalf("CreateEvent(second main) error = %v, want ErrMainEventExists", err)
	}

	// SetMainEvent is the supported way to change it and must update in place.
	replacement := &Event{Title: "Reunion, moved", TargetAt: target.Add(48 * time.Hour), Visible: true}
	if err := s.SetMainEvent(ctx, replacement); err != nil {
		t.Fatalf("SetMainEvent() error = %v", err)
	}
	if replacement.ID != first.ID {
		t.Errorf("SetMainEvent created a new row (id %d), want it to update id %d", replacement.ID, first.ID)
	}

	got, err := s.MainEvent(ctx)
	if err != nil {
		t.Fatalf("MainEvent() error = %v", err)
	}
	if got.Title != "Reunion, moved" {
		t.Errorf("Title = %q, want the replacement", got.Title)
	}
}

func TestSetMainEventCreatesWhenAbsent(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.MainEvent(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("MainEvent() on empty store = %v, want ErrNotFound", err)
	}

	e := &Event{Title: "Reunion", TargetAt: fixedNow.Add(time.Hour), Visible: true}
	if err := s.SetMainEvent(ctx, e); err != nil {
		t.Fatalf("SetMainEvent() error = %v", err)
	}
	if e.ID == 0 {
		t.Error("SetMainEvent did not fill in the ID")
	}
	if e.Kind != KindMain {
		t.Errorf("Kind = %q, want %q", e.Kind, KindMain)
	}
}

func TestMilestonesOrderingAndVisibility(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	for _, e := range []Event{
		{Kind: KindMilestone, Title: "later", TargetAt: fixedNow.Add(72 * time.Hour), Visible: true},
		{Kind: KindMilestone, Title: "sooner", TargetAt: fixedNow.Add(24 * time.Hour), Visible: true},
		{Kind: KindMilestone, Title: "hidden", TargetAt: fixedNow.Add(48 * time.Hour), Visible: false},
	} {
		if err := s.CreateEvent(ctx, &e); err != nil {
			t.Fatalf("CreateEvent(%q) error = %v", e.Title, err)
		}
	}

	visible, err := s.Milestones(ctx, false)
	if err != nil {
		t.Fatalf("Milestones(false) error = %v", err)
	}
	if got := titles(visible); len(got) != 2 || got[0] != "sooner" || got[1] != "later" {
		t.Errorf("visible milestones = %v, want [sooner later]", got)
	}

	all, err := s.Milestones(ctx, true)
	if err != nil {
		t.Fatalf("Milestones(true) error = %v", err)
	}
	if len(all) != 3 {
		t.Errorf("all milestones = %v, want 3 entries", titles(all))
	}
}

func TestNextFutureEventSkipsPastAndHidden(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	for _, e := range []Event{
		{Kind: KindMilestone, Title: "past", TargetAt: fixedNow.Add(-time.Hour), Visible: true},
		{Kind: KindMilestone, Title: "hidden soon", TargetAt: fixedNow.Add(time.Hour), Visible: false},
		{Kind: KindMilestone, Title: "next", TargetAt: fixedNow.Add(2 * time.Hour), Visible: true},
	} {
		if err := s.CreateEvent(ctx, &e); err != nil {
			t.Fatalf("CreateEvent(%q) error = %v", e.Title, err)
		}
	}

	got, err := s.NextFutureEvent(ctx, fixedNow)
	if err != nil {
		t.Fatalf("NextFutureEvent() error = %v", err)
	}
	if got.Title != "next" {
		t.Errorf("NextFutureEvent() = %q, want %q", got.Title, "next")
	}
}

func TestCreateEventValidation(t *testing.T) {
	tests := []struct {
		name    string
		event   Event
		wantMsg string
	}{
		{"empty title", Event{Kind: KindMilestone, TargetAt: fixedNow}, "title must not be empty"},
		{"blank title", Event{Kind: KindMilestone, Title: "   ", TargetAt: fixedNow}, "title must not be empty"},
		{"missing target", Event{Kind: KindMilestone, Title: "x"}, "target time must be set"},
		{"bad kind", Event{Kind: "whatever", Title: "x", TargetAt: fixedNow}, "must be main or milestone"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			err := s.CreateEvent(context.Background(), &tt.event)
			if err == nil {
				t.Fatalf("CreateEvent() error = nil, want an error mentioning %q", tt.wantMsg)
			}
			if !contains(err.Error(), tt.wantMsg) {
				t.Errorf("error %q does not mention %q", err, tt.wantMsg)
			}
		})
	}
}

func titles(events []Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Title
	}
	return out
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
