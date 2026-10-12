package platform

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type fakeSystemctlExit int

func (e fakeSystemctlExit) Error() string { return fmt.Sprintf("systemctl exit %d", e) }
func (e fakeSystemctlExit) ExitCode() int { return int(e) }

func TestResourceSocketFreshInstallRollback(t *testing.T) {
	testResourceSocketRollback(t, false, false, false)
}

func TestResourceSocketExistingActiveRollback(t *testing.T) {
	testResourceSocketRollback(t, true, true, true)
}

func TestResourceSocketExistingOtherStatesRollback(t *testing.T) {
	for _, state := range []struct{ enabled, active bool }{{true, false}, {false, true}, {false, false}} {
		t.Run(fmt.Sprintf("enabled=%t_active=%t", state.enabled, state.active), func(t *testing.T) {
			testResourceSocketRollback(t, true, state.enabled, state.active)
		})
	}
}

func testResourceSocketRollback(t *testing.T, existed, wasEnabled, wasActive bool) {
	t.Helper()
	for _, failure := range []string{"enable-error", "active-error", "inactive"} {
		t.Run(failure, func(t *testing.T) {
			paths := fixtureLinuxPaths(t.TempDir())
			unitDir := filepath.Dir(paths.unit)
			for _, dir := range []string{filepath.Dir(paths.binary), unitDir} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			fixtures := map[string][]byte{
				paths.binary: []byte("executable"), paths.unit: []byte("unit"),
				filepath.Join(unitDir, "rillway-memory.socket"):  []byte("legacy socket"),
				filepath.Join(unitDir, "rillway-memory.service"): []byte("legacy service"),
			}
			if existed {
				fixtures[filepath.Join(unitDir, "rillway-resource-control.socket")] = []byte("previous socket")
				fixtures[filepath.Join(unitDir, "rillway-resource-control.service")] = []byte("previous service")
			}
			for path, content := range fixtures {
				if err := os.WriteFile(path, content, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			assertRestored := func() {
				for path, previous := range fixtures {
					data, err := os.ReadFile(path)
					if err != nil || !bytes.Equal(data, previous) {
						t.Fatalf("file not restored %s (%v)", path, err)
					}
				}
				if !existed {
					for _, ext := range []string{".socket", ".service"} {
						if _, err := os.Lstat(filepath.Join(unitDir, "rillway-resource-control"+ext)); !os.IsNotExist(err) {
							t.Fatalf("fresh unit retained: %v", err)
						}
					}
				}
			}
			var commands []string
			enabled, active := wasEnabled, wasActive
			attempted, disabled := false, false
			run := func(_ context.Context, name string, args ...string) ([]byte, error) {
				command := name + " " + strings.Join(args, " ")
				commands = append(commands, command)
				if strings.Contains(command, "rillway-memory") {
					t.Fatalf("legacy units mutated: %s", command)
				}
				switch command {
				case "systemctl is-enabled rillway-resource-control.socket":
					if wasEnabled {
						return []byte("enabled\n"), nil
					}
					return []byte("disabled\n"), fakeSystemctlExit(1)
				case "systemctl enable --now rillway-resource-control.socket":
					attempted, enabled, active = true, true, true
					if failure == "enable-error" {
						return nil, errors.New("partially enabled socket")
					}
				case "systemctl is-active rillway-resource-control.socket":
					if !attempted {
						if wasActive {
							return []byte("active\n"), nil
						}
						return []byte("inactive\n"), fakeSystemctlExit(3)
					}
					if failure == "active-error" {
						return nil, errors.New("active check failed")
					}
					return []byte("inactive\n"), nil
				case "systemctl disable --now rillway-resource-control.socket":
					for _, suffix := range []string{".socket", ".service"} {
						path := filepath.Join(unitDir, "rillway-resource-control"+suffix)
						data, err := os.ReadFile(path)
						if err != nil || bytes.Equal(data, fixtures[path]) {
							t.Fatalf("unit restored before disable: %s (%v)", path, err)
						}
					}
					disabled, enabled, active = true, false, false
				case "systemctl daemon-reload":
					if disabled {
						assertRestored()
					}
				case "systemctl enable rillway-resource-control.socket":
					assertRestored()
					enabled = true
				case "systemctl start rillway-resource-control.socket":
					assertRestored()
					active = true
				}
				return nil, nil
			}
			if err := reconcileResourceControl(t.Context(), paths, filepath.Join(paths.root, "config.json"), run); err == nil {
				t.Fatal("failed activation accepted")
			}
			want := []string{"systemctl show warp-svc.service --property=LoadState"}
			if existed {
				want = append(want, "systemctl is-enabled rillway-resource-control.socket", "systemctl is-active rillway-resource-control.socket")
			}
			want = append(want, "systemctl daemon-reload", "systemctl enable --now rillway-resource-control.socket")
			if failure != "enable-error" {
				want = append(want, "systemctl is-active rillway-resource-control.socket")
			}
			want = append(want, "systemctl disable --now rillway-resource-control.socket", "systemctl daemon-reload")
			if existed && wasEnabled {
				want = append(want, "systemctl enable rillway-resource-control.socket")
			}
			if existed && wasActive {
				want = append(want, "systemctl start rillway-resource-control.socket")
			}
			if !slices.Equal(commands, want) {
				t.Fatalf("rollback order: got %v, want %v", commands, want)
			}
			if enabled != wasEnabled || active != wasActive {
				t.Fatalf("prior state not restored: enabled=%t active=%t", enabled, active)
			}
			t.Logf("%s: %s; restored enabled=%t active=%t; legacy unchanged", failure, strings.Join(commands, " -> "), enabled, active)
		})
	}
}

func TestResourceSocketSnapshotFailsClosed(t *testing.T) {
	for _, query := range []string{"is-enabled", "is-active"} {
		t.Run(query, func(t *testing.T) {
			paths := fixtureLinuxPaths(t.TempDir())
			for _, dir := range []string{filepath.Dir(paths.binary), filepath.Dir(paths.unit)} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			socket := filepath.Join(filepath.Dir(paths.unit), "rillway-resource-control.socket")
			for _, path := range []string{paths.binary, paths.unit, socket} {
				if err := os.WriteFile(path, []byte("original"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
				if args[0] == query {
					return []byte("disabled\n"), errors.New("transport unavailable")
				}
				if args[0] == "is-enabled" {
					return []byte("enabled\n"), nil
				}
				if args[0] != "show" {
					t.Fatalf("mutation on failed snapshot: %v", args)
				}
				return nil, nil
			}
			if err := reconcileResourceControl(t.Context(), paths, filepath.Join(paths.root, "config.json"), run); err == nil {
				t.Fatal("snapshot failure accepted")
			}
			data, err := os.ReadFile(socket)
			if err != nil || string(data) != "original" {
				t.Fatalf("original socket changed: %q %v", data, err)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(paths.state), "rillway-resource-control")); !os.IsNotExist(err) {
				t.Fatal("snapshot failure created resource state")
			}
		})
	}
}
