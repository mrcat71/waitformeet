package render

import (
	"bytes"
	"image"
	"image/color"
	"strings"
	"testing"
	"time"
)

func TestCalendar(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("LoadLocation() error = %v", err)
	}

	out := string(Calendar("Until we meet", []CalendarEvent{
		{
			Summary: "Reunion",
			Start:   time.Date(2026, 12, 24, 10, 0, 0, 0, shanghai),
		},
		{
			Summary: "Visa",
			Start:   time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
			AllDay:  true,
		},
	}, now))

	for _, want := range []string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"BEGIN:VEVENT",
		"SUMMARY:Reunion",
		// 10:00 in Shanghai is 02:00 UTC, and everything is emitted in UTC.
		"DTSTART:20261224T020000Z",
		"DTSTART;VALUE=DATE:20260901",
		"DTEND;VALUE=DATE:20260902",
		"DTSTAMP:20260805T120000Z",
		"END:VCALENDAR",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("calendar is missing %q", want)
		}
	}

	// Every line must end CRLF, which strict clients require.
	for _, line := range strings.Split(strings.TrimSuffix(out, "\r\n"), "\r\n") {
		if strings.HasSuffix(line, "\n") || strings.HasSuffix(line, "\r") {
			t.Errorf("line %q has a stray line ending", line)
		}
	}
}

// A stable UID is what stops a re-import from creating a second copy of every
// entry rather than updating the ones already there.
func TestCalendarUIDIsStable(t *testing.T) {
	event := CalendarEvent{Summary: "Reunion", Start: time.Date(2026, 12, 24, 2, 0, 0, 0, time.UTC)}

	first := event.uid()
	second := event.uid()
	if first != second {
		t.Errorf("uid() = %q then %q, want the same both times", first, second)
	}

	other := CalendarEvent{Summary: "Something else", Start: event.Start}
	if other.uid() == first {
		t.Error("two different events produced the same UID")
	}
}

func TestEscapeText(t *testing.T) {
	tests := map[string]string{
		"plain":                   "plain",
		"semi;colon":              `semi\;colon`,
		"comma,separated":         `comma\,separated`,
		`back\slash`:              `back\\slash`,
		"line\nbreak":             `line\nbreak`,
		"carriage\r\nreturn":      `carriage\nreturn`,
		`all;of,them\and` + "\n:": `all\;of\,them\\and\n:`,
	}

	for in, want := range tests {
		t.Run(in, func(t *testing.T) {
			if got := escapeText(in); got != want {
				t.Errorf("escapeText(%q) = %q, want %q", in, got, want)
			}
		})
	}
}

// Folding counts octets, so a Cyrillic or Chinese summary must never be split in
// the middle of a character.
func TestFoldLineKeepsRunesIntact(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{name: "ascii", line: "SUMMARY:" + strings.Repeat("a", 200)},
		{name: "cyrillic", line: "SUMMARY:" + strings.Repeat("я", 100)},
		{name: "chinese", line: "SUMMARY:" + strings.Repeat("天", 100)},
		{name: "emoji", line: "SUMMARY:" + strings.Repeat("💍", 60)},
		{name: "short enough to be left alone", line: "SUMMARY:short"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			folded := foldLine(tt.line)

			for _, physical := range strings.Split(folded, "\r\n") {
				if len(physical) > ICSMaxLineOctets {
					t.Errorf("a physical line is %d octets, want at most %d",
						len(physical), ICSMaxLineOctets)
				}
			}

			// Unfolding, which is what a reader does, must give the original back.
			unfolded := strings.ReplaceAll(folded, "\r\n ", "")
			if unfolded != tt.line {
				t.Errorf("unfolding did not reproduce the input\n got %q\nwant %q", unfolded, tt.line)
			}
		})
	}
}

func TestContentDisposition(t *testing.T) {
	tests := map[string]string{
		"Until we meet": `attachment; filename="Until we meet.ics"`,
		`bad/name:here`: `attachment; filename="bad-name-here.ics"`,
		`"quoted"`:      `attachment; filename="-quoted-.ics"`,
	}

	for in, want := range tests {
		t.Run(in, func(t *testing.T) {
			if got := ContentDisposition(in); got != want {
				t.Errorf("ContentDisposition(%q) = %q, want %q", in, got, want)
			}
		})
	}
}

func TestOGRenders(t *testing.T) {
	tests := []struct {
		name string
		opts OGOptions
	}{
		{name: "a normal count", opts: OGOptions{Days: 140, Accent: "#e5687f"}},
		{name: "zero", opts: OGOptions{Days: 0, Accent: "#e5687f"}},
		{name: "a large count", opts: OGOptions{Days: 9999, Accent: "#123456"}},
		{name: "a negative count is clamped", opts: OGOptions{Days: -5, Accent: "#e5687f"}},
		{name: "no accent configured", opts: OGOptions{Days: 12}},
		{name: "a nonsense accent", opts: OGOptions{Days: 12, Accent: "hot pink"}},
		{name: "short hex accent", opts: OGOptions{Days: 12, Accent: "#abc"}},
		{
			// A font path that does not exist must still produce an image rather
			// than failing the whole preview.
			name: "a missing font",
			opts: OGOptions{Days: 12, Accent: "#e5687f", Title: "Until we meet", FontPath: "/no/such/font.ttf"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := OG(tt.opts)
			if err != nil {
				t.Fatalf("OG() error = %v", err)
			}

			cfg, format, err := image.DecodeConfig(bytes.NewReader(out))
			if err != nil {
				t.Fatalf("the output does not decode as an image: %v", err)
			}
			if format != "png" {
				t.Errorf("format = %q, want png", format)
			}
			if cfg.Width != OGWidth || cfg.Height != OGHeight {
				t.Errorf("size = %dx%d, want %dx%d", cfg.Width, cfg.Height, OGWidth, OGHeight)
			}
		})
	}
}

// The digits have to actually appear, not just the background.
func TestOGDrawsTheNumber(t *testing.T) {
	out, err := OG(OGOptions{Days: 8, Accent: "#000000"})
	if err != nil {
		t.Fatalf("OG() error = %v", err)
	}

	img, _, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Against a black background, any near-white pixel is ink from a digit.
	var white int
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if r > 0xf000 && g > 0xf000 && b > 0xf000 {
				white++
			}
		}
	}
	if white == 0 {
		t.Error("no digit pixels were drawn")
	}
}

func TestParseHexColour(t *testing.T) {
	fallback := color.RGBA{R: 1, G: 2, B: 3, A: 255}

	tests := []struct {
		in   string
		want color.RGBA
	}{
		{"#ffffff", color.RGBA{R: 255, G: 255, B: 255, A: 255}},
		{"#000000", color.RGBA{A: 255}},
		{"#e5687f", color.RGBA{R: 0xe5, G: 0x68, B: 0x7f, A: 255}},
		{"#abc", color.RGBA{R: 0xaa, G: 0xbb, B: 0xcc, A: 255}},
		{"", fallback},
		{"nonsense", fallback},
		{"#gggggg", fallback},
		{"e5687f", fallback},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := parseHexColour(tt.in, fallback); got != tt.want {
				t.Errorf("parseHexColour(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
