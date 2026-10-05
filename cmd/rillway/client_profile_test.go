package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"rillway/internal/config"
	"rillway/internal/tui"
	"strings"
	"testing"
)

func TestClientProfileRemembersReferencesPrivately(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "private", "client.json")
	token := filepath.Join(root, "admin.token")
	secret := "never-save-this-token"
	if err := os.WriteFile(token, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	want := tui.ConnectionSettings{BaseURL: "https://192.0.2.10:17892", TokenFile: token, CAFile: filepath.Join(root, "admin.crt")}
	if err := saveClientProfile(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := loadClientProfile(path)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal(got, err)
	}
	data, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	dir, _ := os.Stat(filepath.Dir(path))
	if strings.Contains(string(data), secret) || info.Mode().Perm() != 0o600 || dir.Mode().Perm() != 0o700 {
		t.Fatal("profile leaked a token or has unsafe permissions")
	}
	want.BaseURL = "https://192.0.2.20:17892"
	if err := saveClientProfile(path, want); err != nil {
		t.Fatal(err)
	}
	if got, err = loadClientProfile(path); err != nil || got.BaseURL != want.BaseURL {
		t.Fatal("atomic profile replacement failed")
	}
	for _, bad := range []string{`{"url":"https://example.com","token_file":"token","token":"secret"}`, `{} {}`, `{}`} {
		if err = os.WriteFile(path, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err = loadClientProfile(path); err == nil {
			t.Fatal("invalid or credential-bearing profile accepted")
		}
	}
}

func TestInstalledConfigDiscoveryPreservesExplicitFallback(t *testing.T) {
	root := t.TempDir()
	fhs := filepath.Join(root, "etc.json")
	legacy := filepath.Join(root, "legacy.json")
	fallback := filepath.Join(root, "user.json")
	if got := installedConfigPath("linux", []string{fhs, legacy}, fallback); got != fallback {
		t.Fatal(got)
	}
	if err := os.WriteFile(legacy, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := installedConfigPath("linux", []string{fhs, legacy}, fallback); got != legacy {
		t.Fatal(got)
	}
	if err := os.WriteFile(fhs, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := installedConfigPath("linux", []string{fhs, legacy}, fallback); got != fhs {
		t.Fatal(got)
	}
	if got := installedConfigPath("windows", []string{fhs, legacy}, fallback); got != fallback {
		t.Fatal(got)
	}
}

func TestDefaultConfigPathFollowsOSAndExistingInstallation(t *testing.T) {
	root := t.TempDir()
	linux := []string{filepath.Join(root, "etc", "config.json"), filepath.Join(root, "state", "config.json")}
	if got := defaultConfigPath("linux", root, linux); got != filepath.Join(root, "rillway", "config.json") {
		t.Fatal(got)
	}
	if err := os.MkdirAll(filepath.Dir(linux[1]), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(linux[1], []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := defaultConfigPath("linux", "", linux); got != linux[1] {
		t.Fatal("installed configuration requires user config environment:", got)
	}
	mac := filepath.Join(root, "Rillway", "config.json")
	if got := defaultConfigPath("darwin", root, linux); got != mac {
		t.Fatal("macOS used Linux configuration or wrong application directory:", got)
	}
	legacyMac := filepath.Join(root, "rillway", "config.json")
	if err := os.MkdirAll(filepath.Dir(legacyMac), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyMac, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantMac := legacyMac
	if _, err := os.Stat(mac); err == nil {
		wantMac = mac
	}
	if got := defaultConfigPath("darwin", root, linux); got != wantMac {
		t.Fatal("macOS lost its existing config:", got)
	}
	// On case-insensitive filesystems these two names identify the same file.
	if err := os.MkdirAll(filepath.Dir(mac), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mac, []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := defaultConfigPath("darwin", root, linux); got != mac {
		t.Fatal("macOS convention was not preferred:", got)
	}
	if got := userConfigFilePath("darwin", root, "client.json"); got != filepath.Join(root, "Rillway", "client.json") {
		t.Fatal("new client profile does not use the OS application directory:", got)
	}
}

func TestTerminalConnectionPrefersConfigOverRememberedRemote(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	c := config.Default(filepath.Join(root, "state"))
	c.Listeners.Admin = "192.0.2.20:17892"
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(root, "client.json")
	remote := tui.ConnectionSettings{BaseURL: "https://remote.example:17892", TokenFile: filepath.Join(root, "remote.token"), CAFile: filepath.Join(root, "remote.crt")}
	if err := saveClientProfile(profile, remote); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                           string
		explicitConfig, explicitClient bool
		supplied                       tui.ConnectionSettings
		wantLocal                      bool
		wantURL                        string
	}{
		{name: "implicit config", wantLocal: true, wantURL: "https://192.0.2.20:17892"},
		{name: "explicit client", explicitClient: true, wantURL: remote.BaseURL},
		{name: "explicit config", explicitConfig: true, explicitClient: true, wantLocal: true, wantURL: "https://192.0.2.20:17892"},
		{name: "explicit URL", supplied: remote, wantURL: remote.BaseURL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, local, err := resolveTerminalConnection(t.Context(), path, profile, tc.explicitConfig, tc.explicitClient, tc.supplied)
			if err != nil || local != tc.wantLocal || got.BaseURL != tc.wantURL {
				t.Fatal(got, local, err)
			}
			if local && (got.TokenFile != c.Security.AdminTokenFile || got.CAFile != c.Security.TLSCertFile) {
				t.Fatal("local config mixed with remote credentials:", got)
			}
		})
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	got, local, err := resolveTerminalConnection(t.Context(), path, profile, false, false, tui.ConnectionSettings{})
	if err != nil || local || got.BaseURL != remote.BaseURL {
		t.Fatal("remote-only client was not remembered:", got, local, err)
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("remote-only client created a daemon config")
	}
	if err := os.WriteFile(path, []byte("invalid configuration"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = resolveTerminalConnection(t.Context(), path, profile, false, false, tui.ConnectionSettings{}); err == nil {
		t.Fatal("invalid local config was silently bypassed by a remote profile")
	}
	got, local, err = resolveTerminalConnection(t.Context(), path, profile, false, false, remote)
	if err != nil || local || got.BaseURL != remote.BaseURL {
		t.Fatal("explicit remote URL tried to read the invalid local config:", got, local, err)
	}
	if _, _, err = resolveTerminalConnection(t.Context(), path, filepath.Join(root, "missing-client.json"), false, true, tui.ConnectionSettings{}); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("explicit missing profile silently selected another server:", err)
	}
	if err := os.WriteFile(profile, []byte("invalid profile"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	if _, local, err = resolveTerminalConnection(t.Context(), path, profile, false, false, tui.ConnectionSettings{}); err != nil || !local {
		t.Fatal("implicit broken profile blocked the local config:", local, err)
	}
}

func TestPrivateDefaultConfigDoesNotFallBack(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read protected test directories")
	}
	root := t.TempDir()
	private := filepath.Join(root, "private")
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(private, "config.json")
	if err := os.WriteFile(path, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(private, 0o700) })
	if err := os.Chmod(private, 0); err != nil {
		t.Fatal(err)
	}
	if got := installedConfigPath("linux", []string{path}, filepath.Join(root, "fallback.json")); got != path {
		t.Fatal("private service configuration was bypassed:", got)
	}
	profile := filepath.Join(root, "client.json")
	if err := saveClientProfile(profile, tui.ConnectionSettings{BaseURL: "https://remote.example:17892", TokenFile: filepath.Join(root, "token")}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveTerminalConnection(t.Context(), path, profile, false, false, tui.ConnectionSettings{}); !errors.Is(err, os.ErrPermission) {
		t.Fatal("private config did not retain its permission error:", err)
	}
}
