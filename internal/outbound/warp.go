package outbound

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"rillway/internal/config"
	"strconv"
	"strings"
	"sync"
	"time"
)

var warpVersionPattern = regexp.MustCompile(`\b[0-9]{4}\.[0-9]+\.[0-9]+\.[0-9]+\b`)

type (
	commandRunner func(context.Context, string, ...string) ([]byte, error)
	warp          struct {
		cfg        config.Outbound
		run        commandRunner
		mu         sync.RWMutex
		actionMu   sync.Mutex
		statusMu   sync.Mutex
		cached     Status
		cachedAt   time.Time
		verified   time.Time
		manualStop bool
	}
)

func newWARP(cfg config.Outbound) *warp {
	if cfg.WARPBinary == "" {
		cfg.WARPBinary = "warp-cli"
	}
	if cfg.ProxyAddress == "" {
		cfg.ProxyAddress = "127.0.0.1:40000"
	}
	return &warp{cfg: cfg, run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}}
}
func (w *warp) ID() string   { return w.cfg.ID }
func (w *warp) Close() error { return nil }
func (w *warp) cli(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	b, err := w.run(ctx, w.cfg.WARPBinary, append([]string{"--accept-tos"}, args...)...)
	// Output can contain license keys, account identifiers and registration data.
	// Never return arbitrary CLI output to a caller; inspect it only locally.
	if err != nil {
		message := "The official WARP client could not complete the operation. Check that its daemon is installed and accessible to the current user."
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			message = "warp-cli was not found. Install the official Cloudflare WARP client or configure its executable path."
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			message = "The official WARP client timed out. Check that warp-svc is running."
			err = errors.Join(err, ctx.Err())
		}
		return "", config.PublicError{Message: message, Err: err}
	}
	if len(b) > 65536 {
		return "", config.PublicError{Message: "The official WARP client response exceeded the size limit."}
	}
	return string(b), nil
}

func (w *warp) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	w.mu.RLock()
	stopped := w.manualStop
	w.mu.RUnlock()
	if stopped {
		return nil, errors.New("WARP was manually stopped")
	}
	return dialSOCKS(ctx, w.cfg.ProxyAddress, network, address)
}

func (w *warp) Status(ctx context.Context) Status {
	w.statusMu.Lock()
	defer w.statusMu.Unlock()
	if !w.cachedAt.IsZero() && time.Since(w.cachedAt) < 5*time.Second {
		return w.cached
	}
	s := w.readStatus(ctx)
	w.cached, w.cachedAt = s, time.Now()
	return s
}

func (w *warp) readStatus(ctx context.Context) Status {
	s := Status{ID: w.ID(), Type: "warp", State: "unavailable", PublicInternet: true}
	w.mu.RLock()
	s.VerifiedAt = w.verified
	stopped := w.manualStop
	w.mu.RUnlock()
	if stopped {
		s.State = "stopped"
		return s
	}
	if version, err := w.cli(ctx, "--version"); err == nil {
		s.Version = warpVersionPattern.FindString(version)
	}
	if settings, err := w.cli(ctx, "settings"); err == nil {
		s.Mode = warpMode(settings)
	}
	raw, err := w.cli(ctx, "status")
	if err != nil {
		s.Detail = err.Error()
		return s
	}
	if strings.Contains(strings.ToLower(raw), "disconnected") {
		s.State = "disconnected"
	} else if strings.Contains(strings.ToLower(raw), "connected") {
		s.State = "connected"
	} else {
		s.State = "not-ready"
	}
	if registration, e := w.cli(ctx, "registration", "show"); e == nil {
		if warpUnlimited(registration) {
			s.Account = "Unlimited"
		} else {
			s.Account = "registered (Unlimited not confirmed)"
		}
	}
	d := net.Dialer{Timeout: time.Second}
	conn, e := d.DialContext(ctx, "tcp", w.cfg.ProxyAddress)
	if e != nil {
		s.Detail = "Local Proxy listener unavailable"
	} else {
		_ = conn.Close()
		s.Listener = true
		s.Detail = "Local Proxy listener reachable; end-to-end verification is separate"
	}
	return s
}

func warpUnlimited(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		_, value, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(value), "unlimited") {
			return true
		}
	}
	return false
}

func warpMode(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if _, value, ok := strings.Cut(line, "Mode:"); ok {
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "warpproxy", "proxy":
				return "proxy"
			case "warp":
				return "warp"
			case "tunnelonly", "tunnel_only":
				return "tunnel_only"
			case "warpdoh", "warp+doh":
				return "warp+doh"
			case "doh":
				return "doh"
			case "dot":
				return "dot"
			case "warp+dot", "warpdot":
				return "warp+dot"
			}
		}
	}
	return "unknown"
}

