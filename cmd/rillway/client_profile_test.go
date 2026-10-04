package main

import (
	"os"
	"path/filepath"
	"reflect"
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
	if got := installedConfigPath("darwin", []string{fhs, legacy}, fallback); got != fallback {
		t.Fatal(got)
	}
}
