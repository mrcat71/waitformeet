package i18n

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func testBundle(t *testing.T) *Bundle {
	t.Helper()

	b, err := Load(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return b
}

func TestLoadCatalogues(t *testing.T) {
	b := testBundle(t)

	for _, want := range []string{"en", "ru", "zh-Hans", "es"} {
		if !b.Supported(want) {
			t.Errorf("locale %q is not loaded, got %v", want, b.Tags())
		}
	}
	for _, loc := range b.Locales() {
		if loc.Name == "" {
			t.Errorf("locale %q has no @name for the language switcher", loc.Tag)
		}
	}
}

// Every key in the base catalogue must exist in every other language, or a visitor
// silently gets English in the middle of their own language.
func TestEveryLocaleCoversTheBaseCatalogue(t *testing.T) {
	b := testBundle(t)

	base := b.locales[DefaultLocale]
	for tag, loc := range b.locales {
		if tag == DefaultLocale {
			continue
		}
		for key := range base.messages {
			if _, ok := loc.messages[key]; !ok {
				t.Errorf("locale %q is missing key %q", tag, key)
			}
		}
	}
}

// A plural entry must carry every category its language actually uses. Russian
// needs four; a catalogue with only one/other would render "5 дня".
func TestPluralEntriesCoverTheirLanguage(t *testing.T) {
	required := map[string][]PluralCategory{
		"en":      {One, Other},
		"es":      {One, Other},
		"ru":      {One, Few, Many, Other},
		"zh-Hans": {Other},
	}

	b := testBundle(t)
	base := b.locales[DefaultLocale]

	for key, msg := range base.messages {
		if len(msg.forms) == 0 {
			continue
		}
		for tag, categories := range required {
			loc := b.locales[tag]
			entry, ok := loc.messages[key]
			if !ok {
				continue // reported by the coverage test above
			}
			if len(entry.forms) == 0 {
				t.Errorf("locale %q renders %q as a plain string, want plural forms", tag, key)
				continue
			}
			for _, category := range categories {
				if _, ok := entry.forms[category]; !ok {
					t.Errorf("locale %q key %q is missing the %q form", tag, key, category)
				}
			}
		}
	}
}

func TestTranslate(t *testing.T) {
	b := testBundle(t)

	tests := []struct {
		name   string
		locale string
		key    string
		args   []any
		want   string
	}{
		{name: "plain english", locale: "en", key: "nav.sign_in", want: "Sign in"},
		{name: "plain russian", locale: "ru", key: "nav.sign_in", want: "Войти"},
		{name: "plain chinese", locale: "zh-Hans", key: "nav.sign_in", want: "登录"},
		{name: "plain spanish", locale: "es", key: "nav.sign_in", want: "Entrar"},
		{
			name:   "substitution",
			locale: "en",
			key:    "countdown.heading",
			args:   []any{"title", "the reunion"},
			want:   "Until the reunion",
		},
		{
			name:   "substitution in russian",
			locale: "ru",
			key:    "auth.invite.intro",
			args:   []any{"email", "her@example.com"},
			want:   "Вас пригласили редактировать этот сайт как her@example.com.",
		},
		{
			// An unknown locale renders the base catalogue rather than failing.
			name:   "unknown locale falls back",
			locale: "de",
			key:    "nav.sign_in",
			want:   "Sign in",
		},
		{
			// A missing key renders as the key, which is findable; an empty string
			// would just be a hole in the page.
			name:   "missing key renders as itself",
			locale: "en",
			key:    "no.such.key",
			want:   "no.such.key",
		},
		{
			// An unmatched placeholder stays visible so the typo is obvious.
			name:   "unknown placeholder is left alone",
			locale: "en",
			key:    "countdown.heading",
			args:   []any{"wrong", "value"},
			want:   "Until {title}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := b.Printer(tt.locale).T(tt.key, tt.args...)
			if got != tt.want {
				t.Errorf("T(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestTranslatePlural(t *testing.T) {
	b := testBundle(t)

	tests := []struct {
		locale string
		count  int
		want   string
	}{
		{"en", 1, "1 day apart"},
		{"en", 2, "2 days apart"},
		{"en", 0, "0 days apart"},
		{"ru", 1, "1 день порознь"},
		{"ru", 2, "2 дня порознь"},
		{"ru", 5, "5 дней порознь"},
		{"ru", 11, "11 дней порознь"},
		{"ru", 21, "21 день порознь"},
		{"ru", 22, "22 дня порознь"},
		{"ru", 111, "111 дней порознь"},
		{"es", 1, "1 día separados"},
		{"es", 2, "2 días separados"},
		{"zh-Hans", 1, "分开 1 天"},
		{"zh-Hans", 5, "分开 5 天"},
	}

	for _, tt := range tests {
		t.Run(tt.locale+"/"+strconv.Itoa(tt.count), func(t *testing.T) {
			got := b.Printer(tt.locale).N("apart.count", tt.count)
			if got != tt.want {
				t.Errorf("N(apart.count, %d) = %q, want %q", tt.count, got, tt.want)
			}
		})
	}
}

// The browser needs every form so a ticking counter stays grammatical without a
// second copy of the plural rules in JavaScript.
func TestForms(t *testing.T) {
	b := testBundle(t)

	russian := b.Printer("ru").Forms("countdown.unit.days")
	want := map[string]string{"one": "день", "few": "дня", "many": "дней", "other": "дня"}
	for category, expected := range want {
		if russian[category] != expected {
			t.Errorf("Forms()[%q] = %q, want %q", category, russian[category], expected)
		}
	}

	// A non-plural key still yields a usable map.
	single := b.Printer("en").Forms("nav.sign_in")
	if single["other"] != "Sign in" {
		t.Errorf("Forms() on a plain string = %v, want an \"other\" entry", single)
	}
}

func TestResolve(t *testing.T) {
	b := testBundle(t)

	tests := []struct {
		name       string
		query      string
		cookie     string
		accept     string
		configured string
		want       string
	}{
		{name: "query wins", query: "ru", cookie: "es", accept: "en", configured: "zh-Hans", want: "ru"},
		{name: "cookie beats the header", cookie: "es", accept: "en", configured: "ru", want: "es"},
		{name: "header beats the configured default", accept: "ru", configured: "es", want: "ru"},
		{name: "configured default is the last resort", configured: "zh-Hans", want: "zh-Hans"},
		{name: "nothing at all means english", want: "en"},
		{name: "unknown query is ignored", query: "kl", configured: "ru", want: "ru"},
		{name: "unknown cookie is ignored", cookie: "kl", configured: "ru", want: "ru"},
		{name: "region subtag matches the base language", accept: "ru-RU", want: "ru"},
		{name: "quality values are respected", accept: "de;q=0.9, ru;q=0.8", want: "ru"},
		{name: "higher quality wins", accept: "es;q=0.5, ru;q=0.9", want: "ru"},
		{name: "zero quality is refused", accept: "ru;q=0, es;q=0.5", want: "es"},
		{name: "wildcard is skipped", accept: "*", configured: "es", want: "es"},
		{name: "malformed header does not panic", accept: "!!!;q=abc", configured: "es", want: "es"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := "/"
			if tt.query != "" {
				target += "?" + LocaleQuery + "=" + tt.query
			}
			r := httptest.NewRequest(http.MethodGet, target, nil)
			if tt.cookie != "" {
				r.AddCookie(&http.Cookie{Name: LocaleCookie, Value: tt.cookie})
			}
			if tt.accept != "" {
				r.Header.Set("Accept-Language", tt.accept)
			}

			if got := b.Resolve(r, tt.configured); got != tt.want {
				t.Errorf("Resolve() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMatch(t *testing.T) {
	b := testBundle(t)

	tests := map[string]string{
		"ru":      "ru",
		"RU":      "ru",
		"ru-RU":   "ru",
		"zh-Hans": "zh-Hans",
		"zh-hans": "zh-Hans",
		"zh":      "zh-Hans",
		"zh-CN":   "zh-Hans",
		"en-GB":   "en",
		"de":      "",
		"":        "",
		"  ":      "",
	}

	for in, want := range tests {
		t.Run(in, func(t *testing.T) {
			if got := b.Match(in); got != want {
				t.Errorf("Match(%q) = %q, want %q", in, got, want)
			}
		})
	}
}