func (w *warp) Action(ctx context.Context, action, value string) error {
	w.actionMu.Lock()
	defer w.actionMu.Unlock()
	defer func() { w.statusMu.Lock(); w.cachedAt = time.Time{}; w.statusMu.Unlock() }()
	switch action {
	case "register":
		_, err := w.cli(ctx, "registration", "new")
		return warpFailure("WARP device registration failed. Check that the official daemon is available and whether the device is already registered.", err)
	case "license":
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 512 || strings.ContainsAny(value, "\r\n\x00") {
			return config.PublicError{Message: "The WARP+ license key format is invalid. Paste the key without line breaks."}
		}
		_, err := w.cli(ctx, "registration", "license", value)
		return warpFailure("The WARP+ license key could not be applied. Check that the device is registered and the key has a valid Unlimited subscription.", err)
	case "connect":
		// Validate before changing anything. Only a loopback local SOCKS listener is
		// supported; never fall back to full-device tunnel mode.
		host, port, err := net.SplitHostPort(w.cfg.ProxyAddress)
		if err != nil {
			return config.PublicError{Message: "The WARP Proxy address is invalid. Use 127.0.0.1:<port>.", Err: err}
		}
		ip := net.ParseIP(host)
		n, e := strconv.Atoi(port)
		if ip == nil || !ip.Equal(net.ParseIP("127.0.0.1")) || e != nil || n < 1 || n > 65535 {
			return config.PublicError{Message: "WARP Local Proxy must use 127.0.0.1 with a port from 1 to 65535."}
		}
		if _, err = w.cli(ctx, "mode", "proxy"); err != nil {
			return warpFailure("Could not enable WARP Local Proxy mode. Check that your client version and account support proxy mode. Rillway has not enabled a full-device tunnel.", err)
		}
		if _, err = w.cli(ctx, "proxy", "port", port); err != nil {
			return warpFailure("WARP switched to Local Proxy mode, but the proxy port could not be set. No connection command was sent.", err)
		}
		if _, err = w.cli(ctx, "connect"); err != nil {
			return warpFailure("The WARP connection command failed. Check the official daemon, device registration, and network status.", err)
		}
		w.mu.Lock()
		w.manualStop = false
		w.verified = time.Time{}
		w.mu.Unlock()
		return nil
	case "disconnect":
		// Stop new local connections even if the external daemon cannot be reached.
		w.mu.Lock()
		w.manualStop = true
		w.verified = time.Time{}
		w.mu.Unlock()
		_, err := w.cli(ctx, "disconnect")
		return warpFailure("Rillway stopped using WARP, but the official daemon did not confirm disconnection. Check the WARP client status.", err)
	case "verify":
		err := w.verify(ctx)
		var safe interface{ PublicMessage() string }
		if errors.As(err, &safe) {
			return err
		}
		return warpFailure("WARP end-to-end verification did not complete. Check that Local Proxy is connected and can reach Cloudflare.", err)
	case "version":
		_, err := w.cli(ctx, "--version")
		return err
	default:
		return fmt.Errorf("unknown WARP action %q", action)
	}
}

func warpFailure(message string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return config.PublicError{Message: message, Err: err}
}

func (w *warp) verify(ctx context.Context) error {
	tr := &http.Transport{DialContext: w.DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, DisableKeepAlives: true}
	defer tr.CloseIdleConnections()
	client := http.Client{Transport: tr, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.cloudflare.com/cdn-cgi/trace", nil)
	if err != nil {
		return err
	}
	res, err := client.Do(req)
	if err != nil {
		var certErr *tls.CertificateVerificationError
		var unknownCA x509.UnknownAuthorityError
		if errors.As(err, &certErr) || errors.As(err, &unknownCA) {
			return config.PublicError{Message: "TLS certificate validation failed during WARP verification. This connection will not be marked as verified.", Err: err}
		}
		return config.PublicError{Message: "Could not reach the Cloudflare verification endpoint through WARP Proxy. Check the proxy port and tunnel connection.", Err: err}
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return config.PublicError{Message: fmt.Sprintf("The Cloudflare verification endpoint returned HTTP %d. The WARP outbound has not been verified.", res.StatusCode)}
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 8193))
	if err != nil {
		return err
	}
	if len(b) > 8192 {
		return config.PublicError{Message: "The Cloudflare verification response exceeded the size limit. The WARP outbound could not be verified."}
	}
	active := false
	for _, line := range strings.Split(string(b), "\n") {
		if line == "warp=on" || line == "warp=plus" {
			active = true
		}
	}
	if !active {
		return config.PublicError{Message: "The proxy is reachable, but Cloudflare did not confirm WARP usage. Tunnel verification did not succeed."}
	}
	w.mu.Lock()
	w.verified = time.Now().UTC()
	w.mu.Unlock()
	return nil
}
