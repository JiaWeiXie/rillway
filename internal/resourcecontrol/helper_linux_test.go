package resourcecontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"rillway/internal/config"
	"strconv"
	"strings"
	"testing"
)

type fakeSystem struct {
	limit, high, current uint64
	sets                 int
	fail                 bool
	failures             int
	warpLoaded           bool
	warpLogLevel         string
	warpRestarts         int
}

func (f *fakeSystem) run(_ context.Context, args ...string) ([]byte, error) {
	switch args[0] {
	case "show":
		if len(args) > 1 && args[1] == "warp-svc.service" {
			loadState := "not-found"
			if f.warpLoaded {
				loadState = "loaded"
			}
			lvl := f.warpLogLevel
			if lvl == "" {
				lvl = "info"
			}
			return []byte(fmt.Sprintf("LoadState=%s\nLogLevelMax=%s\n", loadState, lvl)), nil
		}
		return []byte(fmt.Sprintf("LoadState=loaded\nControlGroup=/system.slice/rillway.service\nMemoryCurrent=%d\nMemoryMax=%d\nMemoryHigh=%d\n", f.current, f.limit, f.high)), nil
	case "daemon-reload":
		if f.fail || f.failures > 0 {
			f.failures--
			return nil, errors.New("command output containing token-123456 and /private/var/credentials")
		}
		return nil, nil
	case "restart":
		if f.fail || f.failures > 0 {
			f.failures--
			return nil, errors.New("command output containing token-123456 and /private/var/credentials")
		}
		if len(args) > 1 && args[1] == "warp-svc.service" {
			f.warpRestarts++
		}
		return nil, nil
	case "set-property":
		if f.fail || f.failures > 0 {
			f.failures--
			return nil, errors.New("command output containing token-123456 and /private/var/credentials")
		}
		f.sets++
		for _, arg := range args[2:] {
			if strings.HasPrefix(arg, "MemoryMax=") {
				f.limit = parseBytes(strings.TrimPrefix(arg, "MemoryMax="))
			}
			if strings.HasPrefix(arg, "MemoryHigh=") {
				f.high = parseBytes(strings.TrimPrefix(arg, "MemoryHigh="))
			}
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected command %q", args[0])
	}
}

func testController(t *testing.T) (*controller, *fakeSystem) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := config.Save(path, config.Default(dir)); err != nil {
		t.Fatal(err)
	}
	f := &fakeSystem{limit: 512 * MiB, high: 384 * MiB, current: 32 * MiB, warpLoaded: true, warpLogLevel: "info"}
	c := &controller{
		configPath:  path,
		stateDir:    dir,
		command:     f.run,
		capacity:    func(string) (uint64, error) { return 4 * GiB, nil },
		saveWarpLog: func(b []byte) error { return os.WriteFile(filepath.Join(dir, warpLogFilename), b, 0o600) },
		restoreWarpLog: func(b []byte) error {
			if b == nil {
				return os.Remove(filepath.Join(dir, warpLogFilename))
			}
			return os.WriteFile(filepath.Join(dir, warpLogFilename), b, 0o600)
		},
	}
	c.save = func(m metadata) error {
		return os.WriteFile(filepath.Join(dir, memoryFilename), formatMetadata(m), 0o600)
	}
	c.restore = func(b []byte) error {
		if b == nil {
			return os.Remove(filepath.Join(dir, memoryFilename))
		}
		return os.WriteFile(filepath.Join(dir, memoryFilename), b, 0o600)
	}
	return c, f
}

