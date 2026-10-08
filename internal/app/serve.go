package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"rillway/internal/config"
	"rillway/internal/control"
	"rillway/internal/i18n"
	"rillway/internal/platform"
	"rillway/internal/proxy"
	"strings"
	"sync"
	"time"
)

var errRestart = errors.New("service restart requested")

type restartRequest struct{ active config.Config }

func (r restartRequest) Error() string { return errRestart.Error() }
func (r restartRequest) Unwrap() error { return errRestart }

type serverTimeouts struct {
	adminRead, adminWrite time.Duration
	pacRead, pacWrite     time.Duration
}

var defaultServerTimeouts = serverTimeouts{30 * time.Second, 2 * time.Minute, 10 * time.Second, 30 * time.Second}

// Serve restarts its listeners and runtime in the same low-privilege process.
// It needs no shell commands, sudo policy, or service-manager privileges.
func Serve(ctx context.Context, path string, c config.Config, ready func(string)) error {
	return serveWithTimeouts(ctx, path, c, ready, defaultServerTimeouts)
}

type serveAttempt func(context.Context, string, config.Config, func(string), serverTimeouts) error

func serveWithTimeouts(ctx context.Context, path string, c config.Config, ready func(string), timeouts serverTimeouts) error {
	return serveLoop(ctx, path, c, ready, timeouts, serveOnce)
}

func serveLoop(ctx context.Context, path string, c config.Config, ready func(string), timeouts serverTimeouts, attempt serveAttempt) error {
	current := c
	var fallback *config.Config
	for {
		attemptReady := func(message string) {
			fallback = nil
			if ready != nil {
				ready(message)
			}
		}
		err := attempt(ctx, path, current, attemptReady, timeouts)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, errRestart) {
			next, loadErr := config.Load(path)
			if loadErr != nil {
				return loadErr
			}
			previous := current
			var request restartRequest
			if errors.As(err, &request) {
				previous = request.active
			}
			fallback, current = &previous, next
			continue
		}
		if fallback != nil {
			current, fallback = *fallback, nil
			continue
		}
		return err
	}
}

// serveOnce opens every listener before publishing readiness, and tears down
// all listeners if any requested address cannot be bound.
func serveOnce(ctx context.Context, path string, c config.Config, ready func(string), timeouts serverTimeouts) error {
	token, fp, err := platform.EnsureCredentials(c)
	if err != nil {
		return err
	}
	password := ""
	if c.Security.ProxyPasswordFile != "" {
		b, e := os.ReadFile(c.Security.ProxyPasswordFile)
		if e != nil {
			return e
		}
		password = strings.TrimSpace(string(b))
		if password == "" {
			return errors.New("empty proxy password")
		}
	}
	r, err := New(ctx, path, c)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	r.restart = make(chan struct{}, 1)
	p, err := proxy.New(r.Engine, c.Security, password)
	if err != nil {
		return err
	}
	defer func() { _ = p.Close() }()
	r.Engine.SetDestinationPolicy(p.DestinationPolicy)
	// Set before any listener starts, so management requests never race it.
	r.admission = p.AdmissionRejections
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var listeners []net.Listener
	defer func() {
		for _, l := range listeners {
			_ = l.Close()
		}
	}()
	bind := func(addr string) (net.Listener, error) {
		l, e := net.Listen("tcp", addr)
		if e == nil {
			listeners = append(listeners, l)
		}
		return l, e
	}
	type job struct {
		l                         net.Listener
		handler                   http.Handler
		tls                       bool
		socks                     bool
		readTimeout, writeTimeout time.Duration
	}
	var jobs []job
	// Keep management/PAC capacity independent of saturated proxy traffic.
	controlGuard := proxy.NewConnectionGuard(64, 16, func(peer string) bool {
		return platform.ClientAllowed(peer, c.Security.AllowedClients)
	})
	acl := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if !platform.ClientAllowed(req.RemoteAddr, c.Security.AllowedClients) {
				http.Error(w, "source not allowed", http.StatusForbidden)
				return
			}
			h.ServeHTTP(w, req)
		})
	}
	for _, item := range []struct {
		addr                      string
		h                         http.Handler
		tls                       bool
		socks                     bool
		readTimeout, writeTimeout time.Duration
	}{
		{c.Listeners.HTTP, p.HTTPHandler(), false, false, 0, 0},
		{c.Listeners.SOCKS5, nil, false, true, 0, 0},
		{c.Listeners.Admin, acl(control.New(r, token)), true, false, timeouts.adminRead, timeouts.adminWrite},
		{c.Listeners.PAC, acl(pacHandler(r)), false, false, timeouts.pacRead, timeouts.pacWrite},
	} {
		if item.addr == "" {
			continue
		}
		l, e := bind(item.addr)
		if e != nil {
			return e
		}
		if item.socks || item.addr == c.Listeners.HTTP {
			l = p.GuardListener(l)
		} else {
			p.IgnoreListener(l)
			l = controlGuard.Wrap(l)
		}
		jobs = append(jobs, job{l, item.h, item.tls, item.socks, item.readTimeout, item.writeTimeout})
	}
	cert, err := tls.LoadX509KeyPair(c.Security.TLSCertFile, c.Security.TLSKeyFile)
	if err != nil {
		return err
	}
	failures := make(chan error, len(jobs))
	var wg sync.WaitGroup
	var servers []*http.Server
	for _, j := range jobs {
		if j.socks {
			wg.Add(1)
			go func() { defer wg.Done(); failures <- p.ServeSOCKS(ctx, j.l) }()
			continue
		}
		server := &http.Server{Handler: j.handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: j.readTimeout, WriteTimeout: j.writeTimeout, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 << 10, BaseContext: func(net.Listener) context.Context { return ctx }}
		servers = append(servers, server)
		l := j.l
		if j.tls {
			l = tls.NewListener(l, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
		}
		wg.Add(1)
		go func() { defer wg.Done(); failures <- server.Serve(l) }()
	}
	if ready != nil {
		ready(fmt.Sprintf(i18n.Message(i18n.FromContext(ctx), "Web UI: https://%s\nPAC: http://%s/proxy.pac\nTLS SHA-256: %s\nManagement token file: %s"), c.Listeners.Admin, c.Listeners.PAC, fp, c.Security.AdminTokenFile))
	}
	var serveErr error
	select {
	case <-ctx.Done():
	case <-r.restart:
		serveErr = restartRequest{active: r.Config()}
	case serveErr = <-failures:
	}
	cancel()
	_ = p.Close()
	stopCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	for _, s := range servers {
		if err := s.Shutdown(stopCtx); err != nil {
			_ = s.Close()
		}
	}
	for _, l := range listeners {
		_ = l.Close()
	}
	wg.Wait()
	if errors.Is(serveErr, http.ErrServerClosed) || errors.Is(serveErr, net.ErrClosed) {
		return nil
	}
	return serveErr
}

func pacHandler(r *Runtime) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet || req.URL.Path != "/proxy.pac" {
			http.NotFound(w, req)
			return
		}
		if site := req.Header.Get("Sec-Fetch-Site"); site != "" && site != "none" &&
			(req.Header.Get("Sec-Fetch-Mode") != "navigate" || req.Header.Get("Sec-Fetch-Dest") != "document") {
			http.Error(w, "PAC cannot be loaded by web pages", http.StatusForbidden)
			return
		}
		body, err := platform.PAC(r.Config().PAC)
		if err != nil {
			http.Error(w, "invalid PAC", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-ns-proxy-autoconfig")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write([]byte(body))
	})
}
