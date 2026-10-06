package platform

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"rillway/internal/config"
	"strings"
	"testing"
)

func fixtureLinuxPaths(root string) linuxPaths {
	return linuxPaths{filepath.Join(root, "var/lib/rillway"), filepath.Join(root, "etc/rillway"), filepath.Join(root, "usr/local/lib/rillway/rillway"), filepath.Join(root, "usr/local/bin/rillway"), filepath.Join(root, "etc/systemd/system/rillway.service")}
}

func TestLinuxInstallationPreservesSourceAndRegistersService(t *testing.T) {
	paths := fixtureLinuxPaths(t.TempDir())
	source := filepath.Join(t.TempDir(), "config.json")
	c := config.Default(filepath.Join(filepath.Dir(source), "state"))
	if err := config.Save(source, c); err != nil {
		t.Fatal(err)
	}
	if _, _, err := EnsureCredentials(c); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	token, err := os.ReadFile(c.Security.AdminTokenFile)
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "rillway")
	if err = os.WriteFile(binary, []byte("standalone executable fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	var calls []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		call := strings.Join(append([]string{name}, args...), " ")
		calls = append(calls, call)
		if name == "id" {
			return nil, errors.New("no service user")
		}
		return nil, nil
	}
	if _, err = installLinux(t.Context(), source, paths, run, func() (string, error) { return binary, nil }); err != nil {
		t.Fatal(err)
	}
	installed, err := config.Load(filepath.Join(paths.configDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if installed.Security.AdminTokenFile != filepath.Join(paths.state, "admin.token") {
		t.Fatal(installed.Security)
	}
	installedToken, err := os.ReadFile(installed.Security.AdminTokenFile)
	if err != nil || !bytes.Equal(installedToken, token) {
		t.Fatal("token changed", err)
	}
	after, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("source changed")
	}
	link, err := os.Readlink(paths.link)
	if err != nil || link != paths.binary {
		t.Fatal("PATH symlink", link, err)
	}
	memoryLink := filepath.Join(filepath.Dir(paths.unit), "rillway.service.d", "zzzz-rillway-memory.conf")
	target, err := os.Readlink(memoryLink)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := os.ReadFile(target)
	if err != nil || !strings.Contains(string(initial), "[Service]") || strings.Contains(string(initial), "MemoryMax=") {
		t.Fatal("memory installation changes limits or leaves a dangling drop-in", err)
	}
	for _, p := range []string{paths.state, paths.configDir} {
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("directory permission: %s %v", p, err)
		}
	}
	info, err := os.Stat(paths.binary)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatal("binary not executable")
	}
	unit, err := os.ReadFile(paths.unit)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"User=rillway", "ConfigurationDirectoryMode=0700", "ReadWritePaths=/var/lib/rillway /etc/rillway", filepath.Join(paths.configDir, "config.json")} {
		if !strings.Contains(string(unit), want) {
			t.Error("missing unit setting", want)
		}
	}
	joined := strings.Join(calls, "\n")
	for _, want := range []string{"useradd --system --user-group", "chown -R rillway:rillway " + paths.state + " " + paths.configDir, "systemctl daemon-reload\nsystemctl enable rillway.service\nsystemctl enable --now rillway-memory.socket\nsystemctl start rillway.service"} {
		if !strings.Contains(joined, want) {
			t.Error("missing installer command", want)
		}
	}
	calls = nil
	if _, err = installLinux(t.Context(), source, paths, run, func() (string, error) { return binary, nil }); err == nil || len(calls) != 0 {
		t.Fatal("reinstall must refuse before mutation")
	}
}