func TestControllerLivePersistenceCASAndRollback(t *testing.T) {
	c, f := testController(t)
	ctx := context.Background()
	s, err := c.status(ctx)
	if err != nil || s.HostBytes != 4*GiB || s.LimitBytes != 512*MiB || s.MinimumBytes != 256*MiB || s.GoLimitBytes != 0 {
		t.Fatal(s, err)
	}
	old := s.Revision
	for _, req := range []MemoryRequest{{Mode: "GiB", Value: "0.1", Revision: old}, {Mode: "percent", Value: "91", Revision: old}, {Mode: "MiB", Value: "600", Revision: "stale"}} {
		if _, err = c.apply(ctx, req); err == nil || f.sets != 0 {
			t.Fatal("invalid or stale write mutated service", err, f.sets)
		}
	}
	s, err = c.apply(ctx, MemoryRequest{Mode: "percent", Value: "25", Revision: old})
	if err != nil || f.limit != GiB || f.high != 768*MiB || s.Mode != "percent" || s.Value != "25" || s.GoLimitBytes != 704*MiB || s.Revision == old {
		t.Fatal(s, err, f)
	}
	if _, err = c.apply(ctx, MemoryRequest{Mode: "GiB", Value: "0.5", Revision: old}); !errors.Is(err, ErrMemoryConflict) {
		t.Fatal(err)
	}
	// A new controller process restores the exact requested mode, not a guessed
	// value based on the service's own cap.
	again := *c
	s, err = again.status(ctx)
	if err != nil || s.HostBytes != 4*GiB || s.Mode != "percent" {
		t.Fatal(s, err)
	}
	save := c.save
	c.save = func(metadata) error { return errors.New("private-filesystem-path") }
	if _, err = c.apply(ctx, MemoryRequest{Mode: "GiB", Value: "0.5", Revision: s.Revision}); err == nil || !strings.Contains(err.Error(), "unchanged") || f.limit != GiB || f.high != 768*MiB || strings.Contains(err.Error(), "private") {
		t.Fatal(err, f)
	}
	f.current = 900 * MiB
	s, err = c.status(ctx)
	if err != nil || s.MinimumBytes != 1125*MiB {
		t.Fatal(s, err)
	}
	if _, err = c.apply(ctx, MemoryRequest{Mode: "MiB", Value: "1024", Revision: s.Revision}); err == nil {
		t.Fatal("current usage headroom ignored")
	}
	c.save = save
	f.fail = true
	if _, err = c.apply(ctx, MemoryRequest{Mode: "GiB", Value: "1.5", Revision: s.Revision}); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("command diagnostic leaked", err)
	}
}

func TestControllerPropertyFailureRestoresAtomicState(t *testing.T) {
	c, f := testController(t)
	ctx := context.Background()
	s, _ := c.status(ctx)
	s, err := c.apply(ctx, MemoryRequest{Mode: "MiB", Value: "600", Revision: s.Revision})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(c.stateDir, memoryFilename))
	if err != nil {
		t.Fatal(err)
	}
	f.failures = 1
	if _, err = c.apply(ctx, MemoryRequest{Mode: "GiB", Value: "1", Revision: s.Revision}); err == nil || !strings.Contains(err.Error(), "restored") {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(c.stateDir, memoryFilename))
	if !bytes.Equal(before, after) || f.limit != 600*MiB || f.high != 450*MiB {
		t.Fatal("failed property write left different persistent/live limits")
	}
}

