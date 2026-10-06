package memorylimit

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"rillway/internal/config"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type metadata struct {
	Request
	Bytes   uint64 `json:"bytes"`
	Updated int64  `json:"updated"`
}

const memoryFilename = "memory-limit.conf"

func formatMetadata(m metadata) []byte {
	b, _ := json.Marshal(m)
	format := func(n uint64) string {
		if n == 0 {
			return "infinity"
		}
		return strconv.FormatUint(n, 10)
	}
	return []byte("# rillway-memory " + string(b) + "\n[Service]\nMemoryMax=" + format(m.Bytes) + "\nMemoryHigh=" + format(m.Bytes*3/4) + "\n")
}

type controller struct {
	configPath string
	stateDir   string
	command    func(context.Context, ...string) ([]byte, error)
	capacity   func(string) (uint64, error)
	save       func(metadata) error
	restore    func([]byte) error
}

func systemctl(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "/usr/bin/systemctl", args...).Output()
}

func parseBytes(s string) uint64 {
	n, _ := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if s == "infinity" || n == ^uint64(0) {
		return 0
	}
	return n
}

func (c *controller) properties(ctx context.Context) (map[string]string, error) {
	b, err := c.command(ctx, "show", "rillway.service", "--property=LoadState,ControlGroup,MemoryCurrent,MemoryMax,MemoryHigh")
	if err != nil {
		return nil, messageError("Could not read the installed service memory limits.")
	}
	p := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			p[k] = v
		}
	}
	if p["LoadState"] != "loaded" || p["ControlGroup"] == "" {
		return nil, messageError("Memory control requires a running installed Linux service.")
	}
	return p, nil
}

func (c *controller) status(ctx context.Context) (Status, error) {
	p, err := c.properties(ctx)
	if err != nil {
		return Status{}, err
	}
	host, err := c.capacity(p["ControlGroup"])
	if err != nil || host == 0 {
		return Status{}, messageError("Could not detect the host memory capacity.")
	}
	cfg, err := config.Load(c.configPath)
	if err != nil {
		return Status{}, messageError("Could not validate the installed service configuration.")
	}
	current, limit, high := parseBytes(p["MemoryCurrent"]), parseBytes(p["MemoryMax"]), parseBytes(p["MemoryHigh"])
	minimum, reason := SafeMinimum(cfg, current)
	s := Status{Supported: true, HostBytes: host, MaximumBytes: Maximum(host), MinimumBytes: minimum, CurrentBytes: current, LimitBytes: limit, HighBytes: high, MinimumReason: reason, Mode: "MiB"}
	value := limit
	if value == 0 {
		value = 512 * MiB
		if minimum > value {
			value = minimum
		}
	}
	s.Value = strconv.FormatFloat(float64(value)/float64(MiB), 'f', 3, 64)
	m := metadata{}
	if b, readErr := os.ReadFile(filepath.Join(c.stateDir, memoryFilename)); readErr == nil {
		line, _, _ := bytes.Cut(b, []byte("\n"))
		if json.Unmarshal(bytes.TrimPrefix(line, []byte("# rillway-memory ")), &m) == nil && m.Bytes == limit && limit != 0 {
			s.Mode, s.Value = m.Mode, m.Value
			s.GoLimitBytes = GoBudget(limit)
		}
	}
	identity, _ := json.Marshal([]any{limit, high, host, cfg.Revision, m})
	digest := sha256.Sum256(identity)
	s.Revision = hex.EncodeToString(digest[:])
	if s.MinimumBytes > s.MaximumBytes {
		s.Supported = false
		s.Reason = "Host capacity is below the service safety minimum."
	}
	return s, nil
}

func (c *controller) apply(ctx context.Context, req Request) (Status, error) {
	s, err := c.status(ctx)
	if err != nil {
		return s, err
	}
	if !s.Supported {
		return s, messageError(s.Reason)
	}
	if req.Revision == "" || req.Revision != s.Revision {
		return s, ErrConflict
	}
	limit, err := Calculate(req, s)
	if err != nil {
		return s, err
	}
	high := limit * 3 / 4
	previous, readErr := os.ReadFile(filepath.Join(c.stateDir, memoryFilename))
	if readErr != nil && !os.IsNotExist(readErr) {
		return s, messageError("Could not read saved memory settings. Your current limit is unchanged.")
	}
	set := func(max, soft uint64) error {
		format := func(n uint64) string {
			if n == 0 {
				return "infinity"
			}
			return strconv.FormatUint(n, 10)
		}
		_, err := c.command(ctx, "set-property", "rillway.service", "MemoryMax="+format(max), "MemoryHigh="+format(soft))
		return err
	}
	if err = c.save(metadata{Request: req, Bytes: limit, Updated: time.Now().UnixNano()}); err != nil {
		return s, messageError("Could not save memory settings. Your current limit is unchanged.")
	}
	// The dedicated final drop-in survives other existing unit drop-ins, unlike
	// systemctl's earlier 50-MemoryMax.conf. Persist before changing the live cap.
	_, err = c.command(ctx, "daemon-reload")
	if err == nil {
		err = set(limit, high)
	}
	if err != nil {
		restoreErr := c.restore(previous)
		_, reloadErr := c.command(ctx, "daemon-reload")
		rollbackErr := set(s.LimitBytes, s.HighBytes)
		if restoreErr != nil || reloadErr != nil || rollbackErr != nil {
			return s, messageError("Memory settings could not be saved or restored. Read current limits before retrying.")
		}
		return s, messageError("Could not apply memory settings. The previous settings were restored.")
	}
	after, err := c.status(ctx)
	if err != nil || after.LimitBytes != limit || after.HighBytes != high {
		return after, messageError("Memory verification failed. Read current limits before retrying.")
	}
	return after, nil
}

