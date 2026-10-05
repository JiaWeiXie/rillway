package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"rillway/internal/config"
	"rillway/internal/i18n"
	"rillway/internal/platform"
	"strings"
	"testing"
	"time"
)

func TestSetupExplicitConfigWithoutUserConfigEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	deps := setupDependencies{validate: func(context.Context, config.Config) error { return nil }}
	if err := setupCommand(t.Context(), []string{"--yes", "--no-install", "--config", path}, strings.NewReader(""), io.Discard, deps); err != nil {
		t.Fatal("explicit setup path depended on OS config environment:", err)
	}
	if _, err := config.Load(path); err != nil {
		t.Fatal(err)
	}
}

func TestSetupInteractive(t *testing.T) {
	for _, locale := range []i18n.Locale{i18n.English, i18n.TraditionalChinese} {
		t.Run(string(locale), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "staging", "config.json")
			ctx := i18n.WithLocale(t.Context(), locale)
			var out bytes.Buffer
			installs := 0
			deps := setupDependencies{check: func() error { return nil }, install: func(_ context.Context, p string, _ io.Reader, _ io.Writer) error {
				installs++
				if p != path {
					t.Fatalf("installer received %s", p)
				}
				if _, err := config.Load(p); err != nil {
					t.Fatal(err)
				}
				return nil
			}}
			input := "2001:db8::1\n192.0.2.70,192.0.2.70\n\n\n\n\ncorp.example,公司.example\nyes\n"
			// ASCII domains are required by configuration validation; IDNs use punycode.
			input = strings.ReplaceAll(input, "公司.example", "xn--55qx5d.example")
			if err := setupCommand(ctx, []string{"--config", path}, strings.NewReader(input), &out, deps); err != nil {
				t.Fatal(err)
			}
			c, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if installs != 1 || c.Listeners.Admin != "[2001:db8::1]:17892" || c.PAC.ProxyAddress != "[2001:db8::1]:17890" {
				t.Fatalf("bad setup: %+v", c.Listeners)
			}
			if strings.Join(c.Security.AllowedClients, ",") != "127.0.0.0/8,::1/128,192.0.2.70/32,2001:db8::1/128" {
				t.Fatal(c.Security.AllowedClients)
			}
			certBytes, err := os.ReadFile(c.Security.TLSCertFile)
			if err != nil {
				t.Fatal(err)
			}
			block, _ := pem.Decode(certBytes)
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				t.Fatal(err)
			}
			if err = cert.VerifyHostname("2001:db8::1"); err != nil {
				t.Fatal(err)
			}
			token, err := os.ReadFile(c.Security.AdminTokenFile)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out.String(), strings.TrimSpace(string(token))) {
				t.Fatal("printed management secret")
			}
			for _, file := range []string{path, c.Security.AdminTokenFile, c.Security.TLSKeyFile, c.Security.TLSCertFile} {
				info, err := os.Stat(file)
				if err != nil || info.Mode().Perm() != 0o600 {
					t.Fatalf("private file mode: %s %v %v", file, info, err)
				}
			}
			want := "Review setup:"
			if locale == i18n.TraditionalChinese {
				want = "確認安裝設定："
			}
			if !strings.Contains(out.String(), want) {
				t.Fatal(out.String())
			}
		})
	}
}

func TestSetupRejectsBeforeWriting(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		args        []string
		check       error
	}{
		{name: "cancel", input: "\n\n\n\n\n\n\nno\n"},
		{name: "EOF", input: ""},
		{name: "wildcard", args: []string{"--yes", "--listen", "0.0.0.0"}},
		{name: "multicast", args: []string{"--yes", "--listen", "ff02::1"}},
		{name: "client", args: []string{"--yes", "--allow-client", "corp.example"}},
		{name: "privileged port", args: []string{"--yes", "--http-port", "80"}},
		{name: "duplicate ports", args: []string{"--yes", "--http-port", "17891"}},
		{name: "positional", args: []string{"unexpected"}},
		{name: "existing install", args: []string{"--yes"}, check: errors.New(platform.ExistingInstallation)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "new", "config.json")
			var out bytes.Buffer
			err := setupCommand(t.Context(), append([]string{"--config", path}, tc.args...), strings.NewReader(tc.input), &out, setupDependencies{check: func() error { return tc.check }, install: func(context.Context, string, io.Reader, io.Writer) error { t.Fatal("unexpected install"); return nil }})
			if tc.name == "cancel" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("expected rejection")
			}
			if _, err = os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
				t.Fatal("created files before acceptance", err)
			}
		})
	}
}

func TestSetupExistingConfigurationAndNoInstall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	var out bytes.Buffer
	// nil dependencies prove --no-install never performs service preflight/install.
	args := []string{"--yes", "--no-install", "--config", path, "--listen", "192.0.2.21", "--allow-client", "192.0.2.70"}
	if err := setupCommand(t.Context(), args, nil, &out, setupDependencies{}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = setupCommand(t.Context(), args, nil, &out, setupDependencies{}); err == nil {
		t.Fatal("overwrote setup")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("existing config changed")
	}
}

func TestSetupCancellationInterruptsInput(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	reader, writer := io.Pipe()
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- setupCommand(ctx, []string{"--no-install", "--config", filepath.Join(t.TempDir(), "config.json")}, reader, io.Discard, setupDependencies{check: func() error { close(entered); return nil }})
	}()
	// Cancellation is valid before or during a blocking prompt.
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("setup ignored cancellation")
	}
}

func TestLicenseCommandNeedsNoConfiguration(t *testing.T) {
	t.Setenv("RILLWAY_LANG", "en")
	var out bytes.Buffer
	if err := run(t.Context(), []string{"licenses"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"SIL OPEN FONT LICENSE", "tailscale.com", "Apache License"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing notice %s", want)
		}
	}
}

func TestSetupBindFailureDoesNotWriteAndInstallFailureRetainsPrivateStage(t *testing.T) {
	for _, afterWrite := range []bool{false, true} {
		t.Run(fmt.Sprint(afterWrite), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			var out bytes.Buffer
			failure := errors.New("test failure")
			deps := setupDependencies{check: func() error { return nil }, validate: func(context.Context, config.Config) error {
				if !afterWrite {
					return failure
				}
				return nil
			}, install: func(context.Context, string, io.Reader, io.Writer) error { return failure }}
			err := setupCommand(t.Context(), []string{"--yes", "--config", path}, nil, &out, deps)
			if !errors.Is(err, failure) {
				t.Fatal(err)
			}
			_, err = os.Stat(path)
			if afterWrite {
				if err != nil {
					t.Fatal("failed install lost staged config", err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatal("bind validation wrote files")
			}
			if strings.Contains(out.String(), "Installed.") {
				t.Fatal("claimed success after failure")
			}
		})
	}
}
