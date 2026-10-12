package resourcecontrol

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestWarpLogTempSocketSmoke(t *testing.T) {
	c, _ := testController(t)
	c.warpDropinLink = filepath.Join(c.stateDir, "managed-link")
	if err := os.WriteFile(filepath.Join(c.stateDir, warpLogFilename), formatWarpLogLevel(WarpLogLevelInfo), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(c.stateDir, warpLogFilename), c.warpDropinLink); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(c.stateDir, "smoke.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	for _, request := range []string{"{\"action\":\"warp-log.status\"}\n", strings.Repeat(" ", 2048) + "{}\n"} {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		done := make(chan error, 1)
		go func() {
			conn, err := listener.Accept()
			if err != nil {
				done <- err
				return
			}
			defer func() { _ = conn.Close() }()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			handleControl(ctx, conn, c)
			done <- nil
		}()
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", listener.Addr().String())
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.WriteString(conn, request); err != nil {
			t.Fatal(err)
		}
		var reply wireResponse
		err = json.NewDecoder(conn).Decode(&reply)
		_ = conn.Close()
		serverErr := <-done
		cancel()
		if err != nil || serverErr != nil {
			t.Fatalf("temporary socket round trip: %v / %v", err, serverErr)
		}
		if len(request) <= 2048 {
			if !reply.WarpLog.Supported || reply.WarpLog.Level != WarpLogLevelInfo || reply.WarpLog.EffectiveLevel != "info" || reply.Error != "" {
				t.Fatalf("unexpected status reply: %+v", reply)
			}
			t.Logf("temp UNIX warp-log.status: supported=%t level=%s effective=%s revision_present=%t", reply.WarpLog.Supported, reply.WarpLog.Level, reply.WarpLog.EffectiveLevel, reply.WarpLog.Revision != "")
		} else {
			if reply.Error != "Invalid resource control request." {
				t.Fatalf("oversized request accepted: %+v", reply)
			}
			t.Logf("temp UNIX %d-byte request: %s", len(request), reply.Error)
		}
	}
}

func TestWarpLogApplyCommandOrderSmoke(t *testing.T) {
	c, _ := testController(t)
	stateFile := filepath.Join(c.stateDir, warpLogFilename)
	previous := formatWarpLogLevel(WarpLogLevelInfo)
	if err := os.WriteFile(stateFile, previous, 0o600); err != nil {
		t.Fatal(err)
	}
	c.warpDropinLink = filepath.Join(c.stateDir, "managed-link")
	if err := os.Symlink(stateFile, c.warpDropinLink); err != nil {
		t.Fatal(err)
	}
	status, err := c.warpLogStatus(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var commands []string
	runner := c.command
	c.command = func(ctx context.Context, args ...string) ([]byte, error) {
		commands = append(commands, strings.Join(args, " "))
		return runner(ctx, args...)
	}
	save, restore := c.saveWarpLog, c.restoreWarpLog
	c.saveWarpLog = func(data []byte) error {
		commands = append(commands, "save-state")
		return save(data)
	}
	c.restoreWarpLog = func(data []byte) error {
		commands = append(commands, "restore-state")
		return restore(data)
	}
	show := "show warp-svc.service --property=LoadState,LogLevelMax"
	if _, err := c.warpLogApply(t.Context(), WarpLogRequest{Level: WarpLogLevelWarning, Revision: "stale"}); !errors.Is(err, ErrWarpLogConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	if !slices.Equal(commands, []string{show}) {
		t.Fatalf("CAS mismatch mutated settings: %v", commands)
	}
	t.Logf("warp-log.apply stale CAS: %s; no save/reload/restart/rollback", strings.Join(commands, " -> "))
	commands = nil
	// The fake Runner keeps effective info, so warning must fail verification.
	if _, err := c.warpLogApply(t.Context(), WarpLogRequest{Level: WarpLogLevelWarning, Revision: status.Revision}); err == nil || !strings.Contains(err.Error(), "restored") {
		t.Fatalf("effective mismatch: %v", err)
	}
	want := []string{show, "save-state", "daemon-reload", "restart warp-svc.service", show, "restore-state", "daemon-reload", "restart warp-svc.service"}
	if !slices.Equal(commands, want) {
		t.Fatalf("rollback command order: %v, want %v", commands, want)
	}
	data, err := os.ReadFile(stateFile)
	if err != nil || string(data) != string(previous) {
		t.Fatalf("previous state not restored: %q / %v", data, err)
	}
	t.Logf("warp-log.apply effective mismatch: %s; previous state restored", strings.Join(commands, " -> "))
}
