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
	"rillway/internal/platform"
	"rillway/internal/proxy"
	"strings"
	"sync"
	"time"
)

// Serve opens every listener before publishing readiness, and tears down all
// listeners if any requested address cannot be bound.
func Serve(ctx context.Context, path string, c config.Config, ready func(string)) error {
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
	p, err := proxy.New(r.Engine, c.Security, password)
	if err != nil {
		return err
	}
	defer func() { _ = p.Close() }()
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
		l       net.Listener
		handler http.Handler
		tls     bool
		socks   bool
	}
	var jobs []job
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
		addr  string
		h     http.Handler
		tls   bool
		socks bool
	}{
		{c.Listeners.HTTP, p.HTTPHandler(), false, false},
		{c.Listeners.SOCKS5, nil, false, true},
		{c.Listeners.Admin, acl(control.New(r, token)), true, false},
		{c.Listeners.PAC, acl(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Method != "GET" || req.URL.Path != "/proxy.pac" {
				http.NotFound(w, req)
				return
			}
			body, e := platform.PAC(r.Config().PAC)
			if e != nil {
				http.Error(w, "invalid PAC", 500)
				return
			}
			w.Header().Set("Content-Type", "application/x-ns-proxy-autoconfig")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write([]byte(body))
		})), false, false},
	} {
		if item.addr == "" {
			continue
		}
		l, e := bind(item.addr)
		if e != nil {
			return e
		}
		jobs = append(jobs, job{l, item.h, item.tls, item.socks})
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
		server := &http.Server{Handler: j.handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 << 10, BaseContext: func(net.Listener) context.Context { return ctx }}
		servers = append(servers, server)
		l := j.l
		if j.tls {
			l = tls.NewListener(l, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
		}
		wg.Add(1)
		go func() { defer wg.Done(); failures <- server.Serve(l) }()
	}
	if ready != nil {
		ready(fmt.Sprintf("Web UI: https://%s\nPAC: http://%s/proxy.pac\nTLS SHA-256: %s\nAdmin token file: %s", c.Listeners.Admin, c.Listeners.PAC, fp, c.Security.AdminTokenFile))
	}
	var serveErr error
	select {
	case <-ctx.Done():
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
