package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"rillway/internal/config"
	"rillway/internal/control"
	"rillway/internal/platform"
	"testing"
	"time"
)

func TestSourceACLAdmissionManagement(t *testing.T) {
	for _, proxyAllowed := range []bool{false, true} {
		t.Run(fmt.Sprintf("proxy-allow-%t", proxyAllowed), func(t *testing.T) {
			c := config.Default(t.TempDir())
			c.Outbounds = []config.Outbound{{ID: "direct", Type: "direct", Enabled: true, PublicInternet: true}}
			c.Rules, c.Adaptive.Candidates = nil, []string{"direct"}
			c.Listeners = config.Listeners{HTTP: freeAddress(t), Admin: freeAddress(t), PAC: freeAddress(t)}
			c.PAC.ProxyAddress = c.Listeners.HTTP
			c.SourceAccess.Rules = []config.SourceAccessRule{}
			c.Security.AllowedClients = []string{"192.0.2.1/32"}
			if proxyAllowed {
				c.SourceAccess.Rules = []config.SourceAccessRule{{ID: "fixture", Name: "Fixture", Action: "allow", CIDRs: []string{"127.0.0.1/32"}, Enabled: true}}
			} else {
				c.Security.AllowedClients = []string{"127.0.0.1/32"}
			}
			path := filepath.Join(t.TempDir(), "config.json")
			if err := config.Save(path, c); err != nil {
				t.Fatal(err)
			}
			token, _, err := platform.EnsureCredentials(c)
			if err != nil {
				t.Fatal(err)
			}
			api, err := control.NewClient("https://"+c.Listeners.Admin, token, c.Security.TLSCertFile)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			ready, done := make(chan struct{}), make(chan error, 1)
			go func() { done <- Serve(ctx, path, c, func(string) { close(ready) }); close(done) }()
			defer func() {
				cancel()
				select {
				case err := <-done:
					if err != nil {
						t.Error(err)
					}
				case <-time.After(5 * time.Second):
					t.Error("fixture shutdown timed out")
				}
			}()
			select {
			case <-ready:
			case err := <-done:
				t.Fatal("fixture startup", err)
			case <-time.After(5 * time.Second):
				t.Fatal("fixture startup timed out")
			}
			t.Run("admin", func(t *testing.T) {
				requestCtx, stop := context.WithTimeout(t.Context(), 2*time.Second)
				defer stop()
				_, err = api.Config(requestCtx) // Correct token and trusted fixture certificate.
				if (err == nil) == proxyAllowed || requestCtx.Err() != nil {
					t.Fatal("management ACL separation failed")
				}
			})
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
			defer origin.Close()
			proxyURL, _ := url.Parse("http://" + c.Listeners.HTTP)
			for _, endpoint := range []string{"pac", "proxy"} {
				t.Run(endpoint, func(t *testing.T) {
					transport := &http.Transport{DisableKeepAlives: true}
					address, allowed, status := "http://"+c.Listeners.PAC+"/proxy.pac", !proxyAllowed, http.StatusOK
					if endpoint == "proxy" {
						transport.Proxy, address, allowed, status = http.ProxyURL(proxyURL), origin.URL, proxyAllowed, http.StatusNoContent
					}
					client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
					response, err := client.Get(address)
					transport.CloseIdleConnections()
					if response != nil {
						_ = response.Body.Close()
					}
					if (err == nil) != allowed || allowed && response.StatusCode != status {
						t.Fatal(endpoint, "ACL separation failed")
					}
					if e, ok := err.(net.Error); ok && e.Timeout() {
						t.Fatal("ACL refusal timed out")
					}
				})
			}
		})
	}
}
