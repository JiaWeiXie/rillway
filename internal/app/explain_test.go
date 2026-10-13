package app

import (
	"bytes"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"rillway/internal/config"
	"rillway/internal/control"
	"testing"
)

func TestRuntimeExplainThroughClientUsesCurrentConfigWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	c := config.Default(dir)
	c.Outbounds = []config.Outbound{
		{ID: "direct", Type: "direct", Enabled: true, PublicInternet: true},
		{ID: "direct-extra", Type: "direct", Enabled: true, PublicInternet: true},
	}
	c.Adaptive.Candidates = []string{"direct"}
	c.Rules = []config.Rule{{
		ID: "metadata-rule", Domains: []string{"example.com"},
		Sources: []string{"192.0.2.0/24"}, Inbounds: []string{"socks5"}, Ports: []string{"8000-8100"},
		Outbound: "direct-extra", Family: "ipv4",
	}}
	path := filepath.Join(dir, "config.json")
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	r, err := New(t.Context(), path, c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	server := httptest.NewServer(control.New(r, "test-token"))
	defer server.Close()
	client, err := control.NewClient(server.URL, "test-token", "")
	if err != nil {
		t.Fatal(err)
	}

	req := config.ExplainRequest{Host: "EXAMPLE.COM.", Port: "8050", Source: "192.0.2.7", Inbound: "socks5", Family: "ipv6"}
	for _, tc := range []struct {
		name             string
		req              config.ExplainRequest
		rule, id, family string
	}{
		{"matching metadata", req, "metadata-rule", "direct-extra", "ipv4"},
		{"unknown metadata", config.ExplainRequest{Host: "example.com", Port: "8050", Family: "ipv6"}, "default", "direct", "ipv6"},
		{"different inbound", config.ExplainRequest{Host: "example.com", Port: "8050", Source: "192.0.2.7", Inbound: "http"}, "default", "direct", "auto"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			beforeCfg := r.Config()
			got, err := client.Explain(t.Context(), tc.req)
			if err != nil || got.Blocked || got.RuleID != tc.rule || got.TargetID != tc.id || got.ResolvedID != tc.id || got.Family != tc.family {
				t.Fatalf("runtime preview = %+v, %v", got, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) || !reflect.DeepEqual(beforeCfg, r.Config()) || len(r.Engine.Snapshot().Flows) != 0 || len(r.Engine.Snapshot().Destinations) != 0 {
				t.Fatal("preview mutated runtime, persisted config or observations", err)
			}
		})
	}

	next := r.Config()
	next.Rules[0].Outbound = "direct"
	next.Rules[0].Family = "auto"
	if err := r.Apply(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	got, err := client.Explain(t.Context(), req)
	if err != nil || got.RuleID != "metadata-rule" || got.ResolvedID != "direct" || got.Family != "ipv6" || r.Config().Revision != 2 {
		t.Fatalf("preview after Apply = %+v, %v", got, err)
	}
	disk, err := config.Load(path)
	if err != nil || disk.Revision != 2 || disk.Rules[0].Outbound != "direct" {
		t.Fatalf("applied config = %+v, %v", disk, err)
	}
	if _, err := r.Explain(config.ExplainRequest{Host: "example.com", Port: "443", Source: "invalid"}); err == nil {
		t.Fatal("runtime swallowed input validation error")
	}
	_, err = client.Explain(t.Context(), config.ExplainRequest{Port: "443"})
	var apiErr *control.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 422 {
		t.Fatalf("runtime input error through API = %v", err)
	}
}
