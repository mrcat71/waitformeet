package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseSeedTime(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("LoadLocation() error = %v", err)
	}

	tests := []struct {
		name     string
		value    string
		timezone string
		want     time.Time
		wantErr  bool
	}{
		{
			name:  "rfc3339 with offset",
			value: "2026-12-24T10:00:00+08:00",
			want:  time.Date(2026, 12, 24, 2, 0, 0, 0, time.UTC),
		},
		{
			name:     "rfc3339 offset wins over the declared timezone",
			value:    "2026-12-24T10:00:00Z",
			timezone: "Asia/Shanghai",
			want:     time.Date(2026, 12, 24, 10, 0, 0, 0, time.UTC),
		},
		{
			name:     "wall clock in a named zone",
			value:    "2026-12-24T10:00",
			timezone: "Asia/Shanghai",
			want:     time.Date(2026, 12, 24, 10, 0, 0, 0, shanghai).UTC(),
		},
		{
			name:     "wall clock with seconds",
			value:    "2026-12-24T10:00:30",
			timezone: "Asia/Shanghai",
			want:     time.Date(2026, 12, 24, 10, 0, 30, 0, shanghai).UTC(),
		},
		{
			name:     "space separated",
			value:    "2026-12-24 10:00",
			timezone: "Asia/Shanghai",
			want:     time.Date(2026, 12, 24, 10, 0, 0, 0, shanghai).UTC(),
		},
		{
			name:     "date only means midnight local",
			value:    "2026-12-24",
			timezone: "Asia/Shanghai",
			want:     time.Date(2026, 12, 24, 0, 0, 0, 0, shanghai).UTC(),
		},
		{
			name:  "no timezone means UTC",
			value: "2026-12-24T10:00",
			want:  time.Date(2026, 12, 24, 10, 0, 0, 0, time.UTC),
		},
		{name: "empty", value: "", wantErr: true},
		{name: "nonsense", value: "next tuesday", wantErr: true},
		{name: "unknown zone", value: "2026-12-24", timezone: "Mars/Olympus", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSeedTime(tt.value, tt.timezone)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseSeedTime(%q, %q) error = nil, want an error", tt.value, tt.timezone)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSeedTime(%q, %q) error = %v", tt.value, tt.timezone, err)
			}
			if !got.Equal(tt.want) {
				t.Errorf("parseSeedTime(%q, %q) = %v, want %v", tt.value, tt.timezone, got, tt.want)
			}
		})
	}
}

// Daylight saving is exactly the case a naive fixed-offset implementation gets
// wrong, so pin one date on each side of a European transition.
func TestParseSeedTimeAcrossDST(t *testing.T) {
	tests := []struct {
		name       string
		value      string
		wantOffset int // seconds east of UTC that Europe/Belgrade uses on that date
	}{
		{name: "winter is CET", value: "2026-01-15T12:00", wantOffset: 1 * 3600},
		{name: "summer is CEST", value: "2026-07-15T12:00", wantOffset: 2 * 3600},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSeedTime(tt.value, "Europe/Belgrade")
			if err != nil {
				t.Fatalf("parseSeedTime() error = %v", err)
			}
			// 12:00 local minus the offset is the UTC hour.
			wantHour := 12 - tt.wantOffset/3600
			if got.Hour() != wantHour {
				t.Errorf("UTC hour = %d, want %d (offset %ds)", got.Hour(), wantHour, tt.wantOffset)
			}
		})
	}
}

