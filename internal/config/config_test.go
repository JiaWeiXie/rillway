package config

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"rillway/internal/access"
	"strings"
	"testing"
)

func TestRoundTripAndPermissions(t *testing.T) {
	c := Default(t.TempDir())
	p := filepath.Join(t.TempDir(), "config.json")
	if err := Save(p, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil || got.Revision != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatal(st.Mode())
	}
}

func TestInvalidConfig(t *testing.T) {
	for _, modify := range []func(*Config){
		func(c *Config) { c.Rules[0].Domains = []string{"github.com/evil"} },
		func(c *Config) { c.Outbounds = append(c.Outbounds, c.Outbounds[0]) },
		func(c *Config) { c.Adaptive.ProbesPerMinute = 13 },
		func(c *Config) { c.Rules[0].Outbound = "missing" },
		func(c *Config) { c.Listeners.Admin = c.Listeners.HTTP },
		func(c *Config) { c.Listeners.PAC = "0.0.0.0:17890" },
		func(c *Config) { c.Outbounds[1].Enabled = true; c.Outbounds[1].ProxyAddress = c.Listeners.SOCKS5 },
		func(c *Config) {
			c.Outbounds = append(c.Outbounds,
				Outbound{ID: "ts-a", Type: "tailscale", Enabled: true, StateDir: "/x/ts"},
				Outbound{ID: "ts-b", Type: "tailscale", Enabled: true, StateDir: "/x/ts/"})
		},
	} {
		c := Default(t.TempDir())
		modify(&c)
		if Validate(c) == nil {
			t.Fatal("accepted invalid config")
		}
	}
}

