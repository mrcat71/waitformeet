// Package i18n renders user-facing text in the language a visitor asked for.
//
// Catalogues are JSON files embedded at build time. A message is either a plain
// string or an object of CLDR plural forms. Placeholders are written {like_this}.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

//go:embed locales/*.json
var localeFS embed.FS

// DefaultLocale is the base catalogue. Every other language falls back to it, and
// it is the only one guaranteed to hold every key.
const DefaultLocale = "en"

// LocaleCookie remembers a visitor's explicit language choice.
const LocaleCookie = "wfm_lang"

// LocaleQuery switches language for one request, and sets the cookie.
const LocaleQuery = "lang"

// message is one catalogue entry: either a single string or a set of plural forms.
type message struct {
	single string
	forms  map[PluralCategory]string
}

// UnmarshalJSON accepts both spellings a catalogue may use for an entry.
func (m *message) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		m.single = single
		return nil
	}

	var forms map[string]string
	if err := json.Unmarshal(data, &forms); err != nil {
		return fmt.Errorf("i18n: entry must be a string or an object of plural forms: %w", err)
	}
	m.forms = make(map[PluralCategory]string, len(forms))
	for k, v := range forms {
		m.forms[PluralCategory(k)] = v
	}
	return nil
}

// Locale is one language's messages.
type Locale struct {
	// Tag is the BCP 47 tag, such as "zh-Hans".
	Tag string
	// Name is the language's own name, for the language switcher.
	Name     string
	messages map[string]message
}

// Bundle holds every loaded language.
type Bundle struct {
	locales map[string]*Locale
	// tags is the supported list in display order.
	tags []string
	log  *slog.Logger

	// missing remembers which keys have already been reported, so a key used on
	// every page render logs once rather than on every request.
	missingMu sync.Mutex
	missing   map[string]bool
}

// Load reads every embedded catalogue.
func Load(log *slog.Logger) (*Bundle, error) {
	entries, err := fs.ReadDir(localeFS, "locales")
	if err != nil {
		return nil, fmt.Errorf("i18n: list catalogues: %w", err)
	}

	b := &Bundle{
		locales: make(map[string]*Locale),
		log:     log,
		missing: make(map[string]bool),
	}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		tag := strings.TrimSuffix(name, ".json")

		body, err := localeFS.ReadFile("locales/" + name)
		if err != nil {
			return nil, fmt.Errorf("i18n: read catalogue %s: %w", name, err)
		}

		var meta struct {
			Name string `json:"@name"`
		}
		if err := json.Unmarshal(body, &meta); err != nil {
			return nil, fmt.Errorf("i18n: parse catalogue %s: %w", name, err)
		}

		var messages map[string]message
		if err := json.Unmarshal(body, &messages); err != nil {
			return nil, fmt.Errorf("i18n: parse catalogue %s: %w", name, err)
		}
		// Keys beginning with @ are metadata, not messages.
		for key := range messages {
			if strings.HasPrefix(key, "@") {
				delete(messages, key)
			}
		}

		b.locales[tag] = &Locale{Tag: tag, Name: meta.Name, messages: messages}
		b.tags = append(b.tags, tag)
	}

	if _, ok := b.locales[DefaultLocale]; !ok {
		return nil, fmt.Errorf("i18n: the default catalogue %s.json is missing", DefaultLocale)
	}
	sort.Strings(b.tags)
	return b, nil
}

// Tags lists the supported locales.
func (b *Bundle) Tags() []string { return b.tags }

// Locales lists the supported locales with their own names, for the switcher.
func (b *Bundle) Locales() []*Locale {
	out := make([]*Locale, 0, len(b.tags))
	for _, tag := range b.tags {
		out = append(out, b.locales[tag])
	}
	return out
}

// Supported reports whether a tag has a catalogue.
func (b *Bundle) Supported(tag string) bool {
	_, ok := b.locales[tag]
	return ok
}

// Match resolves an arbitrary tag to a supported one, preferring an exact match and
// then any locale sharing its base language. It returns "" when nothing matches.
func (b *Bundle) Match(tag string) string {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return ""
	}
	if b.Supported(tag) {
		return tag
	}

	// Case-insensitive exact match, since headers and query strings are not
	// consistent about capitalising region and script subtags.
	for _, candidate := range b.tags {
		if strings.EqualFold(candidate, tag) {
			return candidate
		}
	}

	base := BaseLanguage(tag)
	for _, candidate := range b.tags {
		if BaseLanguage(candidate) == base {
			return candidate
		}
	}
	return ""
}

// Resolve picks the locale for a request.
//
// The order is: an explicit ?lang, then the remembered cookie, then the deployment's
// configured default, and only then the browser's Accept-Language. Anything
// unrecognised is skipped rather than treated as an error.
//
// The configured default deliberately outranks the browser. This is a site made by
// two people who chose the language it is written in; a visitor whose phone happens
// to ask for Spanish should still see the site as they wrote it. A visitor who
// disagrees still wins, because their own choice - the switch in the footer, which
// sets ?lang and then the cookie - is checked first and remembered.
func (b *Bundle) Resolve(r *http.Request, configured string) string {
	if tag := b.Match(r.URL.Query().Get(LocaleQuery)); tag != "" {
		return tag
	}
	if cookie, err := r.Cookie(LocaleCookie); err == nil {
		if tag := b.Match(cookie.Value); tag != "" {
			return tag
		}
	}
	if tag := b.Match(configured); tag != "" {
		return tag
	}
	// No configured default (or one naming a language this build does not carry):
	// the browser's preference is a better guess than falling straight to English.
	if tag := b.matchAcceptLanguage(r.Header.Get("Accept-Language")); tag != "" {
		return tag
	}
	return DefaultLocale
}

