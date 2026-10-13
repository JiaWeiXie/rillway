package control

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http/httptest"
	"reflect"
	"rillway/internal/config"
	"rillway/internal/engine"
	"rillway/internal/outbound"
	"strings"
	"testing"
)

const explainPublicError = "Could not explain the route. Check the host, port, source and inbound values."

type previewBackend struct {
	*fakeBackend
	router             *engine.Engine
	failure            error
	requests           []config.ExplainRequest
	dials, statusCalls int
}

func (b *previewBackend) Explain(req config.ExplainRequest) (config.ExplainResult, error) {
	b.requests = append(b.requests, req)
	if b.failure != nil {
		return config.ExplainResult{}, b.failure
	}
	return b.router.Explain(req)
}

type previewProvider struct {
	backend *previewBackend
	id      string
}

func (p *previewProvider) ID() string { return p.id }
func (p *previewProvider) DialContext(context.Context, string, string) (net.Conn, error) {
	p.backend.dials++
	return nil, errors.New("provider-private-output")
}

func (p *previewProvider) Status(context.Context) outbound.Status {
	p.backend.statusCalls++
	return outbound.Status{ID: p.id, Detail: "provider-private-output"}
}
func (p *previewProvider) Close() error { return nil }

func newPreviewBackend(t *testing.T) *previewBackend {
	t.Helper()
	b := &previewBackend{fakeBackend: newBackend()}
	b.cfg = config.Default(t.TempDir())
	b.cfg.Outbounds[1].Enabled = true
	b.cfg.Rules = []config.Rule{{ID: "preview-rule", Domains: []string{"example.com"}, Outbound: "warp"}}
	b.cfg.OutboundGroups = []config.OutboundGroup{{ID: "preview-group", Type: "fallback", Enabled: true, Members: []string{"warp"}}}
	b.cfg.DefaultOutbound = "preview-group"
	if err := config.Validate(b.cfg); err != nil {
		t.Fatal(err)
	}
	providers := map[string]outbound.Provider{}
	for _, id := range []string{"direct", "warp"} {
		providers[id] = &previewProvider{backend: b, id: id}
	}
	b.router = engine.New(b.cfg, providers)
	t.Cleanup(func() { _ = b.router.Close() })
	return b
}

func TestExplainRouteAPIAuthValidationAndSafeResults(t *testing.T) {
	b := newPreviewBackend(t)
	const token = "private-management-token"
	h := New(b, token)
	before := b.Config()
	for _, tc := range []struct {
		name, body, bearer, origin string
		status                     int
	}{
		{"no bearer", `{"host":"example.com","port":"443"}`, "", "", 401},
		{"wrong bearer", `{"host":"example.com","port":"443"}`, "wrong", "", 401},
		{"cross origin", `{"host":"example.com","port":"443"}`, token, "http://evil.example", 403},
		{"empty host", `{"host":"","port":"443"}`, token, "", 422},
		{"invalid port", `{"host":"example.com","port":"65536"}`, token, "", 422},
		{"invalid source", `{"host":"example.com","port":"443","source":"invalid"}`, token, "", 422},
		{"invalid inbound", `{"host":"example.com","port":"443","inbound":"ftp"}`, token, "", 422},
		{"invalid family", `{"host":"example.com","port":"443","family":"tcp4"}`, token, "", 422},
		{"unknown field", `{"host":"example.com","port":"443","extra":true}`, token, "", 400},
		{"trailing JSON", `{"host":"example.com","port":"443"} {}`, token, "", 400},
		{"malformed JSON", `{`, token, "", 400},
		{"too large", `{"host":"` + strings.Repeat("a", 8192) + `","port":"443"}`, token, "", 413},
		{"valid rule", `{"host":"example.com","port":"443"}`, token, "", 200},
		{"private blocked", `{"host":"nas","port":"443"}`, token, "", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := len(b.requests)
			w := request(h, "POST", "/api/v1/route/explain", tc.body, tc.bearer, tc.origin)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
			if tc.status == 401 || tc.status == 403 || tc.status == 400 || tc.status == 413 {
				if len(b.requests) != calls {
					t.Fatal("rejected transport/auth request reached backend")
				}
			}
			if tc.status == 422 {
				var body struct{ Error, Source string }
				var fields map[string]string
				if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil {
					t.Fatal(err)
				}
				body.Error, body.Source = fields["error"], fields["error_source"]
				if body.Error != explainPublicError || body.Source != explainPublicError {
					t.Fatalf("unsafe/unstable error = %v", fields)
				}
			}
			if tc.status == 200 {
				var got config.ExplainResult
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if tc.name == "valid rule" && (got.RuleID != "preview-rule" || got.TargetID != "warp" || got.ResolvedID != "warp" || got.Blocked) {
					t.Fatalf("rule result = %+v", got)
				}
				if tc.name == "private blocked" && (!got.Private || !got.Blocked || got.Reason != "private_protected" || got.TargetID != "preview-group") {
					t.Fatalf("private result = %+v", got)
				}
			}
			for _, secret := range []string{token, "provider-private-output"} {
				if strings.Contains(w.Body.String(), secret) {
					t.Fatalf("response exposed %q", secret)
				}
			}
		})
	}
	if !reflect.DeepEqual(before, b.Config()) || b.dials != 0 || b.statusCalls != 0 || b.lastAction != "" {
		t.Fatal("preview mutated config or invoked providers")
	}
}

