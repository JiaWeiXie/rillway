package platform

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"rillway/internal/config"
	"strings"
	"testing"
)

func freeAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func fixtureConfig(t *testing.T, dir string) config.Config {
	t.Helper()
	c := config.Default(dir)
	c.Listeners.HTTP = freeAddress(t)
	c.Listeners.SOCKS5 = freeAddress(t)
	c.Listeners.Admin = freeAddress(t)
	c.Listeners.PAC = freeAddress(t)
	return c
}

func fixtureLinuxPaths(root string) linuxPaths {
	return linuxPaths{
		filepath.Join(root, "var/lib/rillway"),
		filepath.Join(root, "etc/rillway"),
		filepath.Join(root, "usr/local/lib/rillway/rillway"),
		filepath.Join(root, "usr/local/bin/rillway"),
		filepath.Join(root, "etc/systemd/system/rillway.service"),
		root,
	}
}

func TestLinuxInstallationPreservesSourceAndRegistersService(t *testing.T) {
	paths := fixtureLinuxPaths(t.TempDir())
	source := filepath.Join(t.TempDir(), "config.json")
	c := fixtureConfig(t, filepath.Join(filepath.Dir(source), "state"))
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
		if name == "systemctl" && len(args) > 0 && args[0] == "is-active" {
			return []byte("active\n"), nil
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
		t.Fatal("source configuration was mutated", err)
	}
	for _, p := range []string{paths.state, paths.configDir} {
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatal("directory permissions are not private", p, err)
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
	for _, want := range []string{"useradd --system --user-group", "chown -R rillway:rillway " + paths.state + " " + paths.configDir, "systemctl enable --now rillway-resource-control.socket", "systemctl is-active rillway-resource-control.socket", "systemctl enable rillway.service", "systemctl start rillway.service"} {
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
			if err := os.WriteFile(p, []byte("occupied"), 0o600); err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(t.TempDir(), "config.json")
			if err := config.Save(source, fixtureConfig(t, t.TempDir())); err != nil {
				t.Fatal(err)
			}
			_, err := installLinux(t.Context(), source, paths, func(context.Context, string, ...string) ([]byte, error) {
				t.Fatal("refused installation ran privileged commands")
				return nil, nil
			}, nil)
			if err == nil || !strings.Contains(err.Error(), "already installed") {
				t.Fatal("occupied path did not report existing installation", p, err)
			}
		})
	}
}