func TestListenAddressesOverlap(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want bool
	}{
		{"127.0.0.1:1", "127.0.0.1:1", true},
		{"0.0.0.0:1", "127.0.0.1:1", true},
		{"[::]:1", "127.0.0.1:1", true},
		{":1", "[::1]:1", true},
		{"localhost:1", "127.0.0.1:1", true},
		{"[::ffff:127.0.0.1]:1", "127.0.0.1:1", true},
		{"0.0.0.0:1", "[::1]:1", false},
		{"127.0.0.1:1", "127.0.0.1:2", false},
		{"127.0.0.1:1", "192.168.1.2:1", false},
		{"invalid", "127.0.0.1:1", false},
	} {
		t.Run(tt.a+"/"+tt.b, func(t *testing.T) {
			if got := ListenAddressesOverlap(tt.a, tt.b); got != tt.want {
				t.Fatalf("overlap = %v, want %v", got, tt.want)
			}
			if got := ListenAddressesOverlap(tt.b, tt.a); got != tt.want {
				t.Fatalf("reverse overlap = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDecodeRejectsUnknownAndTrailing(t *testing.T) {
	c := Default(t.TempDir())
	data, _ := json.Marshal(c)
	for _, b := range [][]byte{append(append([]byte{}, data...), []byte(" {}")...), append([]byte(`{"unexpected":1,`), data[1:]...)} {
		if _, err := Decode(b); err == nil {
			t.Fatal("accepted extra JSON")
		}
	}
}

func TestPACDefaultsAndLegacyConfigMigration(t *testing.T) {
	c := Default(t.TempDir())
	if len(c.PAC.BypassDomains) < 9 || len(c.PAC.BypassCIDRs) != 10 {
		t.Fatalf("missing researched PAC presets: %+v", c.PAC)
	}
	for _, entry := range append(append([]PACBypass{}, c.PAC.BypassDomains...), c.PAC.BypassCIDRs...) {
		if !entry.Enabled || entry.Preset == "" {
			t.Fatalf("default bypass lacks enabled preset: %+v", entry)
		}
	}

	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err = json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	pac := root["pac"].(map[string]any)
	pac["bypass_domains"] = []string{"local", "corp.example"}
	pac["bypass_cidrs"] = []string{"10.0.0.0/8"}
	legacy, _ := json.Marshal(root)
	got, err := Decode(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if got.PAC.BypassDomains[0].Preset != PACDomainMDNS || !got.PAC.BypassDomains[1].Enabled || got.PAC.BypassDomains[1].Preset != "" {
		t.Fatalf("legacy domains not migrated: %+v", got.PAC.BypassDomains)
	}
	if got.PAC.BypassCIDRs[0].Preset != PACCIDRPrivate10 || !got.PAC.BypassCIDRs[0].Enabled {
		t.Fatalf("legacy CIDR not migrated: %+v", got.PAC.BypassCIDRs)
	}
	if len(got.PAC.BypassDomains) != 10 || len(got.PAC.BypassCIDRs) != 10 {
		t.Fatalf("legacy config did not receive new safety presets: %+v", got.PAC)
	}
}

func TestPACBypassValidation(t *testing.T) {
	c := Default(t.TempDir())
	c.PAC.BypassDomains = append(c.PAC.BypassDomains, PACBypass{Value: "example.internal", Enabled: false, Note: "Office dashboard 🚀"})
	if err := Validate(c); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Config){
		"duplicate": func(c *Config) { c.PAC.BypassDomains = append(c.PAC.BypassDomains, PACBypass{Value: "LOCAL"}) },
		"preset":    func(c *Config) { c.PAC.BypassDomains[0].Preset = "unknown.preset" },
		"note":      func(c *Config) { c.PAC.BypassCIDRs[0].Note = strings.Repeat("x", 501) },
	} {
		t.Run(name, func(t *testing.T) {
			next := Default(t.TempDir())
			mutate(&next)
			if Validate(next) == nil {
				t.Fatal("accepted invalid PAC bypass")
			}
		})
	}
}

func FuzzDecode(f *testing.F) {
	seed, _ := json.Marshal(Default("state"))
	f.Add(seed)
	f.Fuzz(func(t *testing.T, b []byte) {
		c, err := Decode(b)
		if err == nil && Validate(c) != nil {
			t.Fatal("decoded invalid config")
		}
	})
}

func TestVersion1MigrationPreservesProxyAllowlist(t *testing.T) {
	v1JSON := `{
		"version": 1,
		"revision": 3,
		"listeners": {
			"http": "127.0.0.1:17890",
			"socks5": "127.0.0.1:17891",
			"admin": "127.0.0.1:17892",
			"pac": "127.0.0.1:17893"
		},
		"security": {
			"allowed_clients": ["192.168.1.0/24", "10.0.0.0/8"],
			"admin_token_file": "/tmp/admin.token",
			"tls_cert_file": "/tmp/admin.crt",
			"tls_key_file": "/tmp/admin.key"
		},
		"outbounds": [{"id": "direct", "type": "direct", "enabled": true, "public_internet": true}],
		"rules": [],
		"default_outbound": "direct",
		"adaptive": {"candidates": ["direct"], "window_seconds": 600, "min_samples": 3, "improvement_percent": 25, "cooldown_seconds": 600, "probes_per_minute": 12, "probe_concurrency": 2, "probe_timeout_seconds": 4},
		"pac": {"proxy_address": "127.0.0.1:17890"}
	}`

	got, err := Decode([]byte(v1JSON))
	if err != nil {
		t.Fatalf("failed to decode v1 config: %v", err)
	}
	if got.Version != 2 {
		t.Fatalf("expected in-memory version 2, got %d", got.Version)
	}
	if got.Revision != 3 {
		t.Fatalf("expected revision 3, got %d", got.Revision)
	}
	if len(got.SourceAccess.Rules) != 1 {
		t.Fatalf("expected 1 source access rule, got %d", len(got.SourceAccess.Rules))
	}
	rule := got.SourceAccess.Rules[0]
	if rule.ID != "local-clients" || rule.Name != "Local clients" || rule.Action != "allow" || !rule.Enabled || rule.Note != "" {
		t.Fatalf("unexpected migrated rule: %+v", rule)
	}
	expectedCIDRs := []string{"192.168.1.0/24", "10.0.0.0/8"}
	if !reflect.DeepEqual(rule.CIDRs, expectedCIDRs) {
		t.Fatalf("expected CIDRs %v, got %v", expectedCIDRs, rule.CIDRs)
	}
	if !reflect.DeepEqual(got.Security.AllowedClients, expectedCIDRs) {
		t.Fatalf("expected security allowed_clients %v, got %v", expectedCIDRs, got.Security.AllowedClients)
	}

	policy, err := CompileSourceAccess(got.SourceAccess)
	if err != nil {
		t.Fatalf("failed to compile source access: %v", err)
	}
	dec := policy.Decide(netip.MustParseAddr("192.168.1.50"))
	if !dec.Allowed || dec.RuleID != "local-clients" {
		t.Fatalf("expected allowed by local-clients, got %+v", dec)
	}
	dec = policy.Decide(netip.MustParseAddr("10.5.5.5"))
	if !dec.Allowed || dec.RuleID != "local-clients" {
		t.Fatalf("expected allowed by local-clients, got %+v", dec)
	}
	dec = policy.Decide(netip.MustParseAddr("172.16.0.1"))
	if dec.Allowed || dec.RuleID != "" {
		t.Fatalf("expected denied for non-matching IP, got %+v", dec)
	}
}

func TestVersion1MigrationEmptyLegacyListMigratesDenyAll(t *testing.T) {
	v1JSON := `{
		"version": 1,
		"revision": 1,
		"listeners": {
			"http": "127.0.0.1:17890",
			"socks5": "127.0.0.1:17891",
			"admin": "127.0.0.1:17892",
			"pac": "127.0.0.1:17893"
		},
		"security": {
			"allowed_clients": [],
			"admin_token_file": "/tmp/admin.token",
			"tls_cert_file": "/tmp/admin.crt",
			"tls_key_file": "/tmp/admin.key"
		},
		"outbounds": [{"id": "direct", "type": "direct", "enabled": true, "public_internet": true}],
		"rules": [],
		"default_outbound": "direct",
		"adaptive": {"candidates": ["direct"], "window_seconds": 600, "min_samples": 3, "improvement_percent": 25, "cooldown_seconds": 600, "probes_per_minute": 12, "probe_concurrency": 2, "probe_timeout_seconds": 4},
		"pac": {"proxy_address": "127.0.0.1:17890"}
	}`

	got, err := Decode([]byte(v1JSON))
	if err != nil {
		t.Fatalf("failed to decode v1 config with empty allowed_clients: %v", err)
	}
	if got.Version != 2 {
		t.Fatalf("expected version 2, got %d", got.Version)
	}
	if len(got.SourceAccess.Rules) != 0 {
		t.Fatalf("expected 0 source access rules for empty legacy list, got %d", len(got.SourceAccess.Rules))
	}
	if len(got.Security.AllowedClients) != 0 {
		t.Fatalf("expected 0 allowed_clients, got %d", len(got.Security.AllowedClients))
	}

	policy, err := CompileSourceAccess(got.SourceAccess)
	if err != nil {
		t.Fatalf("failed to compile empty source access: %v", err)
	}
	dec := policy.Decide(netip.MustParseAddr("127.0.0.1"))
	if dec.Allowed || dec.RuleID != "" {
		t.Fatalf("expected deny-all for empty source rules, got %+v", dec)
	}

	mgmtPolicy, err := access.CompileAllowlist(got.Security.AllowedClients)
	if err != nil {
		t.Fatalf("failed to compile empty management allowlist: %v", err)
	}
	dec = mgmtPolicy.Decide(netip.MustParseAddr("127.0.0.1"))
	if dec.Allowed {
		t.Fatalf("expected management ACL to deny-all when empty, got allowed")
	}
}

func TestVersion2EmptyRulesDenyAll(t *testing.T) {
	v2JSON := `{
		"version": 2,
		"revision": 1,
		"listeners": {
			"http": "127.0.0.1:17890",
			"socks5": "127.0.0.1:17891",
			"admin": "127.0.0.1:17892",
			"pac": "127.0.0.1:17893"
		},
		"security": {
			"allowed_clients": ["127.0.0.0/8"],
			"admin_token_file": "/tmp/admin.token",
			"tls_cert_file": "/tmp/admin.crt",
			"tls_key_file": "/tmp/admin.key"
		},
		"source_access": {
			"rules": []
		},
		"outbounds": [{"id": "direct", "type": "direct", "enabled": true, "public_internet": true}],
		"rules": [],
		"default_outbound": "direct",
		"adaptive": {"candidates": ["direct"], "window_seconds": 600, "min_samples": 3, "improvement_percent": 25, "cooldown_seconds": 600, "probes_per_minute": 12, "probe_concurrency": 2, "probe_timeout_seconds": 4},
		"pac": {"proxy_address": "127.0.0.1:17890"}
	}`

	got, err := Decode([]byte(v2JSON))
	if err != nil {
		t.Fatalf("failed to decode v2 empty rules: %v", err)
	}
	if len(got.SourceAccess.Rules) != 0 {
		t.Fatalf("expected 0 rules, got %d", len(got.SourceAccess.Rules))
	}
	policy, err := CompileSourceAccess(got.SourceAccess)
	if err != nil {
		t.Fatalf("failed to compile: %v", err)
	}
	dec := policy.Decide(netip.MustParseAddr("127.0.0.1"))
	if dec.Allowed {
		t.Fatalf("expected deny-all for v2 empty rules, got %+v", dec)
	}
}

func TestVersion1InvalidAllowedClientsFailsClosed(t *testing.T) {
	v1JSON := `{
		"version": 1,
		"revision": 1,
		"listeners": {
			"http": "127.0.0.1:17890",
			"socks5": "127.0.0.1:17891",
			"admin": "127.0.0.1:17892",
			"pac": "127.0.0.1:17893"
		},
		"security": {
			"allowed_clients": ["not-a-valid-cidr"],
			"admin_token_file": "/tmp/admin.token",
			"tls_cert_file": "/tmp/admin.crt",
			"tls_key_file": "/tmp/admin.key"
		},
		"outbounds": [{"id": "direct", "type": "direct", "enabled": true, "public_internet": true}],
		"rules": [],
		"default_outbound": "direct",
		"adaptive": {"candidates": ["direct"], "window_seconds": 600, "min_samples": 3, "improvement_percent": 25, "cooldown_seconds": 600, "probes_per_minute": 12, "probe_concurrency": 2, "probe_timeout_seconds": 4},
		"pac": {"proxy_address": "127.0.0.1:17890"}
	}`

	_, err := Decode([]byte(v1JSON))
	if err == nil || !strings.Contains(err.Error(), "allowed_clients") {
		t.Fatalf("expected allowed_clients error, got: %v", err)
	}
}

func TestUnknownFieldsRejection(t *testing.T) {
	// v1 with source_access should be rejected as unknown field
	v1WithSourceAccess := `{
		"version": 1,
		"revision": 1,
		"listeners": {"admin": "127.0.0.1:17892"},
		"security": {
			"allowed_clients": ["127.0.0.0/8"],
			"admin_token_file": "/tmp/admin.token",
			"tls_cert_file": "/tmp/admin.crt",
			"tls_key_file": "/tmp/admin.key"
		},
		"source_access": {"rules": []},
		"outbounds": [{"id": "direct", "type": "direct", "enabled": true, "public_internet": true}],
		"default_outbound": "direct"
	}`
	if _, err := Decode([]byte(v1WithSourceAccess)); err == nil {
		t.Fatal("v1 config accepted unexpected source_access field")
	}

	// v1 with random field
	v1WithExtra := `{
		"version": 1,
		"revision": 1,
		"unexpected_field": "test",
		"listeners": {"admin": "127.0.0.1:17892"},
		"security": {
			"allowed_clients": ["127.0.0.0/8"],
			"admin_token_file": "/tmp/admin.token",
			"tls_cert_file": "/tmp/admin.crt",
			"tls_key_file": "/tmp/admin.key"
		},
		"outbounds": [{"id": "direct", "type": "direct", "enabled": true, "public_internet": true}],
		"default_outbound": "direct"
	}`
	if _, err := Decode([]byte(v1WithExtra)); err == nil {
		t.Fatal("v1 config accepted unexpected unknown field")
	}

	// v2 with random field
	v2WithExtra := `{
		"version": 2,
		"revision": 1,
		"unknown_v2_field": true,
		"listeners": {"admin": "127.0.0.1:17892"},
		"security": {
			"allowed_clients": ["127.0.0.0/8"],
			"admin_token_file": "/tmp/admin.token",
			"tls_cert_file": "/tmp/admin.crt",
			"tls_key_file": "/tmp/admin.key"
		},
		"source_access": {"rules": []},
		"outbounds": [{"id": "direct", "type": "direct", "enabled": true, "public_internet": true}],
		"default_outbound": "direct"
	}`
	if _, err := Decode([]byte(v2WithExtra)); err == nil {
		t.Fatal("v2 config accepted unexpected unknown field")
	}
}

func TestRoundTripVersion1AndVersion2WithoutSecrets(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "migrated.json")

	v1JSON := `{
		"version": 1,
		"revision": 5,
		"listeners": {
			"http": "127.0.0.1:17890",
			"socks5": "127.0.0.1:17891",
			"admin": "127.0.0.1:17892",
			"pac": "127.0.0.1:17893"
		},
		"security": {
			"allowed_clients": ["10.0.0.0/8"],
			"admin_token_file": filepath.Join(dir, "admin.token"),
			"tls_cert_file": filepath.Join(dir, "admin.crt"),
			"tls_key_file": filepath.Join(dir, "admin.key"),
			"proxy_username": "operator",
			"proxy_password_file": filepath.Join(dir, "proxy.pass")
		},
		"outbounds": [{"id": "direct", "type": "direct", "enabled": true, "public_internet": true}],
		"rules": [],
		"default_outbound": "direct",
		"adaptive": {"candidates": ["direct"], "window_seconds": 600, "min_samples": 3, "improvement_percent": 25, "cooldown_seconds": 600, "probes_per_minute": 12, "probe_concurrency": 2, "probe_timeout_seconds": 4},
		"pac": {"proxy_address": "127.0.0.1:17890"}
	}`
	v1JSON = strings.ReplaceAll(v1JSON, `filepath.Join(dir, "admin.token")`, `"`+filepath.Join(dir, "admin.token")+`"`)
	v1JSON = strings.ReplaceAll(v1JSON, `filepath.Join(dir, "admin.crt")`, `"`+filepath.Join(dir, "admin.crt")+`"`)
	v1JSON = strings.ReplaceAll(v1JSON, `filepath.Join(dir, "admin.key")`, `"`+filepath.Join(dir, "admin.key")+`"`)
	v1JSON = strings.ReplaceAll(v1JSON, `filepath.Join(dir, "proxy.pass")`, `"`+filepath.Join(dir, "proxy.pass")+`"`)

	decoded, err := Decode([]byte(v1JSON))
	if err != nil {
		t.Fatalf("failed to decode v1: %v", err)
	}
	if decoded.Version != 2 {
		t.Fatalf("expected version 2 in memory, got %d", decoded.Version)
	}

	if err := Save(p, decoded); err != nil {
		t.Fatalf("failed to save migrated config: %v", err)
	}

	rawBytes, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("failed to read saved config: %v", err)
	}
	content := string(rawBytes)
	if !strings.Contains(content, `"version": 2`) {
		t.Fatalf("saved file missing version 2: %s", content)
	}
	if !strings.Contains(content, `"source_access"`) {
		t.Fatalf("saved file missing source_access: %s", content)
	}
	if !strings.Contains(content, `"admin_token_file"`) || !strings.Contains(content, `"proxy_password_file"`) {
		t.Fatalf("saved file missing secret file references: %s", content)
	}

	loaded, err := Load(p)
	if err != nil {
		t.Fatalf("failed to load saved config: %v", err)
	}
	if loaded.Version != 2 || loaded.Revision != 5 {
		t.Fatalf("unexpected loaded config: %+v", loaded)
	}
	if len(loaded.SourceAccess.Rules) != 1 || loaded.SourceAccess.Rules[0].ID != "local-clients" {
		t.Fatalf("unexpected loaded source rules: %+v", loaded.SourceAccess)
	}
}

func TestSourceAccessValidation(t *testing.T) {
	base := Default(t.TempDir())

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name: "too many rules",
			mutate: func(c *Config) {
				rules := make([]SourceAccessRule, 257)
				for i := range rules {
					rules[i] = SourceAccessRule{
						ID:      fmt.Sprintf("rule-%d", i),
						Name:    "Test",
						Action:  "allow",
						CIDRs:   []string{"127.0.0.1/32"},
						Enabled: true,
					}
				}
				c.SourceAccess.Rules = rules
			},
			wantErr: "maximum of 256 rules",
		},
		{
			name: "too many CIDRs",
			mutate: func(c *Config) {
				cidrs := make([]string, 1025)
				for i := range cidrs {
					cidrs[i] = "10.0.0.1/32"
				}
				c.SourceAccess.Rules = []SourceAccessRule{
					{ID: "many-cidrs", Name: "Many", Action: "allow", CIDRs: cidrs, Enabled: true},
				}
			},
			wantErr: "1,024 total CIDRs",
		},
		{
			name: "invalid rule ID leading dash",
			mutate: func(c *Config) {
				c.SourceAccess.Rules = []SourceAccessRule{
					{ID: "-bad", Name: "Bad", Action: "allow", CIDRs: []string{"10.0.0.0/8"}, Enabled: true},
				}
			},
			wantErr: "invalid source rule ID",
		},
		{
			name: "invalid rule ID spaces",
			mutate: func(c *Config) {
				c.SourceAccess.Rules = []SourceAccessRule{
					{ID: "bad id", Name: "Bad", Action: "allow", CIDRs: []string{"10.0.0.0/8"}, Enabled: true},
				}
			},
			wantErr: "invalid source rule ID",
		},
		{
			name: "duplicate rule ID",
			mutate: func(c *Config) {
				c.SourceAccess.Rules = []SourceAccessRule{
					{ID: "rule-1", Name: "One", Action: "allow", CIDRs: []string{"10.0.0.0/8"}, Enabled: true},
					{ID: "rule-1", Name: "Two", Action: "deny", CIDRs: []string{"192.168.0.0/16"}, Enabled: true},
				}
			},
			wantErr: "duplicate source rule ID",
		},
		{
			name: "blank name empty",
			mutate: func(c *Config) {
				c.SourceAccess.Rules = []SourceAccessRule{
					{ID: "blank-name", Name: "", Action: "allow", CIDRs: []string{"10.0.0.0/8"}, Enabled: true},
				}
			},
			wantErr: "name must not be blank",
		},
		{
			name: "blank name spaces",
			mutate: func(c *Config) {
				c.SourceAccess.Rules = []SourceAccessRule{
					{ID: "blank-name", Name: "   ", Action: "allow", CIDRs: []string{"10.0.0.0/8"}, Enabled: true},
				}
			},
			wantErr: "name must not be blank",
		},
		{
			name: "name exceeds 256 runes",
			mutate: func(c *Config) {
				c.SourceAccess.Rules = []SourceAccessRule{
					{ID: "long-name", Name: strings.Repeat("名", 257), Action: "allow", CIDRs: []string{"10.0.0.0/8"}, Enabled: true},
				}
			},
			wantErr: "name exceeds 256 characters",
		},
		{
			name: "invalid action",
			mutate: func(c *Config) {
				c.SourceAccess.Rules = []SourceAccessRule{
					{ID: "bad-action", Name: "Bad", Action: "block", CIDRs: []string{"10.0.0.0/8"}, Enabled: true},
				}
			},
			wantErr: "action must be allow or deny",
		},
		{
			name: "empty CIDRs",
			mutate: func(c *Config) {
				c.SourceAccess.Rules = []SourceAccessRule{
					{ID: "empty-cidrs", Name: "Empty", Action: "allow", CIDRs: []string{}, Enabled: true},
				}
			},
			wantErr: "at least one CIDR is required",
		},
		{
			name: "note exceeds 1024 runes",
			mutate: func(c *Config) {
				c.SourceAccess.Rules = []SourceAccessRule{
					{ID: "long-note", Name: "Note", Action: "allow", CIDRs: []string{"10.0.0.0/8"}, Enabled: true, Note: strings.Repeat("📝", 1025)},
				}
			},
			wantErr: "note exceeds 1024 characters",
		},
		{
			name: "invalid CIDR syntax in enabled rule",
			mutate: func(c *Config) {
				c.SourceAccess.Rules = []SourceAccessRule{
					{ID: "bad-cidr", Name: "Bad", Action: "allow", CIDRs: []string{"not-a-cidr"}, Enabled: true},
				}
			},
			wantErr: "CIDR",
		},
		{
			name: "invalid CIDR syntax in disabled rule",
			mutate: func(c *Config) {
				c.SourceAccess.Rules = []SourceAccessRule{
					{ID: "bad-disabled", Name: "Bad", Action: "allow", CIDRs: []string{"999.999.999.999/32"}, Enabled: false},
				}
			},
			wantErr: "CIDR",
		},
		{
			name: "mapped-IPv6 CIDR rejected",
			mutate: func(c *Config) {
				c.SourceAccess.Rules = []SourceAccessRule{
					{ID: "mapped-cidr", Name: "Mapped", Action: "allow", CIDRs: []string{"::ffff:127.0.0.1/32"}, Enabled: true},
				}
			},
			wantErr: "mapped-IPv6",
		},
		{
			name: "zoned CIDR rejected",
			mutate: func(c *Config) {
				c.SourceAccess.Rules = []SourceAccessRule{
					{ID: "zoned-cidr", Name: "Zoned", Action: "allow", CIDRs: []string{"fe80::1%eth0/64"}, Enabled: true},
				}
			},
			wantErr: "zone",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.mutate(&cfg)
			err := Validate(cfg)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tc.wantErr, err)
			}
		})
	}
}

