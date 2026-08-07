package i18n

// PluralCategory is a CLDR plural category. Only the ones the supported languages
// actually use are represented.
type PluralCategory string

const (
	// One is the singular form.
	One PluralCategory = "one"
	// Few is used by Russian for 2 to 4, excluding the teens.
	Few PluralCategory = "few"
	// Many is used by Russian for 0, 5 to 9, and the teens.
	Many PluralCategory = "many"
	// Other is the fallback, and the only form Chinese has.
	Other PluralCategory = "other"
)

// pluralRule maps a count to its category for one language.
//
// These are hand-written from the CLDR rules rather than pulled from a library.
// Every count here is an integer, which collapses the rules enormously: the CLDR
// conditions on v (visible decimal digits) and f (the fractional part) are constant,
// so only the integer clauses survive. The full machinery would be dead weight.
type pluralRule func(n int) PluralCategory

// pluralRules holds one rule per supported base language.
var pluralRules = map[string]pluralRule{
	"en": pluralOneOther,
	"es": pluralOneOther,
	"ru": pluralRussian,
	"zh": pluralOtherOnly,
}

// pluralOneOther covers English and Spanish: exactly one is singular.
func pluralOneOther(n int) PluralCategory {
	if n == 1 {
		return One
	}
	return Other
}

// pluralOtherOnly covers Chinese, which does not inflect for number.
func pluralOtherOnly(int) PluralCategory {
	return Other
}

// pluralRussian implements the CLDR rule for Russian over integers:
//
//	one:  ends in 1, but not 11          -> 1, 21, 101 день
//	few:  ends in 2-4, but not 12-14     -> 2, 23, 104 дня
//	many: everything else                -> 5, 11, 100 дней
//
// Negative counts are folded to their absolute value; the site never shows one, but
// a rule that returned nonsense for -1 would be a trap for later.
func pluralRussian(n int) PluralCategory {
	if n < 0 {
		n = -n
	}
	mod10, mod100 := n%10, n%100

	switch {
	case mod10 == 1 && mod100 != 11:
		return One
	case mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14):
		return Few
	default:
		return Many
	}
}

// Plural returns the category a count takes in the given locale.
// An unknown locale falls back to the English rule.
func Plural(locale string, n int) PluralCategory {
	if rule, ok := pluralRules[BaseLanguage(locale)]; ok {
		return rule(n)
	}
	return pluralOneOther(n)
}
