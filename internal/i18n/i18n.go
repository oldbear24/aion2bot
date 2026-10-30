// Package i18n holds every text the bot shows, in English and Czech.
package i18n

import (
	"fmt"
	"strconv"
	"strings"
)

type Lang string

const (
	English Lang = "en"
	Czech   Lang = "cs"
)

// Langs lists the supported languages, the default first.
var Langs = []Lang{English, Czech}

var catalogs = map[Lang]map[string]string{
	English: en,
	Czech:   cs,
}

// Parse maps a Discord locale such as "cs" or "en-US", or a stored
// language code, to a supported language. Anything else is English.
func Parse(locale string) Lang {
	if strings.HasPrefix(strings.ToLower(locale), "cs") {
		return Czech
	}
	return English
}

// Supported reports whether a locale has a translation of its own.
func Supported(locale string) bool {
	return strings.HasPrefix(strings.ToLower(locale), "cs") || strings.HasPrefix(strings.ToLower(locale), "en")
}

// Name is the language's own name, e.g. "Čeština".
func (l Lang) Name() string { return l.T("lang.name") }

// T returns the text for key, formatted with args. A key missing from the
// language falls back to English, and then to the key itself.
func (l Lang) T(key string, args ...any) string {
	s, ok := catalogs[l][key]
	if !ok {
		if s, ok = en[key]; !ok {
			s = key
		}
	}
	if len(args) == 0 {
		return s
	}
	return fmt.Sprintf(s, args...)
}

// N writes a count with the right form of a noun, e.g. "1 player",
// "3 hráči", "5 hráčů". Keys have ".one", ".few" (Czech 2–4) and ".many"
// variants.
func (l Lang) N(n int, key string) string {
	form := "many"
	switch {
	case n == 1:
		form = "one"
	case l == Czech && n >= 2 && n <= 4:
		form = "few"
	}
	return l.T(key+"."+form, n)
}

// nbsp keeps a number from wrapping at its thousands separator.
var nbsp = string(rune(0xA0))

// GS writes a gear score: "4,500" in English, "4 500" in Czech, where a
// comma would read as a decimal point.
func (l Lang) GS(n int) string {
	sep := ","
	if l == Czech {
		sep = nbsp
	}
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + sep + s[i:]
	}
	return s
}