func TestMissingCredentialFailsBeforeCreatingAccount(t *testing.T) {
	paths := fixtureLinuxPaths(t.TempDir())
	source := filepath.Join(t.TempDir(), "config.json")
	c := fixtureConfig(t, filepath.Join(filepath.Dir(source), "state"))
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
	c := fixtureConfig(t, filepath.Join(filepath.Dir(source), "state"))
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
	c := fixtureConfig(t, filepath.Join(filepath.Dir(source), "state"))
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
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "id" {
			return []byte("1000"), nil
		}
		if name == "systemctl" && len(args) > 0 && args[0] == "is-active" {
			return []byte("active\n"), nil
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
	c := fixtureConfig(t, filepath.Join(filepath.Dir(source), "state"))
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
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "id" {
			return []byte("1000"), nil
		}
		if name == "systemctl" && len(args) > 0 && args[0] == "is-active" {
			return []byte("active\n"), nil
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

func TestReconcileResourceControlMigratesLegacyUnitsAndPreservesMemoryState(t *testing.T) {
	root := t.TempDir()
	paths := fixtureLinuxPaths(root)
	if err := os.MkdirAll(filepath.Dir(paths.binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.binary, []byte("mock-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	unitDir := filepath.Dir(paths.unit)
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.unit, []byte("mock-unit"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Set up legacy units
	oldSocket := filepath.Join(unitDir, "rillway-memory.socket")
	oldService := filepath.Join(unitDir, "rillway-memory.service")
	if err := os.WriteFile(oldSocket, []byte("old-socket"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldService, []byte("old-service"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Set up existing memory limit state and symlink
	resourceDir := filepath.Join(filepath.Dir(paths.state), "rillway-resource-control")
	if err := os.MkdirAll(resourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	memState := filepath.Join(resourceDir, "memory-limit.conf")
	customMem := []byte("# rillway-memory custom\n[Service]\nMemoryMax=1073741824\n")
	if err := os.WriteFile(memState, customMem, 0o644); err != nil {
		t.Fatal(err)
	}

	dropinDir := filepath.Join(unitDir, "rillway.service.d")
	if err := os.MkdirAll(dropinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	memLink := filepath.Join(dropinDir, "zzzz-rillway-memory.conf")
	if err := os.Symlink(memState, memLink); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(root, "config.json")

	var commands []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		cmd := strings.Join(append([]string{name}, args...), " ")
		commands = append(commands, cmd)
		if name == "systemctl" && len(args) > 0 && args[0] == "is-active" {
			return []byte("active\n"), nil
		}
		return nil, nil
	}

	if err := reconcileResourceControl(t.Context(), paths, cfgPath, run); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	// Verify legacy units deleted
	if _, err := os.Stat(oldSocket); !os.IsNotExist(err) {
		t.Errorf("old socket was not removed: %v", err)
	}
	if _, err := os.Stat(oldService); !os.IsNotExist(err) {
		t.Errorf("old service was not removed: %v", err)
	}

	// Verify new units exist
	newSocket := filepath.Join(unitDir, "rillway-resource-control.socket")
	newService := filepath.Join(unitDir, "rillway-resource-control.service")
	if _, err := os.Stat(newSocket); err != nil {
		t.Errorf("new socket not found: %v", err)
	}
	if _, err := os.Stat(newService); err != nil {
		t.Errorf("new service not found: %v", err)
	}

	// Verify memory state was preserved
	content, err := os.ReadFile(memState)
	if err != nil || !bytes.Equal(content, customMem) {
		t.Errorf("memory state was altered: %s", string(content))
	}

	// Verify idempotency
	commands = nil
	if err := reconcileResourceControl(t.Context(), paths, cfgPath, run); err != nil {
		t.Fatalf("second reconcile failed: %v", err)
	}
}

func TestReconcileResourceControlRollsBackOnActivationFailure(t *testing.T) {
	root := t.TempDir()
	paths := fixtureLinuxPaths(root)
	if err := os.MkdirAll(filepath.Dir(paths.binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.binary, []byte("mock-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	unitDir := filepath.Dir(paths.unit)
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.unit, []byte("mock-unit"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Legacy units present
	oldSocket := filepath.Join(unitDir, "rillway-memory.socket")
	if err := os.WriteFile(oldSocket, []byte("old-socket"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(root, "config.json")
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" && len(args) > 0 && args[0] == "is-active" {
			return []byte("failed\n"), errors.New("socket activation failed")
		}
		return nil, nil
	}

	err := reconcileResourceControl(t.Context(), paths, cfgPath, run)
	if err == nil {
		t.Fatal("expected failure on socket inactive")
	}

	// Old socket should remain
	if _, err := os.Stat(oldSocket); err != nil {
		t.Errorf("old socket should be retained on rollback: %v", err)
	}

	// New socket should be cleaned up
	newSocket := filepath.Join(unitDir, "rillway-resource-control.socket")
	if _, err := os.Stat(newSocket); !os.IsNotExist(err) {
		t.Errorf("new socket should be cleaned up on rollback: %v", err)
	}
}

func TestReconcileResourceControlRejectsInsecureDropinTarget(t *testing.T) {
	root := t.TempDir()
	paths := fixtureLinuxPaths(root)
	if err := os.MkdirAll(filepath.Dir(paths.binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.binary, []byte("mock-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	unitDir := filepath.Dir(paths.unit)
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dropinDir := filepath.Join(unitDir, "rillway.service.d")
	if err := os.MkdirAll(dropinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	memLink := filepath.Join(dropinDir, "zzzz-rillway-memory.conf")
	if err := os.Symlink("/etc/shadow", memLink); err != nil {
		t.Fatal(err)
	}

	run := func(_ context.Context, _ string, _ ...string) ([]byte, error) { return nil, nil }
	err := reconcileResourceControl(t.Context(), paths, filepath.Join(root, "config.json"), run)
	if err == nil || !strings.Contains(err.Error(), "another target") {
		t.Fatalf("expected rejection of hijacked symlink target: %v", err)
	}
}

func TestReconcileResourceControlWarpDetection(t *testing.T) {
	root := t.TempDir()
	paths := fixtureLinuxPaths(root)
	if err := os.MkdirAll(filepath.Dir(paths.binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.binary, []byte("mock-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	unitDir := filepath.Dir(paths.unit)
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(root, "config.json")
	resourceDir := filepath.Join(filepath.Dir(paths.state), "rillway-resource-control")

	// Case 1: WARP is loaded
	warpLoaded := true
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" && len(args) > 0 {
			if args[0] == "is-active" {
				return []byte("active\n"), nil
			}
			if args[0] == "show" && len(args) > 1 && args[1] == "warp-svc.service" {
				if warpLoaded {
					return []byte("LoadState=loaded\n"), nil
				}
				return []byte("LoadState=not-found\n"), nil
			}
		}
		return nil, nil
	}

	if err := reconcileResourceControl(t.Context(), paths, cfgPath, run); err != nil {
		t.Fatalf("reconcile with warp loaded failed: %v", err)
	}

	// Verify warp-log-level.conf created with default info
	warpState := filepath.Join(resourceDir, "warp-log-level.conf")
	data, err := os.ReadFile(warpState)
	if err != nil || !strings.Contains(string(data), "LogLevelMax=info") {
		t.Fatalf("warp log state file not created with default info: %s, err=%v", string(data), err)
	}

	// Verify symlink created
	warpDropin := filepath.Join(unitDir, "warp-svc.service.d", "zzzz-rillway-log-level.conf")
	target, err := os.Readlink(warpDropin)
	if err != nil || target != warpState {
		t.Fatalf("warp symlink error: target=%s, want=%s, err=%v", target, warpState, err)
	}

	// Case 2: WARP absent on fresh root
	root2 := t.TempDir()
	paths2 := fixtureLinuxPaths(root2)
	_ = os.MkdirAll(filepath.Dir(paths2.binary), 0o755)
	_ = os.WriteFile(paths2.binary, []byte("mock-binary"), 0o755)
	_ = os.MkdirAll(filepath.Dir(paths2.unit), 0o755)

	warpLoaded = false
	if err := reconcileResourceControl(t.Context(), paths2, filepath.Join(root2, "config.json"), run); err != nil {
		t.Fatalf("reconcile with warp absent failed: %v", err)
	}

	warpState2 := filepath.Join(filepath.Dir(paths2.state), "rillway-resource-control", "warp-log-level.conf")
	if _, err := os.Stat(warpState2); !os.IsNotExist(err) {
		t.Fatalf("warp state file should not be created when warp is absent: %v", err)
	}
}

func TestReconcileResourceControlLateWarpConflictLeavesNoAssetsCreated(t *testing.T) {
	root := t.TempDir()
	paths := fixtureLinuxPaths(root)
	if err := os.MkdirAll(filepath.Dir(paths.binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.binary, []byte("mock-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	unitDir := filepath.Dir(paths.unit)
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create conflicting WARP drop-in symlink
	warpDropinDir := filepath.Join(unitDir, "warp-svc.service.d")
	if err := os.MkdirAll(warpDropinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	conflictingWarpLink := filepath.Join(warpDropinDir, "zzzz-rillway-log-level.conf")
	if err := os.Symlink("/etc/shadow", conflictingWarpLink); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(root, "config.json")
	resourceDir := filepath.Join(filepath.Dir(paths.state), "rillway-resource-control")

	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" && len(args) > 1 && args[0] == "show" && args[1] == "warp-svc.service" {
			return []byte("LoadState=loaded\n"), nil
		}
		return nil, nil
	}

	err := reconcileResourceControl(t.Context(), paths, cfgPath, run)
	if err == nil || !strings.Contains(err.Error(), "another target") {
		t.Fatalf("expected rejection of conflicting warp symlink, got: %v", err)
	}

	// Assert that NONE of the previously absent memory/resource assets were created
	if _, err := os.Stat(resourceDir); !os.IsNotExist(err) {
		t.Errorf("resourceDir was created despite preflight conflict: %s", resourceDir)
	}
	memState := filepath.Join(resourceDir, "memory-limit.conf")
	if _, err := os.Stat(memState); !os.IsNotExist(err) {
		t.Errorf("memoryState was created despite preflight conflict: %s", memState)
	}
	memDropinDir := filepath.Join(unitDir, "rillway.service.d")
	if _, err := os.Stat(memDropinDir); !os.IsNotExist(err) {
		t.Errorf("memDropinDir was created despite preflight conflict: %s", memDropinDir)
	}
	memLink := filepath.Join(memDropinDir, "zzzz-rillway-memory.conf")
	if _, err := os.Lstat(memLink); !os.IsNotExist(err) {
		t.Errorf("memLink was created despite preflight conflict: %s", memLink)
	}
	warpState := filepath.Join(resourceDir, "warp-log-level.conf")
	if _, err := os.Stat(warpState); !os.IsNotExist(err) {
		t.Errorf("warpState was created despite preflight conflict: %s", warpState)
	}
	newSocket := filepath.Join(unitDir, "rillway-resource-control.socket")
	if _, err := os.Stat(newSocket); !os.IsNotExist(err) {
		t.Errorf("newSocket was created despite preflight conflict: %s", newSocket)
	}
	newService := filepath.Join(unitDir, "rillway-resource-control.service")
	if _, err := os.Stat(newService); !os.IsNotExist(err) {
		t.Errorf("newService was created despite preflight conflict: %s", newService)
	}
}

func TestReconcileResourceControlRejectsExistingStateDirUnderUnsafeAncestor(t *testing.T) {
	root := t.TempDir()
	paths := fixtureLinuxPaths(root)
	if err := os.MkdirAll(filepath.Dir(paths.binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.binary, []byte("mock-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	unitDir := filepath.Dir(paths.unit)
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ancestorDir := filepath.Dir(paths.state)
	if err := os.MkdirAll(ancestorDir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ancestorDir, 0o777); err != nil {
		t.Fatal(err)
	}

	resourceDir := filepath.Join(ancestorDir, "rillway-resource-control")
	if err := os.MkdirAll(resourceDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(root, "config.json")
	run := func(_ context.Context, _ string, _ ...string) ([]byte, error) { return nil, nil }

	err := reconcileResourceControl(t.Context(), paths, cfgPath, run)
	if err == nil || !strings.Contains(err.Error(), "unsafe parent directory") {
		t.Fatalf("expected rejection of existing state dir under unsafe ancestor, got: %v", err)
	}
}
