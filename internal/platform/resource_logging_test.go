package platform

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestReconcileWarpEffectiveReadback(t *testing.T) {
	for _, tc := range []struct {
		name, configured, effective string
		readError, fail             bool
	}{
		{name: "default-info", effective: "info"},
		{name: "numeric-info", effective: "6"},
		{name: "warning", configured: "warning", effective: "warning"},
		{name: "debug", configured: "debug", effective: "debug"},
		{name: "inherit-vendor-debug", configured: "inherit", effective: "7"},
		{name: "inherit-unset-default", configured: "inherit", effective: "-1"},
		{name: "inherit-empty-default", configured: "inherit", effective: ""},
		{name: "explicit-unset-mismatch", configured: "info", effective: "-1", fail: true},
		{name: "default-mismatch", effective: "debug", fail: true},
		{name: "read-error", readError: true, fail: true},
		{name: "empty-readback", fail: true},
		{name: "unsafe-inherit-readback", configured: "inherit", effective: "private command output", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := fixtureLinuxPaths(t.TempDir())
			unitDir := filepath.Dir(paths.unit)
			for _, dir := range []string{filepath.Dir(paths.binary), unitDir} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for _, path := range []string{paths.binary, paths.unit, filepath.Join(unitDir, "rillway-memory.socket"), filepath.Join(unitDir, "rillway-memory.service")} {
				if err := os.WriteFile(path, []byte("original"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			warpState := filepath.Join(filepath.Dir(paths.state), "rillway-resource-control", "warp-log-level.conf")
			var previous []byte
			if tc.configured != "" {
				value := tc.configured
				if value == "inherit" {
					value = ""
				}
				previous = []byte("[Service]\nLogLevelMax=" + value + "\n")
				if err := os.MkdirAll(filepath.Dir(warpState), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(warpState, previous, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var commands []string
			run := func(_ context.Context, name string, args ...string) ([]byte, error) {
				command := name + " " + strings.Join(args, " ")
				commands = append(commands, command)
				switch command {
				case "systemctl show warp-svc.service --property=LoadState":
					return []byte("LoadState=loaded\n"), nil
				case "systemctl show warp-svc.service --property=LogLevelMax --value":
					if tc.readError {
						return []byte("private command output"), errors.New("private command output")
					}
					return []byte(tc.effective + "\n"), nil
				case "systemctl is-active rillway-resource-control.socket":
					return []byte("active\n"), nil
				}
				return nil, nil
			}
			err := reconcileResourceControl(t.Context(), paths, filepath.Join(paths.root, "config.json"), run)
			want := []string{"systemctl show warp-svc.service --property=LoadState", "systemctl daemon-reload", "systemctl show warp-svc.service --property=LogLevelMax --value"}
			if tc.fail {
				if err == nil || err.Error() != "could not verify WARP service logging; the previous settings were restored" {
					t.Fatalf("unexpected unsafe verification error: %v", err)
				}
				want = append(want, "systemctl daemon-reload")
				for _, name := range []string{"rillway-resource-control.socket", "rillway-resource-control.service", "warp-svc.service.d"} {
					if _, err := os.Lstat(filepath.Join(unitDir, name)); !os.IsNotExist(err) {
						t.Fatalf("rollback retained %s: %v", name, err)
					}
				}
				for _, ext := range []string{".socket", ".service"} {
					data, err := os.ReadFile(filepath.Join(unitDir, "rillway-memory"+ext))
					if err != nil || string(data) != "original" {
						t.Fatalf("legacy unit changed: %v", err)
					}
				}
				if previous == nil {
					if _, err := os.Lstat(warpState); !os.IsNotExist(err) {
						t.Fatalf("new WARP state retained: %v", err)
					}
				} else {
					data, err := os.ReadFile(warpState)
					if err != nil || string(data) != string(previous) {
						t.Fatalf("existing WARP state changed: %v", err)
					}
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				want = append(want, "systemctl enable --now rillway-resource-control.socket", "systemctl is-active rillway-resource-control.socket", "systemctl disable --now rillway-memory.socket", "systemctl stop rillway-memory.service", "systemctl daemon-reload")
				if previous != nil {
					data, err := os.ReadFile(warpState)
					if err != nil || string(data) != string(previous) {
						t.Fatalf("configured WARP state changed: %v", err)
					}
				}
			}
			if !slices.Equal(commands, want) {
				t.Fatalf("readback/migration order: %v, want %v", commands, want)
			}
			t.Logf("%s: %s", tc.name, strings.Join(commands, " -> "))
		})
	}
}
