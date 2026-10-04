package outbound

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"rillway/internal/config"
	"testing"

	"tailscale.com/ipn/ipnstate"
)

func TestTailscaleStateLockAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	lock, err := lockTailscaleState(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	check := func(want string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestTailscaleStateLockHelper$")
		cmd.Env = append(os.Environ(), "RILLWAY_TEST_LOCK_DIR="+dir, "RILLWAY_TEST_LOCK_WANT="+want)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("child lock check: %v %s", err, out)
		}
	}
	check("busy")
	node := &tailscale{stateLock: lock}
	if err := node.Close(); err != nil {
		t.Fatal(err)
	}
	if err := node.Close(); err != nil {
		t.Fatal(err)
	}
	check("available")
}

func TestTailscaleStateLockHelper(t *testing.T) {
	dir := os.Getenv("RILLWAY_TEST_LOCK_DIR")
	if dir == "" {
		t.Skip("subprocess helper")
	}
	lock, err := lockTailscaleState(dir)
	if lock != nil {
		defer func() { _ = lock.Close() }()
	}
	if os.Getenv("RILLWAY_TEST_LOCK_WANT") == "busy" {
		var public config.PublicError
		if !errors.As(err, &public) {
			t.Fatalf("second process was not rejected safely: %v", err)
		}
	} else if err != nil {
		t.Fatalf("released lock could not be acquired: %v", err)
	}
}

func TestTailscaleInitializationFailureReleasesStateLock(t *testing.T) {
	dir := t.TempDir()
	if _, err := newTailscale(t.Context(), config.Outbound{ID: "test", StateDir: dir, AuthKeyFile: dir + "/missing.key"}); err == nil {
		t.Fatal("missing key accepted")
	}
	lock, err := lockTailscaleState(dir)
	if err != nil {
		t.Fatalf("failed initialization stranded the state lock: %v", err)
	}
	_ = lock.Close()
}

func TestTailscaleLoginAfterStopExposesAuthURL(t *testing.T) {
	node := &tailscale{stopped: true, login: func(context.Context) error { return nil }, status: func(context.Context) (*ipnstate.Status, error) {
		return &ipnstate.Status{BackendState: "NeedsLogin", AuthURL: "https://login.tailscale.com/example"}, nil
	}}
	if err := node.Action(t.Context(), "login", ""); err != nil {
		t.Fatal(err)
	}
	status := node.Status(t.Context())
	if status.State != "NeedsLogin" || status.AuthURL == "" {
		t.Fatalf("interactive login remains hidden: %+v", status)
	}
	node.stopped = true
	node.login = func(context.Context) error { return errors.New("login failed") }
	if err := node.Action(t.Context(), "login", ""); err == nil {
		t.Fatal("failure ignored")
	}
	if node.Status(t.Context()).State != "stopped" {
		t.Fatal("failed login cleared the explicit stop")
	}
}