// Host capacity is physical RAM capped by parent cgroups, excluding the
// service's own current memory.max. Do not parse another process's mount paths.
func hostCapacity(group string) (uint64, error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	var host uint64
	for _, line := range strings.Split(string(b), "\n") {
		parts := strings.Fields(line)
		if len(parts) >= 2 && parts[0] == "MemTotal:" {
			host = parseBytes(parts[1]) * 1024
			break
		}
	}
	if !strings.HasPrefix(group, "/") || strings.Contains(group, "..") {
		return 0, messageError("invalid service cgroup")
	}
	parent := filepath.Dir(filepath.Clean(group))
	for {
		path := filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(parent, "/"), "memory.max")
		if data, e := os.ReadFile(path); e == nil {
			if cap := parseBytes(strings.TrimSpace(string(data))); cap > 0 && (host == 0 || cap < host) {
				host = cap
			}
		}
		if parent == "/" {
			break
		}
		parent = filepath.Dir(parent)
	}
	return host, nil
}

func saveMetadata(m metadata) error {
	return writeMemoryState(formatMetadata(m))
}

func writeMemoryState(b []byte) error {
	info, err := os.Lstat(StateDirectory)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || stat.Uid != 0 || info.Mode().Perm()&0o022 != 0 {
		return messageError("unsafe resource state directory")
	}
	if b == nil {
		err = os.Remove(filepath.Join(StateDirectory, memoryFilename))
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	f, err := os.CreateTemp(StateDirectory, ".memory-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = f.Chmod(0o644)
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp, filepath.Join(StateDirectory, memoryFilename))
}

func peerAllowed(conn *net.UnixConn, uid uint32, group string) bool {
	raw, err := conn.SyscallConn()
	if err != nil {
		return false
	}
	var cred *syscall.Ucred
	var credentialErr error
	if err = raw.Control(func(fd uintptr) {
		cred, credentialErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || credentialErr != nil || cred == nil {
		return false
	}
	if cred.Uid == 0 {
		return true
	}
	if cred.Uid != uid {
		return false
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", cred.Pid))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if line == "0::"+group {
			return true
		}
	}
	return false
}

func handleControl(ctx context.Context, conn io.ReadWriter, c *controller) {
	var req wireRequest
	data, readErr := bufio.NewReader(io.LimitReader(conn, 2049)).ReadBytes('\n')
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	reply := wireResponse{}
	err := decoder.Decode(&req)
	if err == nil {
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			err = messageError("trailing input")
		}
	}
	if readErr != nil || len(data) > 2048 || err != nil {
		reply.Error = "Invalid memory control request."
	} else {
		var err error
		switch {
		case req.Action == "status" && req.Limit == nil:
			reply.Status, err = c.status(ctx)
		case req.Action == "apply" && req.Limit != nil:
			reply.Status, err = c.apply(ctx, *req.Limit)
		default:
			err = messageError("Invalid memory control request.")
		}
		if err != nil {
			reply.Error = err.Error()
			reply.Conflict = errors.Is(err, ErrConflict)
		}
	}
	_ = json.NewEncoder(conn).Encode(reply)
}

func ServeHelper(ctx context.Context, configPath string) error {
	if os.Geteuid() != 0 || os.Getenv("LISTEN_PID") != strconv.Itoa(os.Getpid()) || os.Getenv("LISTEN_FDS") != "1" {
		return messageError("Memory control requires systemd socket activation.")
	}
	f := os.NewFile(3, "memory-control")
	l, err := net.FileListener(f)
	_ = f.Close()
	if err != nil {
		return messageError("Memory control socket is unavailable.")
	}
	defer func() { _ = l.Close() }()
	unix, ok := l.(*net.UnixListener)
	if !ok || unix.Addr().String() != SocketPath {
		return messageError("Memory control socket is unavailable.")
	}
	account, err := user.Lookup("rillway")
	if err != nil {
		return messageError("Memory control service account is unavailable.")
	}
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil {
		return messageError("Memory control service account is unavailable.")
	}
	c := &controller{configPath: configPath, stateDir: StateDirectory, command: systemctl, capacity: hostCapacity, save: saveMetadata, restore: writeMemoryState}
	stop := context.AfterFunc(ctx, func() { _ = unix.Close() })
	defer stop()
	for {
		_ = unix.SetDeadline(time.Now().Add(30 * time.Second))
		conn, err := unix.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return messageError("Memory control socket stopped.")
		}
		p, err := c.properties(ctx)
		if err == nil && peerAllowed(conn, uint32(uid), p["ControlGroup"]) {
			_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
			handleControl(ctx, conn, c)
		}
		_ = conn.Close()
	}
}
