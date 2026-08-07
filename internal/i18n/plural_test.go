package i18n

import (
	"strconv"
	"testing"
)

// Russian is the reason this package hand-writes plural rules instead of just
// picking between "day" and "days", so it gets the exhaustive table.
func TestPluralRussian(t *testing.T) {
	tests := []struct {
		n    int
		want PluralCategory
	}{
		{0, Many},   // дней
		{1, One},    // день
		{2, Few},    // дня
		{3, Few},    // дня
		{4, Few},    // дня
		{5, Many},   // дней
		{9, Many},   // дней
		{10, Many},  // дней
		{11, Many},  // одиннадцать дней, not "день"
		{12, Many},  // двенадцать дней, not "дня"
		{13, Many},  // дней
		{14, Many},  // дней
		{15, Many},  // дней
		{20, Many},  // дней
		{21, One},   // двадцать один день
		{22, Few},   // двадцать два дня
		{25, Many},  // дней
		{100, Many}, // сто дней
		{101, One},  // сто один день
		{102, Few},  // сто два дня
		{111, Many}, // сто одиннадцать дней
		{112, Many}, // дней
		{121, One},  // сто двадцать один день
		{1000, Many},
		{1001, One},
		// Never shown by the site, but a rule that misbehaves on negatives would
		// be a trap for whoever reuses this next.
		{-1, One},
		{-3, Few},
		{-11, Many},
	}

	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.n), func(t *testing.T) {
			if got := pluralRussian(tt.n); got != tt.want {
				t.Errorf("pluralRussian(%d) = %q, want %q", tt.n, got, tt.want)
			}
		})
	}
}

func TestPluralByLocale(t *testing.T) {
	tests := []struct {
		locale string
		n      int
		want   PluralCategory
	}{
		{"en", 0, Other},
		{"en", 1, One},
		{"en", 2, Other},
		{"es", 1, One},
		{"es", 0, Other},
		{"es", 21, Other},
		{"ru", 1, One},
		{"ru", 3, Few},
		{"ru", 5, Many},
		{"zh-Hans", 0, Other},
		{"zh-Hans", 1, Other},
		{"zh-Hans", 5, Other},
		// Region and script subtags must not defeat the lookup.
		{"en-GB", 1, One},
		{"ru-RU", 3, Few},
		{"ZH-hans", 1, Other},
		// An unknown language falls back to the English rule rather than panicking.
		{"de", 1, One},
		{"", 2, Other},
	}

	for _, tt := range tests {
		t.Run(tt.locale+"/"+strconv.Itoa(tt.n), func(t *testing.T) {
			if got := Plural(tt.locale, tt.n); got != tt.want {
				t.Errorf("Plural(%q, %d) = %q, want %q", tt.locale, tt.n, got, tt.want)
			}
		})
	}
}

func TestBaseLanguage(t *testing.T) {
	tests := map[string]string{
		"en":      "en",
		"en-GB":   "en",
		"zh-Hans": "zh",
		"ZH-Hant": "zh",
		"ru-RU":   "ru",
		"":        "",
	}

	for in, want := range tests {
		t.Run(in, func(t *testing.T) {
			if got := BaseLanguage(in); got != want {
				t.Errorf("BaseLanguage(%q) = %q, want %q", in, got, want)
			}
		})
	}
}
