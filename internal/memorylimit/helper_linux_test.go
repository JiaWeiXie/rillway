package memorylimit

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
}

func (f *fakeSystem) run(_ context.Context, args ...string) ([]byte, error) {
	switch args[0] {
	case "show":
		return []byte(fmt.Sprintf("LoadState=loaded\nControlGroup=/system.slice/rillway.service\nMemoryCurrent=%d\nMemoryMax=%d\nMemoryHigh=%d\n", f.current, f.limit, f.high)), nil
	case "daemon-reload":
		return nil, nil
	case "set-property":
		if len(args) != 4 || args[1] != "rillway.service" {
			return nil, errors.New("unexpected command")
		}
		f.sets++
		if f.failures > 0 {
			f.failures--
			return nil, errors.New("private-command-output")
		}
		if f.fail {
			return nil, errors.New("private-command-output")
		}
		f.limit = parseBytes(strings.TrimPrefix(args[2], "MemoryMax="))
		f.high = parseBytes(strings.TrimPrefix(args[3], "MemoryHigh="))
		return nil, nil
	default:
		return nil, errors.New("unexpected command")
	}
}

func testController(t *testing.T) (*controller, *fakeSystem) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := config.Save(path, config.Default(dir)); err != nil {
		t.Fatal(err)
	}
	f := &fakeSystem{limit: 512 * MiB, high: 384 * MiB, current: 32 * MiB}
	c := &controller{configPath: path, stateDir: dir, command: f.run, capacity: func(string) (uint64, error) { return 4 * GiB, nil }}
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
	for _, req := range []Request{{Mode: "GiB", Value: "0.1", Revision: old}, {Mode: "percent", Value: "91", Revision: old}, {Mode: "MiB", Value: "600", Revision: "stale"}} {
		if _, err = c.apply(ctx, req); err == nil || f.sets != 0 {
			t.Fatal("invalid or stale write mutated service", err, f.sets)
		}
	}
	s, err = c.apply(ctx, Request{Mode: "percent", Value: "25", Revision: old})
	if err != nil || f.limit != GiB || f.high != 768*MiB || s.Mode != "percent" || s.Value != "25" || s.GoLimitBytes != 704*MiB || s.Revision == old {
		t.Fatal(s, err, f)
	}
	if _, err = c.apply(ctx, Request{Mode: "GiB", Value: "0.5", Revision: old}); !errors.Is(err, ErrConflict) {
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
	if _, err = c.apply(ctx, Request{Mode: "GiB", Value: "0.5", Revision: s.Revision}); err == nil || !strings.Contains(err.Error(), "unchanged") || f.limit != GiB || f.high != 768*MiB || strings.Contains(err.Error(), "private") {
		t.Fatal(err, f)
	}
	f.current = 900 * MiB
	s, err = c.status(ctx)
	if err != nil || s.MinimumBytes != 1125*MiB {
		t.Fatal(s, err)
	}
	if _, err = c.apply(ctx, Request{Mode: "MiB", Value: "1024", Revision: s.Revision}); err == nil {
		t.Fatal("current usage headroom ignored")
	}
	c.save = save
	f.fail = true
	if _, err = c.apply(ctx, Request{Mode: "GiB", Value: "1.5", Revision: s.Revision}); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("command diagnostic leaked", err)
	}
}

func TestControllerPropertyFailureRestoresAtomicState(t *testing.T) {
	c, f := testController(t)
	ctx := context.Background()
	s, _ := c.status(ctx)
	s, err := c.apply(ctx, Request{Mode: "MiB", Value: "600", Revision: s.Revision})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(c.stateDir, memoryFilename))
	if err != nil {
		t.Fatal(err)
	}
	f.failures = 1
	if _, err = c.apply(ctx, Request{Mode: "GiB", Value: "1", Revision: s.Revision}); err == nil || !strings.Contains(err.Error(), "restored") {
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
	if _, err = c.apply(context.Background(), Request{Mode: "MiB", Value: "512", Revision: s.Revision}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	after, err := c.status(context.Background())
	if err != nil || after.MinimumBytes != GiB {
		t.Fatal(after, err)
	}
	if _, err = c.apply(context.Background(), Request{Mode: "MiB", Value: "512", Revision: after.Revision}); err == nil {
		t.Fatal("embedded VPN minimum ignored")
	}
}

type controlBuffers struct {
	io.Reader
	io.Writer
}

func TestControllerStrictBoundedProtocol(t *testing.T) {
	c, f := testController(t)
	for _, input := range []string{"{}\n", "{\"action\":\"reboot\"}\n", "{\"action\":\"status\",\"command\":\"bad\"}\n", "{\"action\":\"status\"} {}\n", "{\"action\":\"status\"}", strings.Repeat(" ", 2048) + "{}\n"} {
		var output bytes.Buffer
		handleControl(context.Background(), controlBuffers{strings.NewReader(input), &output}, c)
		var reply wireResponse
		if err := json.Unmarshal(output.Bytes(), &reply); err != nil || reply.Error != "Invalid memory control request." || f.sets != 0 {
			t.Fatal(input, output.String(), err)
		}
	}
	var output bytes.Buffer
	handleControl(context.Background(), controlBuffers{strings.NewReader("{\"action\":\"status\"}\n"), &output}, c)
	var reply wireResponse
	if err := json.Unmarshal(output.Bytes(), &reply); err != nil || !reply.Status.Supported || reply.Error != "" {
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