// matchAcceptLanguage picks the highest-weighted acceptable language.
func (b *Bundle) matchAcceptLanguage(header string) string {
	type candidate struct {
		tag     string
		quality float64
	}

	var best candidate
	for _, part := range strings.Split(header, ",") {
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		tag = strings.TrimSpace(tag)
		if tag == "" || tag == "*" {
			continue
		}

		quality := 1.0
		if raw, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			parsed, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				continue
			}
			quality = parsed
		}
		if quality <= 0 || quality <= best.quality {
			continue
		}
		if matched := b.Match(tag); matched != "" {
			best = candidate{tag: matched, quality: quality}
		}
	}
	return best.tag
}

// Printer renders messages in one locale.
type Printer struct {
	bundle   *Bundle
	locale   *Locale
	fallback *Locale
}

// Printer builds a renderer for a locale, falling back to the default catalogue for
// anything the locale is missing.
func (b *Bundle) Printer(tag string) *Printer {
	locale, ok := b.locales[tag]
	if !ok {
		locale = b.locales[DefaultLocale]
	}
	return &Printer{bundle: b, locale: locale, fallback: b.locales[DefaultLocale]}
}

// Locale returns the tag being rendered.
func (p *Printer) Locale() string { return p.locale.Tag }

// LocaleName returns the language's own name.
func (p *Printer) LocaleName() string { return p.locale.Name }

// T renders a message. Arguments are alternating placeholder names and values:
//
//	p.T("greeting", "name", "Andrei")
//
// A missing key renders as the key itself, which is ugly on screen but far easier to
// track down than an empty string.
func (p *Printer) T(key string, args ...any) string {
	msg, ok := p.lookup(key)
	if !ok {
		p.bundle.reportMissing(p.locale.Tag, key)
		return key
	}
	if msg.single == "" && len(msg.forms) > 0 {
		// A plural entry used without a count: render the "other" form rather than
		// nothing, so the page still reads sensibly.
		return substitute(msg.forms[Other], args)
	}
	return substitute(msg.single, args)
}

// N renders a message with a count, choosing the plural form for the locale. The
// count is also available to the message as {count}.
func (p *Printer) N(key string, count int, args ...any) string {
	msg, ok := p.lookup(key)
	if !ok {
		p.bundle.reportMissing(p.locale.Tag, key)
		return key
	}

	args = append(args, "count", count)
	if len(msg.forms) == 0 {
		return substitute(msg.single, args)
	}

	category := Plural(p.locale.Tag, count)
	form, ok := msg.forms[category]
	if !ok {
		form = msg.forms[Other]
	}
	return substitute(form, args)
}

// Forms returns every plural form of a key with placeholders already substituted.
//
// The browser needs these to keep a ticking counter grammatical without shipping a
// second copy of the plural rules: it picks among them with Intl.PluralRules.
func (p *Printer) Forms(key string, args ...any) map[string]string {
	msg, ok := p.lookup(key)
	if !ok {
		p.bundle.reportMissing(p.locale.Tag, key)
		return map[string]string{string(Other): key}
	}
	if len(msg.forms) == 0 {
		return map[string]string{string(Other): substitute(msg.single, args)}
	}

	out := make(map[string]string, len(msg.forms))
	for category, form := range msg.forms {
		out[string(category)] = substitute(form, args)
	}
	return out
}

// Has reports whether a key exists, for templates that render optional text.
func (p *Printer) Has(key string) bool {
	_, ok := p.lookup(key)
	return ok
}

func (p *Printer) lookup(key string) (message, bool) {
	if msg, ok := p.locale.messages[key]; ok {
		return msg, true
	}
	if p.fallback != nil && p.fallback != p.locale {
		if msg, ok := p.fallback.messages[key]; ok {
			return msg, true
		}
	}
	return message{}, false
}

func (b *Bundle) reportMissing(tag, key string) {
	b.missingMu.Lock()
	defer b.missingMu.Unlock()

	id := tag + "/" + key
	if b.missing[id] {
		return
	}
	b.missing[id] = true
	if b.log != nil {
		b.log.Warn("missing translation", "locale", tag, "key", key)
	}
}

// substitute replaces {placeholders} with the supplied values.
//
// Unknown placeholders are left in place rather than blanked, so a typo is visible
// on the page instead of silently swallowing text.
func substitute(text string, args []any) string {
	if len(args) == 0 || !strings.Contains(text, "{") {
		return text
	}

	replacements := make([]string, 0, len(args))
	for i := 0; i+1 < len(args); i += 2 {
		name, ok := args[i].(string)
		if !ok {
			continue
		}
		replacements = append(replacements, "{"+name+"}", fmt.Sprint(args[i+1]))
	}
	if len(replacements) == 0 {
		return text
	}
	return strings.NewReplacer(replacements...).Replace(text)
}

// BaseLanguage strips region and script subtags: "zh-Hans" becomes "zh".
func BaseLanguage(tag string) string {
	base, _, _ := strings.Cut(strings.ToLower(tag), "-")
	return base
}
