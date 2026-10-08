package config

import (
	"encoding/json"
	"os"
	"path/filepath"
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
