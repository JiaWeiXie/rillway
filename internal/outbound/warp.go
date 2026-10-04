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
		message := "官方 WARP client 操作失敗，請確認 daemon 已安裝並可由目前帳號使用。"
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			message = "找不到 warp-cli。請先安裝官方 Cloudflare WARP，或設定正確的執行檔路徑。"
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			message = "官方 WARP client 回應逾時，請檢查 warp-svc 是否正常執行。"
			err = errors.Join(err, ctx.Err())
		}
		return "", config.PublicError{Message: message, Err: err}
	}
	if len(b) > 65536 {
		return "", config.PublicError{Message: "官方 WARP client 回應超過大小限制。"}
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
		return warpFailure("WARP 裝置註冊失敗。請確認官方 daemon 可用及裝置是否已註冊。", err)
	case "license":
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 512 || strings.ContainsAny(value, "\r\n\x00") {
			return config.PublicError{Message: "WARP+ 授權碼格式不正確，請重新貼上不含換行的授權碼。"}
		}
		_, err := w.cli(ctx, "registration", "license", value)
		return warpFailure("WARP+ 授權碼未成功套用。請確認裝置已註冊，並使用有效的 Unlimited 訂閱授權碼。", err)
	case "connect":
		// Validate before changing anything. Only a loopback local SOCKS listener is
		// supported; never fall back to full-device tunnel mode.
		host, port, err := net.SplitHostPort(w.cfg.ProxyAddress)
		if err != nil {
			return config.PublicError{Message: "WARP Proxy 位址格式不正確，請使用 127.0.0.1:連接埠。", Err: err}
		}
		ip := net.ParseIP(host)
		n, e := strconv.Atoi(port)
		if ip == nil || !ip.Equal(net.ParseIP("127.0.0.1")) || e != nil || n < 1 || n > 65535 {
			return config.PublicError{Message: "WARP Local Proxy 必須使用 127.0.0.1，連接埠範圍為 1–65535。"}
		}
		if _, err = w.cli(ctx, "mode", "proxy"); err != nil {
			return warpFailure("無法啟用 WARP 的 Local Proxy 模式。請確認 client 版本及帳號支援 proxy 模式；Rillway 未切換為全機 tunnel。", err)
		}
		if _, err = w.cli(ctx, "proxy", "port", port); err != nil {
			return warpFailure("WARP 已切換為 Local Proxy 模式，但無法設定代理連接埠，尚未送出連線指令。", err)
		}
		if _, err = w.cli(ctx, "connect"); err != nil {
			return warpFailure("WARP 連線指令失敗。請檢查官方 daemon、裝置註冊及網路狀態。", err)
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
		return warpFailure("Rillway 已停止使用 WARP，但官方 daemon 未確認斷線。請檢查 WARP client 狀態。", err)
	case "verify":
		err := w.verify(ctx)
		var safe interface{ PublicMessage() string }
		if errors.As(err, &safe) {
			return err
		}
		return warpFailure("WARP 端到端驗證未完成，請確認 Local Proxy 已連線且能存取 Cloudflare。", err)
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
			return config.PublicError{Message: "WARP 驗證的 TLS 憑證無法通過檢查；此次連線不會標記為已驗證。", Err: err}
		}
		return config.PublicError{Message: "無法經 WARP Proxy 連到 Cloudflare 驗證網址；請檢查代理連接埠與 tunnel 連線。", Err: err}
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return config.PublicError{Message: fmt.Sprintf("Cloudflare 驗證網址回傳 HTTP %d，尚未確認 WARP 出口。", res.StatusCode)}
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 8193))
	if err != nil {
		return err
	}
	if len(b) > 8192 {
		return config.PublicError{Message: "Cloudflare 驗證回應超過大小限制，無法確認 WARP 出口。"}
	}
	active := false
	for _, line := range strings.Split(string(b), "\n") {
		if line == "warp=on" || line == "warp=plus" {
			active = true
		}
	}
	if !active {
		return config.PublicError{Message: "Proxy 可以連線，但 Cloudflare 回應未確認使用 WARP；不能視為 tunnel 驗證成功。"}
	}
	w.mu.Lock()
	w.verified = time.Now().UTC()
	w.mu.Unlock()
	return nil
}