func TestLoadSeedFile(t *testing.T) {
	dir := t.TempDir()

	t.Run("missing file is not an error", func(t *testing.T) {
		seed, err := LoadSeedFile(filepath.Join(dir, "absent.json"))
		if err != nil {
			t.Fatalf("LoadSeedFile() error = %v, want nil", err)
		}
		if seed != nil {
			t.Errorf("LoadSeedFile() = %+v, want nil", seed)
		}
	})

	t.Run("unknown key is rejected", func(t *testing.T) {
		path := filepath.Join(dir, "typo.json")
		if err := os.WriteFile(path, []byte(`{"siteTitel": "oops"}`), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		if _, err := LoadSeedFile(path); err == nil {
			t.Error("LoadSeedFile() error = nil, want a complaint about the unknown key")
		}
	})

	t.Run("valid document", func(t *testing.T) {
		path := filepath.Join(dir, "seed.json")
		if err := os.WriteFile(path, []byte(`{
			"siteTitle": "Until we meet",
			"timezone": "Asia/Shanghai",
			"partnerA": {"name": "A", "city": "Belgrade", "timezone": "Europe/Belgrade"},
			"main": {"title": "Reunion", "at": "2026-12-24T10:00"}
		}`), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}

		seed, err := LoadSeedFile(path)
		if err != nil {
			t.Fatalf("LoadSeedFile() error = %v", err)
		}
		if seed.SiteTitle != "Until we meet" {
			t.Errorf("SiteTitle = %q", seed.SiteTitle)
		}
		if seed.Main == nil || seed.Main.Title != "Reunion" {
			t.Errorf("Main = %+v", seed.Main)
		}
	})
}

func fullSeed() *Seed {
	autoAdvance := false
	return &Seed{
		SiteTitle:     "Until we meet",
		Tagline:       "soon",
		Timezone:      "Asia/Shanghai",
		PartnerA:      SeedPartner{Name: "A", City: "Belgrade", Timezone: "Europe/Belgrade"},
		PartnerB:      SeedPartner{Name: "B", City: "Shanghai", Timezone: "Asia/Shanghai"},
		AccentColor:   "#ff8800",
		DefaultLocale: "en",
		SeparatedAt:   "2026-03-01",
		Main:          &SeedEvent{Title: "Reunion", At: "2026-12-24T10:00"},
		Milestones: []SeedEvent{
			{Title: "Visa", At: "2026-09-01"},
			{Title: "Flights booked", At: "2026-10-01", Hidden: true},
		},
		Quotes:     []SeedQuote{{Text: "hello"}, {Text: "привет", Locale: "ru"}},
		Visibility: &SeedVisibility{Gallery: "public"},
		Features:   SeedFeatures{Quotes: true, AutoAdvance: &autoAdvance},
	}
}

func TestApplySeed(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.ApplySeed(ctx, fullSeed()); err != nil {
		t.Fatalf("ApplySeed() error = %v", err)
	}

	set, err := s.Settings(ctx)
	if err != nil {
		t.Fatalf("Settings() error = %v", err)
	}
	if set.SiteTitle != "Until we meet" {
		t.Errorf("SiteTitle = %q", set.SiteTitle)
	}
	if set.PartnerB.City != "Shanghai" {
		t.Errorf("PartnerB.City = %q", set.PartnerB.City)
	}
	if set.Visibility.Gallery != VisPublic {
		t.Errorf("Visibility.Gallery = %q, want the seed override", set.Visibility.Gallery)
	}
	if set.Visibility.Notes != VisLoggedIn {
		t.Errorf("Visibility.Notes = %q, want the default to survive a partial override", set.Visibility.Notes)
	}
	if !set.QuotesEnabled {
		t.Error("QuotesEnabled = false, want true")
	}
	if set.AutoAdvance {
		t.Error("AutoAdvance = true, want the explicit false from the seed")
	}
	if set.SeparatedAt == nil {
		t.Fatal("SeparatedAt = nil, want the seeded date")
	}

	main, err := s.MainEvent(ctx)
	if err != nil {
		t.Fatalf("MainEvent() error = %v", err)
	}
	// 10:00 in Shanghai is 02:00 UTC.
	if want := time.Date(2026, 12, 24, 2, 0, 0, 0, time.UTC); !main.TargetAt.Equal(want) {
		t.Errorf("main TargetAt = %v, want %v", main.TargetAt, want)
	}

	milestones, err := s.Milestones(ctx, true)
	if err != nil {
		t.Fatalf("Milestones() error = %v", err)
	}
	if len(milestones) != 2 {
		t.Fatalf("milestones = %v, want 2", titles(milestones))
	}
	if milestones[1].Visible {
		t.Error("the milestone marked hidden is visible")
	}

	quotes, err := s.Quotes(ctx, "", false)
	if err != nil {
		t.Fatalf("Quotes() error = %v", err)
	}
	if len(quotes) != 2 {
		t.Errorf("quotes = %d, want 2", len(quotes))
	}

	applied, err := s.SeedApplied(ctx)
	if err != nil {
		t.Fatalf("SeedApplied() error = %v", err)
	}
	if !applied {
		t.Error("SeedApplied() = false, want true after seeding")
	}
}

// Re-applying the seed is what SEED_MODE=always does on every restart. It must
// converge rather than pile up duplicates.
func TestApplySeedIsConvergent(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	for i := range 3 {
		if err := s.ApplySeed(ctx, fullSeed()); err != nil {
			t.Fatalf("ApplySeed() run %d error = %v", i+1, err)
		}
	}

	milestones, err := s.Milestones(ctx, true)
	if err != nil {
		t.Fatalf("Milestones() error = %v", err)
	}
	if len(milestones) != 2 {
		t.Errorf("milestones after three runs = %d, want 2", len(milestones))
	}

	quotes, err := s.Quotes(ctx, "", false)
	if err != nil {
		t.Fatalf("Quotes() error = %v", err)
	}
	if len(quotes) != 2 {
		t.Errorf("quotes after three runs = %d, want 2", len(quotes))
	}

	if _, err := s.MainEvent(ctx); err != nil {
		t.Errorf("MainEvent() after three runs: %v", err)
	}
}

// Notes and pictures belong to the people using the site. Re-seeding must never
// touch them, no matter what the chart says.
func TestApplySeedLeavesUserContentAlone(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	note := &Note{AuthorName: "A", Body: "miss you", Visible: true}
	if err := s.CreateNote(ctx, note); err != nil {
		t.Fatalf("CreateNote() error = %v", err)
	}
	pic := &Media{Filename: "a.jpg", ThumbFilename: "a-thumb.jpg"}
	if err := s.CreateMedia(ctx, pic); err != nil {
		t.Fatalf("CreateMedia() error = %v", err)
	}

	if err := s.ApplySeed(ctx, fullSeed()); err != nil {
		t.Fatalf("ApplySeed() error = %v", err)
	}

	notes, err := s.Notes(ctx, true)
	if err != nil {
		t.Fatalf("Notes() error = %v", err)
	}
	if len(notes) != 1 {
		t.Errorf("notes after seeding = %d, want 1", len(notes))
	}

	media, err := s.MediaList(ctx)
	if err != nil {
		t.Fatalf("MediaList() error = %v", err)
	}
	if len(media) != 1 {
		t.Errorf("media after seeding = %d, want 1", len(media))
	}
}

func TestApplySeedRejectsBadInput(t *testing.T) {
	tests := []struct {
		name    string
		seed    *Seed
		wantMsg string
	}{
		{
			name:    "unknown timezone",
			seed:    &Seed{PartnerA: SeedPartner{Timezone: "Mars/Olympus"}},
			wantMsg: "unknown timezone",
		},
		{
			name:    "unknown visibility",
			seed:    &Seed{Visibility: &SeedVisibility{Notes: "everyone"}},
			wantMsg: "unknown visibility",
		},
		{
			name:    "unparseable event time",
			seed:    &Seed{Main: &SeedEvent{Title: "Reunion", At: "whenever"}},
			wantMsg: "cannot parse",
		},
		{
			name:    "milestone without a title",
			seed:    &Seed{Milestones: []SeedEvent{{At: "2026-09-01"}}},
			wantMsg: "title must not be empty",
		},
		{
			name:    "bad separation date",
			seed:    &Seed{SeparatedAt: "ages ago"},
			wantMsg: "cannot parse",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := newTestStore(t)

			err := s.ApplySeed(ctx, tt.seed)
			if err == nil {
				t.Fatalf("ApplySeed() error = nil, want an error mentioning %q", tt.wantMsg)
			}
			if !contains(err.Error(), tt.wantMsg) {
				t.Errorf("error %q does not mention %q", err, tt.wantMsg)
			}

			// A rejected seed must not leave the marker behind, so a corrected
			// deployment gets another chance in "once" mode.
			applied, err := s.SeedApplied(ctx)
			if err != nil {
				t.Fatalf("SeedApplied() error = %v", err)
			}
			if applied {
				t.Error("SeedApplied() = true after a failed seed, want false")
			}
		})
	}
}

func TestApplySeedNilIsNoOp(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.ApplySeed(ctx, nil); err != nil {
		t.Fatalf("ApplySeed(nil) error = %v", err)
	}
	applied, err := s.SeedApplied(ctx)
	if err != nil {
		t.Fatalf("SeedApplied() error = %v", err)
	}
	if applied {
		t.Error("SeedApplied() = true after seeding nothing")
	}
}
