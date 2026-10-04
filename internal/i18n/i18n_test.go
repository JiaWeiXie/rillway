package i18n

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  Locale
	}{
		{"en", English},
		{" EN ", English},
		{"en_US", English},
		{"zh-Hant", TraditionalChinese},
		{"ZH_hAnT", TraditionalChinese},
		{"zh-TW", TraditionalChinese},
		{"zh_tw", TraditionalChinese},
		{"zh-HK", TraditionalChinese},
		{" zh_HK ", TraditionalChinese},
		{"zh-Hant-TW", TraditionalChinese},
		{"", English},
		{"fr", English},
		{"unknown", English},
		{"zh", English},
		{"zh-CN", English},
		{"zh_CN", English},
		{"zh-Hans", English},
		{"zh-Hans-TW", English},
		{"zh-Hantish", English},
	} {
		t.Run(tc.input, func(t *testing.T) {
			if got := Parse(tc.input); got != tc.want {
				t.Fatalf("Parse(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestAcceptLanguage(t *testing.T) {
	for _, tc := range []struct {
		header string
		want   Locale
	}{
		{"", English},
		{"fr, de;q=0.5", English},
		{"zh-CN, en;q=0.5", English},
		{"zh_CN, zh_TW;q=0.7", TraditionalChinese},
		{"en;q=0.3, zh-Hant;q=0.9", TraditionalChinese},
		{"zh-TW;q=0.3, en;q=0.9", English},
		{"fr;q=1, zh-HK;q=0.5, en;q=0.3", TraditionalChinese},
		{"zh-Hant;q=0.8, en;q=0.8", TraditionalChinese},
		{"en;q=0.8, zh-Hant;q=0.8", English},
		{"en;q=0, zh-Hant;q=0.5", TraditionalChinese},
		{"zh-Hant;q=0, en;q=0.5", English},
		{"zh-Hant;q=invalid, en", English},
		{"zh-Hant;q=-1, en;q=0.5", English},
		{"zh-Hant;q=1.1, en;q=0.5", English},
		{"zh-Hant;q=NaN, en;q=0.5", English},
		{"zh-Hant;q=+Inf, en;q=0.5", English},
		{"zh-Hant;q=0.9;q=0.1, en;q=0.5", English},
		{"zh-Hant;q, en;q=0.5", English},
		{"ZH_hAnT ; Q = 0.9, en;q=0.5", TraditionalChinese},
		{"*, zh-Hant;q=0.5", English},
		{"*;q=0, zh-Hant;q=0.5", TraditionalChinese},
		{"en;q=0, *;q=0.9", TraditionalChinese},
		{"en;q=0, zh-Hant;q=0, *;q=0.5", English},
		{"*;q=0, fr", English},
	} {
		t.Run(tc.header, func(t *testing.T) {
			if got := FromAcceptLanguage(tc.header); got != tc.want {
				t.Fatalf("FromAcceptLanguage(%q) = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}

func TestContextPreservesParentAndCancellation(t *testing.T) {
	type key struct{}
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), key{}, "request value"))
	defer cancel()
	chinese := WithLocale(parent, TraditionalChinese)
	english := WithLocale(chinese, English)
	if FromContext(parent) != English || FromContext(chinese) != TraditionalChinese || FromContext(english) != English {
		t.Fatal("locale did not remain isolated to its context")
	}
	if chinese.Value(key{}) != "request value" || english.Value(key{}) != "request value" {
		t.Fatal("locale context lost a parent value")
	}
	if FromContext(WithLocale(parent, Locale("zh_TW"))) != TraditionalChinese || FromContext(WithLocale(parent, Locale("fr"))) != English {
		t.Fatal("context locale normalization or fallback failed")
	}
	cancel()
	if chinese.Err() != context.Canceled || english.Err() != context.Canceled {
		t.Fatal("locale context lost parent cancellation")
	}
}

func TestTranslateUsesExactKeysAndPreservesUserData(t *testing.T) {
	catalog := map[string]string{"Connected": "已連線 ✅", "Empty": ""}
	if got := Translate(TraditionalChinese, catalog, "Connected"); got != "已連線 ✅" {
		t.Fatalf("known message: %q", got)
	}
	for _, locale := range []Locale{English, "fr", "zh-TW", ""} {
		if got := Translate(locale, catalog, "Connected"); got != "Connected" {
			t.Fatalf("unsupported locale %q changed source: %q", locale, got)
		}
	}
	for _, source := range []string{"公司 🚀", "Connected to 公司 🚀", "prefix: Connected", "connected", "", "100% Connected\n"} {
		if got := Translate(TraditionalChinese, catalog, source); got != source {
			t.Fatalf("unknown or user text changed: %q -> %q", source, got)
		}
	}
	if got := Translate(TraditionalChinese, nil, "Connected"); got != "Connected" {
		t.Fatalf("nil catalog fallback: %q", got)
	}
	if got := Translate(TraditionalChinese, catalog, "Empty"); got != "" {
		t.Fatalf("explicit empty translation was ignored: %q", got)
	}
	if len(catalog) != 2 || catalog["Connected"] != "已連線 ✅" {
		t.Fatal("translation changed the supplied catalog")
	}
}

func TestFormatTranslatesBeforeSubstitutingArguments(t *testing.T) {
	catalog := map[string]string{
		"Outbound %s has %d connections (%.1f%%).": "出口 %[1]s 有 %[2]d 個連線（%.1[3]f%%）。",
	}
	name := "公司 🚀 / Connected 100%"
	source := "Outbound %s has %d connections (%.1f%%)."
	if got, want := Format(TraditionalChinese, catalog, source, name, 3, 25.5), "出口 "+name+" 有 3 個連線（25.5%）。"; got != want {
		t.Fatalf("formatted translation = %q, want %q", got, want)
	}
	if got, want := Format(English, catalog, source, name, 3, 25.5), fmt.Sprintf(source, name, 3, 25.5); got != want {
		t.Fatalf("English formatting = %q, want %q", got, want)
	}
	if got := Format(TraditionalChinese, catalog, "Unknown %q", name); got != fmt.Sprintf("Unknown %q", name) {
		t.Fatalf("unknown format did not preserve the argument: %q", got)
	}
}

func TestSharedCatalogMessages(t *testing.T) {
	for _, source := range []string{
		"Enter a valid management token to sign in.",
		"The WARP+ license key format is invalid. Paste the key without line breaks.",
		"Local Proxy listener reachable; end-to-end verification is separate",
		"embedded node; only advertised tailnet and subnet routes permitted",
		"userspace interface; handshake requires actual traffic",
		"Configuration validation failed: admin listener is required",
		"PAC URL must be an HTTP(S) URL without credentials",
		"service uninstall requires sudo",
	} {
		if translated := Message(TraditionalChinese, source); translated == source || translated == "" || !utf8.ValidString(translated) {
			t.Errorf("missing or invalid shared translation for %q: %q", source, translated)
		}
		if got := Message(English, source); got != source {
			t.Errorf("English source changed: %q", got)
		}
	}
	for _, source := range []string{
		"upstream status: service uninstall requires sudo",
		"Configuration validation failed: unknown outbound \"公司 🚀\"",
		"unknown custom detail ✅",
	} {
		if got := Message(TraditionalChinese, source); got != source {
			t.Errorf("arbitrary data was rewritten: %q -> %q", source, got)
		}
	}
	format := "The Cloudflare verification endpoint returned HTTP %d. The WARP outbound has not been verified."
	if got := fmt.Sprintf(Message(TraditionalChinese, format), 503); !strings.Contains(got, "503") || strings.Contains(got, "%!") {
		t.Errorf("shared format placeholder was damaged: %q", got)
	}
}

func FuzzLanguagePreferences(f *testing.F) {
	for _, input := range []string{"en", "zh-Hant", "zh_TW;q=0.7,en;q=0.5", "zh-CN", "en;q=NaN", "公司 🚀", "\x00\xff"} {
		f.Add(input)
	}
	f.Fuzz(func(t *testing.T, input string) {
		for _, got := range []Locale{Parse(input), FromAcceptLanguage(input)} {
			if got != English && got != TraditionalChinese {
				t.Fatalf("unsupported result %q", got)
			}
		}
	})
}