func TestInstallerRefusesEveryRetainedPath(t *testing.T) {
	for _, kind := range []string{"unit", "binary", "link", "config", "state"} {
		t.Run(kind, func(t *testing.T) {
			paths := fixtureLinuxPaths(t.TempDir())
			p := map[string]string{"unit": paths.unit, "binary": paths.binary, "link": paths.link, "config": paths.configDir, "state": paths.state}[kind]
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if kind == "link" {
				if err := os.Symlink("missing", p); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(p, []byte("retain me"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := installLinux(t.Context(), "does-not-exist", paths, func(context.Context, string, ...string) ([]byte, error) {
				t.Fatal("ran privileged command")
				return nil, nil
			}, nil)
			if err == nil || !strings.Contains(err.Error(), ExistingInstallation) {
				t.Fatal(err)
			}
			if _, err = os.Lstat(p); err != nil {
				t.Fatal("removed retained file")
			}
		})
	}
}

func TestMissingCredentialFailsBeforeCreatingAccount(t *testing.T) {
	paths := fixtureLinuxPaths(t.TempDir())
	source := filepath.Join(t.TempDir(), "config.json")
	c := config.Default(filepath.Join(filepath.Dir(source), "state"))
	c.Security.ProxyUsername = "user"
	c.Security.ProxyPasswordFile = filepath.Join(filepath.Dir(source), "missing.password")
	if err := config.Save(source, c); err != nil {
		t.Fatal(err)
	}
	_, err := installLinux(t.Context(), source, paths, func(context.Context, string, ...string) ([]byte, error) { t.Fatal("created account"); return nil, nil }, nil)
	if err == nil {
		t.Fatal("missing credential accepted")
	}
	if _, err = os.Stat(paths.state); !os.IsNotExist(err) {
		t.Fatal("created state")
	}
}

func TestDisabledOutboundFilesDoNotBlockInstallationPreflight(t *testing.T) {
	paths := fixtureLinuxPaths(t.TempDir())
	source := filepath.Join(t.TempDir(), "config.json")
	c := config.Default(filepath.Join(filepath.Dir(source), "state"))
	c.Outbounds = append(c.Outbounds, config.DefaultsForForms(c).Outbounds["wireguard"])
	if err := config.Save(source, c); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("reached account preflight")
	_, err := installLinux(t.Context(), source, paths, func(context.Context, string, ...string) ([]byte, error) {
		return nil, sentinel
	}, func() (string, error) { return "", sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("disabled outbound blocked installation before account preflight: %v", err)
	}
}

func TestDisabledSuggestedOutboundsInstallWithoutSourceFiles(t *testing.T) {
	paths := fixtureLinuxPaths(t.TempDir())
	source := filepath.Join(t.TempDir(), "config.json")
	c := config.Default(filepath.Join(filepath.Dir(source), "state"))
	forms := config.DefaultsForForms(c)
	c.Outbounds = append(c.Outbounds, forms.Outbounds["wireguard"], forms.Outbounds["tailscale"])
	if err := config.Save(source, c); err != nil {
		t.Fatal(err)
	}
	if _, _, err := EnsureCredentials(c); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "rillway")
	if err := os.WriteFile(binary, []byte("executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "id" {
			return []byte("1000"), nil
		}
		return nil, nil
	}
	if _, err := installLinux(t.Context(), source, paths, run, func() (string, error) { return binary, nil }); err != nil {
		t.Fatal(err)
	}
	installed, err := config.Load(filepath.Join(paths.configDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range installed.Outbounds {
		if profile.ID == "wireguard" && profile.ConfigFile != filepath.Join(paths.state, "wireguard.conf") {
			t.Fatalf("unexpected disabled WireGuard path: %s", profile.ConfigFile)
		}
		if profile.ID == "tailscale" && profile.StateDir != filepath.Join(paths.state, "outbounds", "tailscale") {
			t.Fatalf("unexpected disabled Tailscale path: %s", profile.StateDir)
		}
	}
}

func TestEnabledTailscaleInstallCreatesFreshStateDirectory(t *testing.T) {
	paths := fixtureLinuxPaths(t.TempDir())
	source := filepath.Join(t.TempDir(), "config.json")
	c := config.Default(filepath.Join(filepath.Dir(source), "state"))
	profile := config.DefaultsForForms(c).Outbounds["tailscale"]
	profile.Enabled = true
	profile.StateDir = filepath.Join(t.TempDir(), "not-created-yet")
	c.Outbounds = append(c.Outbounds, profile)
	if err := config.Save(source, c); err != nil {
		t.Fatal(err)
	}
	if _, _, err := EnsureCredentials(c); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "rillway")
	if err := os.WriteFile(binary, []byte("executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "id" {
			return []byte("1000"), nil
		}
		return nil, nil
	}
	if _, err := installLinux(t.Context(), source, paths, run, func() (string, error) { return binary, nil }); err != nil {
		t.Fatal(err)
	}
	installed, err := config.Load(filepath.Join(paths.configDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, outbound := range installed.Outbounds {
		if outbound.ID == profile.ID && outbound.StateDir != filepath.Join(paths.state, "outbounds", profile.ID) {
			t.Fatalf("fresh Tailscale state directory was not mapped into service state: %s", outbound.StateDir)
		}
	}
}
