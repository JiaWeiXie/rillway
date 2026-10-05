package main

import (
	"os"
	"path/filepath"
	"rillway/internal/platform"
	"testing"
)

func TestInstalledServiceConfiguration(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "custom directory", "100%config.json")
	unit, err := platform.SystemdUnit("/usr/local/lib/rillway/rillway", path)
	if err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(root, "rillway.service")
	if err = os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	// The chosen service config wins even when missing: do not switch to another
	// server merely because its config or credentials cannot be read.
	if got := installedServiceConfigPath("linux", unitPath); got != path {
		t.Fatal(got)
	}
	if got := installedServiceConfigPath("darwin", unitPath); got != "" {
		t.Fatal(got)
	}
	if got := installedServiceConfigPath("linux", filepath.Join(root, "missing")); got != "" {
		t.Fatal(got)
	}
}

func TestSystemdConfigurationFormat(t *testing.T) {
	for _, tc := range []struct{ name, unit, want string }{
		{"unquoted legacy", "[Service]\nExecStart=/usr/bin/rillway serve --config /var/lib/rillway/config.json", "/var/lib/rillway/config.json"},
		{"reset", "[Service]\nExecStart=/usr/bin/rillway serve --config /custom/config.json\nExecStart=", ""},
		{"wrong section", "[Unit]\nExecStart=/usr/bin/rillway serve --config /custom/config.json", ""},
		{"relative config", "[Service]\nExecStart=/usr/bin/rillway serve --config relative.json", ""},
		{"invalid quote", "[Service]\nExecStart=/usr/bin/rillway serve --config \"/custom/config.json", ""},
		{"extra arguments", "[Service]\nExecStart=/usr/bin/rillway serve --config /custom/config.json --bad", ""},
		{"wrapper", "[Service]\nExecStart=/usr/bin/env rillway serve --config /custom/config.json", ""},
		{"newline", "[Service]\nExecStart=/usr/bin/rillway serve --config \"/custom/\\nconfig.json\"", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := systemdConfigPath([]byte(tc.unit)); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}
