package app

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"rillway/internal/access"
	"rillway/internal/config"
	"rillway/internal/control"
	"rillway/internal/proxy"
	"strings"
	"testing"
	"time"
)

func TestRuntimeBlockSourceTransactionAndStreamSurvival(t *testing.T) {
	dir := t.TempDir()
	c := config.Default(dir)
	path := filepath.Join(dir, "config.json")
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	r, err := New(t.Context(), path, c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	if _, _, err := r.BlockSource(t.Context(), 1, "127.0.0.1"); err == nil || err.Error() != "Proxy source control is unavailable." {
		t.Fatalf("expected unavailable proxy error, got: %v", err)
	}
	initialPolicy, err := config.CompileSourceAccess(c.SourceAccess)
	if err != nil {
		t.Fatal(err)
	}
	p, err := proxy.New(r.Engine, c.Security, initialPolicy, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	r.attachProxy(p)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = target.Close() }()
	go func() {
		for {
			conn, err := target.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				reader := bufio.NewReader(c)
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					if _, err := c.Write([]byte("ECHO:" + line)); err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	server := httptest.NewUnstartedServer(p.HTTPHandler())
	server.Listener = p.GuardListener(l)
	server.Start()
	defer server.Close()
	connectConn, err := net.DialTimeout("tcp", l.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connectConn.Close() }()
	connectReq := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target.Addr().String(), target.Addr().String())
	if _, err := connectConn.Write([]byte(connectReq)); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(connectConn), nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("connect failed: %v %v", resp, err)
	}
	next := r.Config()
	next.Rules = append(next.Rules, config.Rule{ID: "extra-domain", Domains: []string{"example.org"}, Outbound: "direct"})
	if err := r.Apply(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	if _, err := connectConn.Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	echoReader := bufio.NewReader(connectConn)
	reply, err := echoReader.ReadString('\n')
	if err != nil || reply != "ECHO:ping\n" {
		t.Fatalf("stream interrupted by regular update: %q %v", reply, err)
	}
	if _, _, err := r.BlockSource(t.Context(), 1, "127.0.0.1"); err != config.ErrConflict {
		t.Fatalf("stale revision accepted: %v", err)
	}
	if _, _, err := r.BlockSource(t.Context(), r.Config().Revision, "127.0.0.1/32"); err == nil {
		t.Fatal("cidr allowed as block target")
	}
	secondListener, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Fatal(err)
	}
	other := httptest.NewUnstartedServer(p.HTTPHandler())
	other.Listener = p.GuardListener(secondListener)
	other.Start()
	defer other.Close()
	otherConn, err := net.DialTimeout("tcp", secondListener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = otherConn.Close() }()
	if _, err := fmt.Fprint(otherConn, connectReq); err != nil {
		t.Fatal(err)
	}
	if resp, err := http.ReadResponse(bufio.NewReader(otherConn), nil); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("second source CONNECT: %v %v", resp, err)
	}
	echo := func(conn net.Conn, text string) {
		t.Helper()
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		if _, err := fmt.Fprintln(conn, text); err != nil {
			t.Fatal(err)
		}
		if reply, err := bufio.NewReader(conn).ReadString('\n'); err != nil || reply != "ECHO:"+text+"\n" {
			t.Fatalf("held stream: %q %v", reply, err)
		}
		_ = conn.SetDeadline(time.Time{})
	}
	management := httptest.NewUnstartedServer(control.New(r, "test-token"))
	managementPolicy, err := access.CompileAllowlist(c.Security.AllowedClients)
	if err != nil {
		t.Fatal(err)
	}
	management.Listener = proxy.NewConnectionGuard(64, 16, managementPolicy.Decide).Wrap(management.Listener)
	management.StartTLS()
	defer management.Close()
	roots := x509.NewCertPool()
	roots.AddCert(management.Certificate())
	adminConn, err := tls.Dial("tcp", management.Listener.Addr().String(), &tls.Config{RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = adminConn.Close() }()
	readAdmin := func() {
		t.Helper()
		_ = adminConn.SetDeadline(time.Now().Add(time.Second))
		_, err := fmt.Fprint(adminConn, "GET /api/v1/config HTTP/1.1\r\nHost: "+management.Listener.Addr().String()+"\r\nAuthorization: Bearer test-token\r\n\r\n")
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(bufio.NewReader(adminConn), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("management inaccessible: %d", response.StatusCode)
		}
		if _, err := io.Copy(io.Discard, response.Body); err != nil {
			t.Fatal(err)
		}
	}
	readAdmin()
	badParent := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(badParent, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.path = filepath.Join(badParent, "config.json")
	before := r.Config()
	if _, _, err := r.BlockSource(t.Context(), before.Revision, "127.0.0.1"); err == nil {
		t.Fatal("block succeeded without persistence")
	}
	if err := r.Apply(t.Context(), before); err == nil {
		t.Fatal("update succeeded without persistence")
	}
	if got := r.Config(); !reflect.DeepEqual(got, before) {
		t.Fatal("failed save changed runtime configuration")
	}
	persisted, err := config.Load(path)
	if err != nil || !reflect.DeepEqual(persisted, before) {
		t.Fatalf("failed save changed persisted configuration: %v", err)
	}
	if got := r.providers["direct"]; got == nil {
		t.Fatal("failed save lost provider")
	}
	echo(connectConn, "after-failed-save")
	echo(otherConn, "other-after-failed-save")
	r.path = path

	updatedCfg, disconnected, err := r.BlockSource(t.Context(), r.Config().Revision, "127.0.0.1")
	if err != nil || disconnected != 1 {
		t.Fatalf("block failed: count=%d err=%v", disconnected, err)
	}
	if updatedCfg.Revision != 3 {
		t.Fatalf("revision not incremented: %d", updatedCfg.Revision)
	}
	_ = connectConn.SetReadDeadline(time.Now().Add(time.Second))
	var buf [1]byte
	if _, err := connectConn.Read(buf[:]); err == nil {
		t.Fatal("blocked stream remained open")
	}
	echo(otherConn, "unblocked-source-survives")
	readAdmin()
	rejectedConn, err := net.DialTimeout("tcp", l.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rejectedConn.Close() }()
	_ = rejectedConn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := rejectedConn.Read(buf[:]); err == nil {
		t.Fatal("new connection admitted after block")
	}
	cfgAgain, countAgain, err := r.BlockSource(t.Context(), r.Config().Revision, "127.0.0.1")
	if err != nil || countAgain != 0 || cfgAgain.Revision != 3 {
		t.Fatalf("already denied block mutated revision or miscounted: %d %d %v", cfgAgain.Revision, countAgain, err)
	}
}

func TestBlockSourceInputRevisionAndDisabledRule(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "::1"} {
		t.Run(address, func(t *testing.T) {
			c := config.Default(t.TempDir())
			id := blockRuleID(address, c.SourceAccess.Rules)
			prefix := address + "/32"
			if address == "::1" {
				prefix = address + "/128"
			}
			c.SourceAccess.Rules = append(c.SourceAccess.Rules, config.SourceAccessRule{ID: id, Name: address, Action: "deny", CIDRs: []string{prefix}, Enabled: false})
			path := filepath.Join(t.TempDir(), "config.json")
			r, err := New(t.Context(), path, c)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = r.Close() })
			policy, err := config.CompileSourceAccess(c.SourceAccess)
			if err != nil {
				t.Fatal(err)
			}
			p, err := proxy.New(r.Engine, c.Security, policy, "")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = p.Close() })
			r.attachProxy(p)
			for _, invalid := range []string{"invalid", "192.0.2.1:80", "[::1]:80", "127.0.0.1/32"} {
				if _, _, err := r.BlockSource(t.Context(), 0, invalid); err != config.ErrConflict {
					t.Errorf("stale invalid target %q: %v", invalid, err)
				}
				if _, _, err := r.BlockSource(t.Context(), c.Revision, invalid); err == nil {
					t.Errorf("non-IP target %q accepted", invalid)
				}
			}
			invalidConfig := r.Config()
			invalidConfig.Revision = 0
			invalidConfig.SourceAccess.Rules[0].Action = "invalid"
			if err := r.Apply(t.Context(), invalidConfig); err != config.ErrConflict {
				t.Errorf("stale invalid config: %v", err)
			}
			got, count, err := r.BlockSource(t.Context(), c.Revision, address)
			if err != nil {
				t.Fatal(err)
			}
			if count != 0 || got.Revision != c.Revision+1 {
				t.Fatalf("block transaction: count=%d revision=%d", count, got.Revision)
			}
			compiled, err := config.CompileSourceAccess(got.SourceAccess)
			if err != nil {
				t.Fatal(err)
			}
			ip, err := access.ParsePeer(address)
			if err != nil {
				t.Fatal(err)
			}
			if d := compiled.Decide(ip); d.Allowed || d.RuleID != id+"-2" {
				t.Fatalf("host deny not appended with a unique ID: %+v", d)
			}
			if len(got.SourceAccess.Rules) != len(c.SourceAccess.Rules)+1 || !reflect.DeepEqual(got.SourceAccess.Rules[:len(c.SourceAccess.Rules)], c.SourceAccess.Rules) {
				t.Fatal("block overwrote existing source rule metadata")
			}
			persisted, err := config.Load(path)
			if err != nil || persisted.Revision != got.Revision {
				t.Fatalf("block not persisted: %+v %v", persisted, err)
			}
		})
	}
}

func TestSourceBlockAPISafeLocalizedErrors(t *testing.T) {
	c := config.Default(t.TempDir())
	r, err := New(t.Context(), filepath.Join(t.TempDir(), "config.json"), c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	h := control.New(r, "token")
	for _, locale := range []string{"en", "zh-Hant"} {
		body := fmt.Sprintf("{\"revision\":%d,\"address\":\"credential-fixture-do-not-echo\"}", c.Revision)
		req := httptest.NewRequest("POST", "/api/v1/source-clients/block", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer token")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept-Language", locale)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		var failure struct {
			Error  string
			Source string `json:"error_source"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil {
			t.Fatal(err)
		}
		if w.Code != 422 || strings.Contains(w.Body.String(), "credential-fixture-do-not-echo") {
			t.Fatalf("unsafe source error: %d %s", w.Code, w.Body.String())
		}
		if locale == "zh-Hant" && failure.Error == failure.Source {
			t.Fatalf("source error was not localized: %+v", failure)
		}
	}
}
