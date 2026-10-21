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
	"rillway/internal/routeinfo"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	warpVersionPattern   = regexp.MustCompile(`\b[0-9]{4}\.[0-9]+\.[0-9]+\.[0-9]+\b`)
	warpModeLinePattern  = regexp.MustCompile(`^(?:\([^()\r\n]+\)[ \t]+)?Mode:[ \t]*(.*)$`)
	warpProxyPortPattern = regexp.MustCompile(`(?i)^warpproxy[ \t]+on[ \t]+port[ \t]+([0-9]{1,5})$`)
)

type (
	commandRunner func(context.Context, string, ...string) ([]byte, error)
	warpMetadata  struct {
		version string
		mode    string
		account string
		hasGood bool
	}
	warpDynamic struct {
		state    string
		detail   string
		listener bool
	}
	flight[T any] struct {
		mu sync.Mutex
		in *flightCall[T]
	}
	flightCall[T any] struct {
		done  chan struct{}
		epoch uint64
		val   T
		err   error
	}
	warp struct {
		cfg         config.Outbound
		run         commandRunner
		now         func() time.Time
		dialContext func(context.Context, string, string) (net.Conn, error)
		mu          sync.RWMutex
		actionMu    sync.Mutex
		statusEpoch atomic.Uint64

		dynamicMu     sync.Mutex
		dynamicFlight flight[warpDynamic]
		cachedDynamic warpDynamic
		dynamicAt     time.Time
		dynamicEpoch  uint64

		metadataMu       sync.Mutex
		metadataFlight   flight[warpMetadata]
		cachedMetadata   warpMetadata
		lastGoodMetadata warpMetadata
		metadataAt       time.Time
		metadataEpoch    uint64

		verified   time.Time
		manualStop bool

		handshakeSem chan struct{}
		paceMu       sync.Mutex
		lastDial     time.Time
		burstTokens  float64
		burstCap     float64
		paceInterval time.Duration
		sleep        func(context.Context, time.Duration) error
	}
)

func (f *flight[T]) Do(epoch uint64, fn func() (T, error)) (T, error) {
	f.mu.Lock()
	if c := f.in; c != nil && c.epoch >= epoch {
		f.mu.Unlock()
		<-c.done
		return c.val, c.err
	}
	c := &flightCall[T]{done: make(chan struct{}), epoch: epoch}
	f.in = c
	f.mu.Unlock()

	c.val, c.err = fn()
	f.mu.Lock()
	if f.in == c {
		f.in = nil
	}
	close(c.done)
	f.mu.Unlock()

	return c.val, c.err
}

func newWARP(cfg config.Outbound) *warp {
	if cfg.WARPBinary == "" {
		cfg.WARPBinary = "warp-cli"
	}
	if cfg.ProxyAddress == "" {
		cfg.ProxyAddress = "127.0.0.1:40000"
	}
	return &warp{
		cfg: cfg,
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		},
		now:          time.Now,
		dialContext:  (&net.Dialer{Timeout: time.Second}).DialContext,
		handshakeSem: make(chan struct{}, 8),
		burstTokens:  4,
		burstCap:     4,
		paceInterval: 10 * time.Millisecond,
		sleep:        sleepContext,
	}
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