func TestCompileSourceAccessPrecedenceAndTieBreaking(t *testing.T) {
	sa := SourceAccess{
		Rules: []SourceAccessRule{
			{
				ID:      "broad-allow",
				Name:    "Broad allow",
				Action:  "allow",
				CIDRs:   []string{"10.0.0.0/8"},
				Enabled: true,
			},
			{
				ID:      "narrow-deny",
				Name:    "Narrow deny",
				Action:  "deny",
				CIDRs:   []string{"10.1.0.0/16"},
				Enabled: true,
			},
			{
				ID:      "equal-allow",
				Name:    "Equal allow",
				Action:  "allow",
				CIDRs:   []string{"192.168.1.0/24"},
				Enabled: true,
			},
			{
				ID:      "equal-deny",
				Name:    "Equal deny",
				Action:  "deny",
				CIDRs:   []string{"192.168.1.0/24"},
				Enabled: true,
			},
			{
				ID:      "earlier-allow",
				Name:    "Earlier allow",
				Action:  "allow",
				CIDRs:   []string{"172.16.0.0/16"},
				Enabled: true,
			},
			{
				ID:      "later-allow",
				Name:    "Later allow",
				Action:  "allow",
				CIDRs:   []string{"172.16.0.0/16"},
				Enabled: true,
			},
			{
				ID:      "disabled-allow",
				Name:    "Disabled allow",
				Action:  "allow",
				CIDRs:   []string{"100.64.0.0/10"},
				Enabled: false,
			},
		},
	}

	policy, err := CompileSourceAccess(sa)
	if err != nil {
		t.Fatalf("failed to compile: %v", err)
	}

	// Longest prefix wins: 10.1.0.0/16 (deny) beats 10.0.0.0/8 (allow)
	dec := policy.Decide(netip.MustParseAddr("10.1.2.3"))
	if dec.Allowed || dec.RuleID != "narrow-deny" {
		t.Fatalf("expected narrow-deny, got %+v", dec)
	}
	dec = policy.Decide(netip.MustParseAddr("10.2.2.3"))
	if !dec.Allowed || dec.RuleID != "broad-allow" {
		t.Fatalf("expected broad-allow, got %+v", dec)
	}

	// Equal prefix length: deny beats allow
	dec = policy.Decide(netip.MustParseAddr("192.168.1.5"))
	if dec.Allowed || dec.RuleID != "equal-deny" {
		t.Fatalf("expected equal-deny, got %+v", dec)
	}

	// Equal prefix and action: earlier rule in config order supplies rule_id
	dec = policy.Decide(netip.MustParseAddr("172.16.0.10"))
	if !dec.Allowed || dec.RuleID != "earlier-allow" {
		t.Fatalf("expected earlier-allow, got %+v", dec)
	}

	// Disabled rule is omitted from the trie
	dec = policy.Decide(netip.MustParseAddr("100.64.1.1"))
	if dec.Allowed || dec.RuleID != "" {
		t.Fatalf("expected denied for disabled rule, got %+v", dec)
	}
}

func TestExampleConfigValidates(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "config.json")
	c, err := Load(path)
	if err != nil {
		t.Fatalf("failed to load examples/config.json: %v", err)
	}
	if c.Version != 2 {
		t.Fatalf("expected version 2 in examples/config.json, got %d", c.Version)
	}
	if len(c.SourceAccess.Rules) == 0 {
		t.Fatal("expected source_access.rules in examples/config.json")
	}
	if _, err := CompileSourceAccess(c.SourceAccess); err != nil {
		t.Fatalf("failed to compile source access from examples/config.json: %v", err)
	}
}
