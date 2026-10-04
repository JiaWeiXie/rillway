// Package i18n localizes Rillway-owned messages without rewriting user data.
package i18n

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Locale is a supported display language. Persist these values, not labels.
type Locale string

const (
	English            Locale = "en"
	TraditionalChinese Locale = "zh-Hant"
)

// Parse normalizes supported language tags. Ambiguous or Simplified Chinese
// tags fall back to English rather than being mislabeled Traditional Chinese.
func Parse(s string) Locale {
	locale, _ := supported(s)
	return locale
}

func supported(s string) (Locale, bool) {
	tag := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "_", "-"))
	switch {
	case tag == "en" || strings.HasPrefix(tag, "en-"):
		return English, true
	case tag == "zh-hant" || strings.HasPrefix(tag, "zh-hant-") || tag == "zh-tw" || tag == "zh-hk":
		return TraditionalChinese, true
	default:
		return English, false
	}
}

// FromAcceptLanguage selects the highest-quality supported language. Equal
// qualities preserve header order; invalid and q=0 entries are skipped. A
// wildcard prefers English unless it was explicitly excluded. With no usable
// preference, the application's default is English.
func FromAcceptLanguage(header string) Locale {
	type preference struct {
		locale   Locale
		quality  float64
		wildcard bool
	}
	var preferences []preference
	excluded := map[Locale]bool{}
	for _, item := range strings.Split(header, ",") {
		parts := strings.Split(item, ";")
		tag := strings.TrimSpace(parts[0])
		locale, ok := supported(tag)
		wildcard := tag == "*"
		if !ok && !wildcard {
			continue
		}
		quality, seenQuality, valid := 1.0, false, true
		for _, parameter := range parts[1:] {
			key, value, found := strings.Cut(strings.TrimSpace(parameter), "=")
			if !strings.EqualFold(strings.TrimSpace(key), "q") {
				continue
			}
			if !found || seenQuality {
				valid = false
				break
			}
			seenQuality = true
			var err error
			quality, err = strconv.ParseFloat(strings.TrimSpace(value), 64)
			// This range check also rejects NaN and infinities.
			if err != nil || !(quality >= 0 && quality <= 1) {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		if quality == 0 {
			if !wildcard {
				excluded[locale] = true
			}
			continue
		}
		preferences = append(preferences, preference{locale: locale, quality: quality, wildcard: wildcard})
	}
	sort.SliceStable(preferences, func(i, j int) bool { return preferences[i].quality > preferences[j].quality })
	for _, item := range preferences {
		if !item.wildcard {
			return item.locale
		}
		for _, locale := range []Locale{English, TraditionalChinese} {
			if !excluded[locale] {
				return locale
			}
		}
	}
	return English
}

type localeKey struct{}

// WithLocale records a normalized locale without modifying the parent context.
func WithLocale(ctx context.Context, locale Locale) context.Context {
	return context.WithValue(ctx, localeKey{}, Parse(string(locale)))
}

// FromContext returns English when no supported locale has been selected.
func FromContext(ctx context.Context) Locale {
	if ctx == nil {
		return English
	}
	if locale, ok := ctx.Value(localeKey{}).(Locale); ok {
		return Parse(string(locale))
	}
	return English
}

// Message translates an exact, known English message from the shared catalog.
// Already-formatted messages containing user or upstream data are never parsed
// or rewritten. Localize their format string before substituting arguments.
func Message(locale Locale, source string) string {
	return Translate(locale, coreCatalog, source)
}

// Translate uses a component's English-to-Traditional-Chinese catalog. Unknown
// messages and unsupported locales pass through unchanged; catalogs are read-only.
func Translate(locale Locale, catalog map[string]string, source string) string {
	if locale == TraditionalChinese {
		if translated, ok := catalog[source]; ok {
			return translated
		}
	}
	return source
}

// Format localizes a format string before inserting arguments, preserving names,
// paths, domains, emoji, and other user data exactly as supplied to fmt.Sprintf.
func Format(locale Locale, catalog map[string]string, source string, args ...any) string {
	return fmt.Sprintf(Translate(locale, catalog, source), args...)
}