var (
	warpFixedBackoffs = []time.Duration{
		100 * time.Millisecond,
		250 * time.Millisecond,
		500 * time.Millisecond,
		850 * time.Millisecond,
		1200 * time.Millisecond,
		1300 * time.Millisecond,
	}
	warpFastFailoverBackoffs = []time.Duration{
		150 * time.Millisecond,
	}
)

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *warp) acquireHandshakeSlot(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if w.handshakeSem != nil {
		select {
		case w.handshakeSem <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	release := func() {
		if w.handshakeSem != nil {
			<-w.handshakeSem
		}
	}
	if w.paceInterval <= 0 {
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		return release, nil
	}
	sleepFn := w.sleep
	if sleepFn == nil {
		sleepFn = sleepContext
	}
	for {
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		w.paceMu.Lock()
		now := w.clock()
		if !w.lastDial.IsZero() && now.After(w.lastDial) {
			w.burstTokens += float64(now.Sub(w.lastDial)) / float64(w.paceInterval)
			if w.burstTokens > w.burstCap {
				w.burstTokens = w.burstCap
			}
			w.lastDial = now
		}
		if w.burstTokens >= 1 {
			if err := ctx.Err(); err != nil {
				w.paceMu.Unlock()
				release()
				return nil, err
			}
			w.burstTokens--
			w.lastDial = now
			w.paceMu.Unlock()
			return release, nil
		}
		wait := time.Duration((1 - w.burstTokens) * float64(w.paceInterval))
		if wait < time.Nanosecond {
			wait = time.Nanosecond
		}
		w.paceMu.Unlock()
		if err := sleepFn(ctx, wait); err != nil {
			release()
			return nil, err
		}
	}
}

func (w *warp) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network == "udp" || network == "udp4" || network == "udp6" {
		return nil, errors.New("WARP Local Proxy UDP is unsupported")
	}
	backoffs := warpFixedBackoffs
	if routeinfo.From(ctx).FastFailover {
		backoffs = warpFastFailoverBackoffs
	}
	for attempt := 0; ; attempt++ {
		w.mu.RLock()
		stopped := w.manualStop
		w.mu.RUnlock()
		if stopped {
			return nil, errors.New("WARP was manually stopped")
		}
		release, err := w.acquireHandshakeSlot(ctx)
		if err != nil {
			return nil, err
		}
		conn, err := dialSOCKS(ctx, w.cfg.ProxyAddress, network, address)
		release()
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !errors.Is(err, ErrWARPReset) || attempt >= len(backoffs) {
			return nil, err
		}
		sleepFn := w.sleep
		if sleepFn == nil {
			sleepFn = sleepContext
		}
		if sleepErr := sleepFn(ctx, backoffs[attempt]); sleepErr != nil {
			return nil, sleepErr
		}
	}
}

func (w *warp) clock() time.Time {
	if w.now != nil {
		return w.now()
	}
	return time.Now()
}

func (w *warp) InvalidateStatus() {
	// Both cache layers and their flights use the epoch captured by Status.
	// A late result from a prior epoch cannot satisfy a forced inspection.
	w.statusEpoch.Add(1)
}

func (w *warp) Status(ctx context.Context) Status {
	s := Status{ID: w.ID(), Type: "warp", State: "unavailable", PublicInternet: true}
	w.mu.RLock()
	s.VerifiedAt = w.verified
	stopped := w.manualStop
	w.mu.RUnlock()

	if stopped {
		s.State = "stopped"
		return s
	}

	epoch := w.statusEpoch.Load()
	dyn := w.getDynamic(ctx, epoch)
	meta := w.getMetadata(ctx, epoch)

	s.State = dyn.state
	s.Detail = dyn.detail
	s.Listener = dyn.listener

	s.Version = meta.version
	s.Mode = meta.mode
	s.Account = meta.account

	return s
}

func (w *warp) getDynamic(ctx context.Context, epoch uint64) warpDynamic {
	now := w.clock()
	w.dynamicMu.Lock()
	if w.dynamicEpoch == epoch && !w.dynamicAt.IsZero() && now.Sub(w.dynamicAt) < 30*time.Second {
		d := w.cachedDynamic
		w.dynamicMu.Unlock()
		return d
	}
	w.dynamicMu.Unlock()

	dyn, _ := w.dynamicFlight.Do(epoch, func() (warpDynamic, error) {
		now := w.clock()
		w.dynamicMu.Lock()
		if w.dynamicEpoch == epoch && !w.dynamicAt.IsZero() && now.Sub(w.dynamicAt) < 30*time.Second {
			d := w.cachedDynamic
			w.dynamicMu.Unlock()
			return d, nil
		}
		w.dynamicMu.Unlock()

		d := w.readDynamic(ctx)
		w.dynamicMu.Lock()
		if w.statusEpoch.Load() == epoch {
			w.cachedDynamic = d
			w.dynamicAt = w.clock()
			w.dynamicEpoch = epoch
		}
		w.dynamicMu.Unlock()
		return d, nil
	})
	return dyn
}

func (w *warp) readDynamic(ctx context.Context) warpDynamic {
	raw, err := w.cli(ctx, "status")
	if err != nil {
		return warpDynamic{
			state:  "unavailable",
			detail: err.Error(),
		}
	}
	var d warpDynamic
	lower := strings.ToLower(raw)
	if strings.Contains(lower, "disconnected") {
		d.state = "disconnected"
	} else if strings.Contains(lower, "connected") {
		d.state = "connected"
	} else {
		d.state = "not-ready"
	}
	dial := w.dialContext
	if dial == nil {
		dial = (&net.Dialer{Timeout: time.Second}).DialContext
	}
	conn, e := dial(ctx, "tcp", w.cfg.ProxyAddress)
	if e != nil {
		d.detail = "Local Proxy listener unavailable"
	} else {
		_ = conn.Close()
		d.listener = true
		d.detail = "Local Proxy listener reachable; end-to-end verification is separate"
	}
	return d
}

func (w *warp) getMetadata(ctx context.Context, epoch uint64) warpMetadata {
	now := w.clock()
	w.metadataMu.Lock()
	if w.metadataEpoch == epoch && !w.metadataAt.IsZero() && now.Sub(w.metadataAt) < 5*time.Minute {
		m := w.cachedMetadata
		w.metadataMu.Unlock()
		return m
	}
	w.metadataMu.Unlock()

	meta, _ := w.metadataFlight.Do(epoch, func() (warpMetadata, error) {
		now := w.clock()
		w.metadataMu.Lock()
		if w.metadataEpoch == epoch && !w.metadataAt.IsZero() && now.Sub(w.metadataAt) < 5*time.Minute {
			m := w.cachedMetadata
			w.metadataMu.Unlock()
			return m, nil
		}
		w.metadataMu.Unlock()

		m := w.readMetadata(ctx)
		w.metadataMu.Lock()
		if !m.hasGood && w.lastGoodMetadata.hasGood {
			m = w.lastGoodMetadata
		}
		if w.statusEpoch.Load() == epoch {
			if m.hasGood {
				w.lastGoodMetadata = m
			}
			w.cachedMetadata = m
			w.metadataAt = w.clock()
			w.metadataEpoch = epoch
		}
		w.metadataMu.Unlock()
		return m, nil
	})
	return meta
}

func (w *warp) readMetadata(ctx context.Context) warpMetadata {
	var m warpMetadata
	version, errV := w.cli(ctx, "--version")
	if errV == nil {
		m.version = warpVersionPattern.FindString(version)
	}
	settings, errS := w.cli(ctx, "settings")
	if errS == nil {
		m.mode = warpMode(settings)
	}
	reg, errR := w.cli(ctx, "registration", "show")
	if errR == nil {
		if warpUnlimited(reg) {
			m.account = "Unlimited"
		} else {
			m.account = "registered (Unlimited not confirmed)"
		}
	}
	m.hasGood = errV == nil && errS == nil && errR == nil
	return m
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
		field := warpModeLinePattern.FindStringSubmatch(strings.TrimSpace(line))
		if field == nil {
			continue
		}
		value := strings.TrimSpace(field[1])
		switch strings.ToLower(value) {
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
		// Official clients also report "WarpProxy on port 40000". Require the
		// entire value and a valid decimal port; mentions of proxy mode in other
		// fields or unrecognized suffixes do not establish the current mode.
		if match := warpProxyPortPattern.FindStringSubmatch(value); match != nil {
			port, err := strconv.Atoi(match[1])
			if err == nil && port >= 1 && port <= 65535 {
				return "proxy"
			}
		}
		return "unknown"
	}
	return "unknown"
}

func (w *warp) Action(ctx context.Context, action, value string) error {
	w.actionMu.Lock()
	defer w.actionMu.Unlock()
	defer w.InvalidateStatus()
	switch action {
	case "register":
		// Registration belongs to the official client, including any WARP+
		// subscription. Repeated requests must preserve that existing device.
		if _, err := w.cli(ctx, "registration", "show"); err == nil {
			return nil
		}
		_, err := w.cli(ctx, "registration", "new")
		if err != nil {
			// Another process may have registered the shared client meanwhile.
			if _, existing := w.cli(ctx, "registration", "show"); existing == nil {
				return nil
			}
		}
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