func TestExplainRouteHidesBackendErrors(t *testing.T) {
	b := newPreviewBackend(t)
	for _, failure := range []error{
		errors.New("provider-private-output private-management-token"),
		config.PublicError{Message: "provider-private-output private-management-token"},
	} {
		b.failure = failure
		w := request(New(b, "private-management-token"), "POST", "/api/v1/route/explain", `{"host":"example.com","port":"443"}`, "private-management-token", "")
		var fields map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil {
			t.Fatal(err)
		}
		if w.Code != 422 || fields["error"] != explainPublicError || fields["error_source"] != explainPublicError {
			t.Fatalf("unsafe error response = %d %v", w.Code, fields)
		}
	}
}

func TestClientExplainPreservesRequestResultAndErrors(t *testing.T) {
	b := newPreviewBackend(t)
	server := httptest.NewServer(New(b, "token"))
	defer server.Close()
	client, err := NewClient(server.URL, "token", "")
	if err != nil {
		t.Fatal(err)
	}
	req := config.ExplainRequest{Host: "example.com", Port: "443", Source: "192.0.2.7", Inbound: "socks5", Family: "ipv6"}
	got, err := client.Explain(t.Context(), req)
	if err != nil || got.RuleID != "preview-rule" || got.ResolvedID != "warp" || got.Family != "ipv6" || got.Blocked {
		t.Fatalf("client result = %+v, %v", got, err)
	}
	if len(b.requests) != 1 || !reflect.DeepEqual(b.requests[0], req) {
		t.Fatalf("backend requests = %+v", b.requests)
	}
	blocked, err := client.Explain(t.Context(), config.ExplainRequest{Host: "nas", Port: "443"})
	if err != nil || !blocked.Blocked || blocked.Reason != "private_protected" {
		t.Fatalf("blocked client result = %+v, %v", blocked, err)
	}
	_, err = client.Explain(t.Context(), config.ExplainRequest{Port: "443"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 422 || apiErr.Source != explainPublicError {
		t.Fatalf("client error = %v", err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	calls := len(b.requests)
	if _, err := client.Explain(cancelled, req); !errors.Is(err, context.Canceled) || len(b.requests) != calls {
		t.Fatalf("cancelled request = %v, calls = %d", err, len(b.requests))
	}
	badClient, err := NewClient(server.URL, "wrong", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = badClient.Explain(t.Context(), req); !errors.As(err, &apiErr) || apiErr.Status != 401 {
		t.Fatalf("unauthenticated client error = %v", err)
	}
}
