package main

import (
	"bytes"
	"path/filepath"
	"rillway/internal/config"
	"rillway/internal/i18n"
	"slices"
	"strings"
	"testing"
)

func TestInitDoesNotOverwrite(t *testing.T) {
	t.Setenv("RILLWAY_LANG", "en")
	path := filepath.Join(t.TempDir(), "config.json")
	var out bytes.Buffer
	if err := run(t.Context(), []string{"init", "--config", path}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Management token:") {
		t.Fatal("init did not identify the management token")
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Outbounds[1].Enabled {
		t.Fatal("WARP must require explicit enable")
	}
	if err = run(t.Context(), []string{"init", "--config", path}, &out); err == nil {
		t.Fatal("init overwrote existing config")
	}
	if err = run(t.Context(), []string{"pac", "--config", path}, &out); err != nil {
		t.Fatal(err)
	}
}

func TestHelpDescribesManagementEntrypoints(t *testing.T) {
	t.Setenv("RILLWAY_LANG", "en")
	var out bytes.Buffer
	if err := run(t.Context(), []string{"help"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"rillway serve", "HTTPS management UI", "rillway tui", "--token-file FILE", "service install|start|stop|restart|status|uninstall"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help is missing %q", want)
		}
	}
}

func TestCLILanguagePreferenceAndOverride(t *testing.T) {
	for _, tc := range []struct {
		name, environment, want string
		args                    []string
	}{
		{"default", "", "observable split proxy", []string{"help"}},
		{"environment", "zh-Hant", "可觀察的網路分流", []string{"help"}},
		{"override", "zh-Hant", "observable split proxy", []string{"--lang", "en", "help"}},
		{"after command", "en", "可觀察的網路分流", []string{"help", "--lang=zh-Hant"}},
		{"flag help", "zh-Hant", "管理權杖檔案", []string{"tui", "--help"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RILLWAY_LANG", tc.environment)
			var out bytes.Buffer
			if err := run(t.Context(), tc.args, &out); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("missing %q in %s", tc.want, out.String())
			}
		})
	}
}

func TestCLILocalizedErrorsPreserveArguments(t *testing.T) {
	t.Setenv("RILLWAY_LANG", "zh-Hant")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"公司🚀"}, `不明指令 "公司🚀"`},
		{[]string{"service"}, "service 需要指定"},
		{[]string{"tui", "--unknown"}, "不明選項：-unknown"},
		{[]string{"tui", "--config"}, "選項需要指定值：-config"},
		{[]string{"--lang"}, "--lang 需要指定"},
		{[]string{"--lang=fr", "help"}, "不支援語言"},
	} {
		var out bytes.Buffer
		err := run(t.Context(), tc.args, &out)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("run(%v) = %v, want %q", tc.args, err, tc.want)
		}
	}
	args := []string{"init", "--config", "--lang", "--lang", "zh-Hant"}
	locale, remaining, err := languageArguments(args, "en")
	if err != nil || locale != i18n.TraditionalChinese || !slices.Equal(remaining, []string{"init", "--config", "--lang"}) {
		t.Fatalf("language parsing changed a flag value: %v %v %v", locale, remaining, err)
	}
}