func TestControllerRechecksEnabledVPNFloor(t *testing.T) {
	c, _ := testController(t)
	s, _ := c.status(context.Background())
	cfg, err := config.Load(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	// A valid profile is not needed for a policy decision; use Tailscale, whose
	// state directory and hostname can be validated without signing in.
	cfg.Revision++
	cfg.Outbounds = append(cfg.Outbounds, config.Outbound{ID: "tail", Type: "tailscale", Enabled: true, Hostname: "test-node", StateDir: filepath.Join(c.stateDir, "tail")})
	if err = config.Save(c.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err = c.apply(context.Background(), MemoryRequest{Mode: "MiB", Value: "512", Revision: s.Revision}); !errors.Is(err, ErrMemoryConflict) {
		t.Fatal(err)
	}
	after, err := c.status(context.Background())
	if err != nil || after.MinimumBytes != GiB {
		t.Fatal(after, err)
	}
	if _, err = c.apply(context.Background(), MemoryRequest{Mode: "MiB", Value: "512", Revision: after.Revision}); err == nil {
		t.Fatal("embedded VPN minimum ignored")
	}
}

func TestWarpLogStatusWhenServiceNotLoaded(t *testing.T) {
	c, f := testController(t)
	f.warpLoaded = false
	s, err := c.warpLogStatus(context.Background())
	if err != nil || s.Supported || !strings.Contains(s.Reason, "not installed or loaded") {
		t.Fatalf("unexpected status for uninstalled warp: %+v %v", s, err)
	}
}

func TestWarpLogStatusWhenDropinNotConfigured(t *testing.T) {
	c, f := testController(t)
	f.warpLoaded = true
	c.warpDropinLink = filepath.Join(t.TempDir(), "nonexistent-link")
	s, err := c.warpLogStatus(context.Background())
	if err != nil || s.Supported || !strings.Contains(s.Reason, "service restart") {
		t.Fatalf("unexpected status for missing link: %+v %v", s, err)
	}
}

func TestWarpLogLifecycleAndRollback(t *testing.T) {
	c, f := testController(t)
	f.warpLoaded = true
	f.warpLogLevel = "info"

	linkDir := t.TempDir()
	c.warpDropinLink = filepath.Join(linkDir, "zzzz-rillway-log-level.conf")
	stateFile := filepath.Join(c.stateDir, warpLogFilename)
	if err := os.Symlink(stateFile, c.warpDropinLink); err != nil {
		t.Fatal(err)
	}
	// Effective properties change on restart, not before the CAS check.
	c.command = func(ctx context.Context, args ...string) ([]byte, error) {
		out, err := f.run(ctx, args...)
		if err == nil && args[0] == "restart" && args[1] == "warp-svc.service" {
			data, readErr := os.ReadFile(stateFile)
			if readErr != nil {
				return nil, readErr
			}
			level := parseWarpLogLevelFile(data)
			f.warpLogLevel = string(level)
			if level == WarpLogLevelInherit {
				f.warpLogLevel = "6"
			}
		}
		return out, err
	}

	ctx := context.Background()
	s, err := c.warpLogStatus(ctx)
	if err != nil || !s.Supported || s.Level != WarpLogLevelInfo || s.EffectiveLevel != "info" || s.Revision == "" {
		t.Fatalf("unexpected initial status: %+v %v", s, err)
	}

	initialRev := s.Revision

	// Apply with stale revision
	_, err = c.warpLogApply(ctx, WarpLogRequest{Level: WarpLogLevelDebug, Revision: "stale"})
	if !errors.Is(err, ErrWarpLogConflict) {
		t.Fatalf("expected ErrWarpLogConflict, got: %v", err)
	}

	// Apply with invalid level
	_, err = c.warpLogApply(ctx, WarpLogRequest{Level: "trace", Revision: initialRev})
	if err == nil || !strings.Contains(err.Error(), "Invalid WARP log level") {
		t.Fatalf("expected invalid level error, got: %v", err)
	}

	// Apply debug successfully
	s, err = c.warpLogApply(ctx, WarpLogRequest{Level: WarpLogLevelDebug, Revision: initialRev})
	if err != nil || s.Level != WarpLogLevelDebug || s.Revision == initialRev || f.warpRestarts != 1 {
		t.Fatalf("apply debug failed: %+v %v restarts=%d", s, err, f.warpRestarts)
	}

	data, _ := os.ReadFile(stateFile)
	if !strings.Contains(string(data), "LogLevelMax=debug") {
		t.Fatalf("state file content: %s", string(data))
	}

	// Apply inherit
	prevRev := s.Revision
	s, err = c.warpLogApply(ctx, WarpLogRequest{Level: WarpLogLevelInherit, Revision: prevRev})
	if err != nil || s.Level != WarpLogLevelInherit || s.EffectiveLevel != "info" {
		t.Fatalf("apply inherit failed: %+v %v", s, err)
	}
	data, _ = os.ReadFile(stateFile)
	if !strings.Contains(string(data), "LogLevelMax=\n") {
		t.Fatalf("state file inherit content: %s", string(data))
	}

	// Rollback on failure
	f.fail = true
	_, err = c.warpLogApply(ctx, WarpLogRequest{Level: WarpLogLevelWarning, Revision: s.Revision})
	if err == nil || !strings.Contains(err.Error(), "restored") || strings.Contains(err.Error(), "credentials") {
		t.Fatalf("expected sanitized rollback error: %v", err)
	}
}

func TestWarpLogApplyRollsBackOnReadbackError(t *testing.T) {
	c, f := testController(t)
	f.warpLoaded = true
	f.warpLogLevel = "info"

	linkDir := t.TempDir()
	c.warpDropinLink = filepath.Join(linkDir, "zzzz-rillway-log-level.conf")
	stateFile := filepath.Join(c.stateDir, warpLogFilename)
	if err := os.Symlink(stateFile, c.warpDropinLink); err != nil {
		t.Fatal(err)
	}

	initialData := []byte("# rillway-resource-control\n[Service]\nLogLevelMax=info\n")
	if err := os.WriteFile(stateFile, initialData, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, err := c.warpLogStatus(ctx)
	if err != nil || !s.Supported {
		t.Fatal(err)
	}

	originalCommand := c.command
	restarted := false
	c.command = func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "restart" && len(args) > 1 && args[1] == "warp-svc.service" {
			restarted = true
		}
		if restarted && args[0] == "show" {
			return nil, errors.New("readback failure")
		}
		return originalCommand(ctx, args...)
	}

	_, err = c.warpLogApply(ctx, WarpLogRequest{Level: WarpLogLevelDebug, Revision: s.Revision})
	if err == nil || !strings.Contains(err.Error(), "restored") {
		t.Fatalf("expected restored rollback error on readback failure, got: %v", err)
	}

	data, _ := os.ReadFile(stateFile)
	if !bytes.Equal(data, initialData) {
		t.Fatalf("state file was not restored to initial: %s", string(data))
	}

	if f.warpRestarts != 2 {
		t.Fatalf("expected 2 restarts (apply + rollback), got: %d", f.warpRestarts)
	}
}

func TestWarpLogApplyRollsBackOnEffectiveLevelMismatch(t *testing.T) {
	c, f := testController(t)
	f.warpLoaded = true
	f.warpLogLevel = "info"

	linkDir := t.TempDir()
	c.warpDropinLink = filepath.Join(linkDir, "zzzz-rillway-log-level.conf")
	stateFile := filepath.Join(c.stateDir, warpLogFilename)
	if err := os.Symlink(stateFile, c.warpDropinLink); err != nil {
		t.Fatal(err)
	}

	initialData := []byte("# rillway-resource-control\n[Service]\nLogLevelMax=info\n")
	if err := os.WriteFile(stateFile, initialData, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, err := c.warpLogStatus(ctx)
	if err != nil || !s.Supported {
		t.Fatal(err)
	}

	// Requested level is debug, but systemd returns info (mismatch)
	f.warpLogLevel = "info"

	_, err = c.warpLogApply(ctx, WarpLogRequest{Level: WarpLogLevelDebug, Revision: s.Revision})
	if err == nil || !strings.Contains(err.Error(), "restored") {
		t.Fatalf("expected restored rollback error on level mismatch, got: %v", err)
	}

	data, _ := os.ReadFile(stateFile)
	if !bytes.Equal(data, initialData) {
		t.Fatalf("state file was not restored to initial: %s", string(data))
	}

	if f.warpRestarts != 2 {
		t.Fatalf("expected 2 restarts (apply + rollback), got: %d", f.warpRestarts)
	}
}

func TestNormalizeLogLevelMaxPreservesNoticePriority(t *testing.T) {
	for _, input := range []string{"notice", "5"} {
		if got := normalizeLogLevelMax(input); got != "notice" {
			t.Fatalf("normalizeLogLevelMax(%q) = %q, want notice", input, got)
		}
	}
}

type controlBuffers struct {
	io.Reader
	io.Writer
}

func TestControllerStrictBoundedProtocol(t *testing.T) {
	c, f := testController(t)
	linkDir := t.TempDir()
	c.warpDropinLink = filepath.Join(linkDir, "zzzz-rillway-log-level.conf")
	_ = os.Symlink(filepath.Join(c.stateDir, warpLogFilename), c.warpDropinLink)

	for _, input := range []string{
		"{}\n",
		"{\"action\":\"reboot\"}\n",
		"{\"action\":\"memory.status\",\"command\":\"bad\"}\n",
		"{\"action\":\"memory.status\"} {}\n",
		"{\"action\":\"memory.status\"}",
		strings.Repeat(" ", 2048) + "{}\n",
		"{\"action\":\"warp-log.status\",\"memory\":{\"mode\":\"MiB\",\"value\":\"512\"}}\n",
		"{\"action\":\"warp-log.apply\"}\n",
	} {
		var output bytes.Buffer
		handleControl(context.Background(), controlBuffers{strings.NewReader(input), &output}, c)
		var reply wireResponse
		if err := json.Unmarshal(output.Bytes(), &reply); err != nil || reply.Error != "Invalid resource control request." || f.sets != 0 {
			t.Fatal(input, output.String(), err)
		}
	}
	var output bytes.Buffer
	handleControl(context.Background(), controlBuffers{strings.NewReader("{\"action\":\"memory.status\"}\n"), &output}, c)
	var reply wireResponse
	if err := json.Unmarshal(output.Bytes(), &reply); err != nil || !reply.Memory.Supported || reply.Error != "" {
		t.Fatal(reply, err)
	}

	output.Reset()
	handleControl(context.Background(), controlBuffers{strings.NewReader("{\"action\":\"warp-log.status\"}\n"), &output}, c)
	reply = wireResponse{}
	if err := json.Unmarshal(output.Bytes(), &reply); err != nil || !reply.WarpLog.Supported || reply.Error != "" {
		t.Fatal(reply, err)
	}
}

func TestPeerMustBelongToServiceCgroup(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("run as a non-root user to test the cgroup boundary")
	}
	path := filepath.Join(t.TempDir(), "control.sock")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	remote, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = remote.Close() }()
	conn, err := l.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		t.Fatal(err)
	}
	group := ""
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "0::") {
			group = strings.TrimPrefix(line, "0::")
		}
	}
	if group == "" {
		t.Skip("cgroup v2 unavailable")
	}
	if !peerAllowed(conn, uint32(os.Getuid()), group) || peerAllowed(conn, uint32(os.Getuid()+1), group) || peerAllowed(conn, uint32(os.Getuid()), group+"/different") {
		t.Fatal("peer identity/cgroup check failed", strconv.Itoa(os.Getuid()))
	}
}

func TestHelperRequiresActivationAndHostDetection(t *testing.T) {
	if err := ServeHelper(context.Background(), "unused"); err == nil {
		t.Fatal("ordinary invocation accepted")
	}
	host, err := hostCapacity("/system.slice/rillway.service")
	if err != nil || host == 0 {
		t.Fatal(host, err)
	}
	for _, group := range []string{"relative", "/../../etc"} {
		if _, err = hostCapacity(group); err == nil {
			t.Fatal(group)
		}
	}
}
