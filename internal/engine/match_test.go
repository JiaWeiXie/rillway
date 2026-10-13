package engine

import (
	"net/netip"
	"rillway/internal/config"
	"testing"
)

func TestMatchCompositeConditionsANDWithORWithinEachList(t *testing.T) {
	m := compileTarget(config.Rule{
		Suffixes: []string{"other.example", "example.com"},
		Sources:  []string{"198.51.100.0/24", "203.0.113.0/24"},
		Inbounds: []string{"http", "socks5"},
		Ports:    []string{"443", "8000-8100"},
	})
	base := target{host: "a.example.com", port: "8050", source: netip.MustParseAddr("203.0.113.7"), inbound: "socks5"}
	for _, tt := range []struct {
		name   string
		change func(*target)
		want   bool
	}{
		{"manual match", func(*target) {}, true},
		{"other suffix alternative", func(v *target) { v.host = "x.other.example" }, true},
		{"other source alternative", func(v *target) { v.source = netip.MustParseAddr("198.51.100.9") }, true},
		{"other inbound alternative", func(v *target) { v.inbound = "http" }, true},
		{"single port alternative", func(v *target) { v.port = "443" }, true},
		{"wrong suffix", func(v *target) { v.host = "evil-example.com" }, false},
		{"wrong source", func(v *target) { v.source = netip.MustParseAddr("192.0.2.7") }, false},
		{"wrong inbound", func(v *target) { v.inbound = "ftp" }, false},
		{"wrong port", func(v *target) { v.port = "8101" }, false},
		{"missing source", func(v *target) { v.source = netip.Addr{} }, false},
		{"missing inbound", func(v *target) { v.inbound = "" }, false},
		{"missing port", func(v *target) { v.port = "" }, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			v := base
			tt.change(&v)
			if got := m.match(v); got != tt.want {
				t.Fatalf("match(%+v) = %v, want %v", v, got, tt.want)
			}
		})
	}
}

func TestMatchManualSocksOnlyRejectsHTTP(t *testing.T) {
	m := compileTarget(config.Rule{Suffixes: []string{"example.com"}, Sources: []string{"203.0.113.0/24"}, Inbounds: []string{"socks5"}, Ports: []string{"8000-8100"}})
	v := target{host: "a.example.com", port: "8050", source: netip.MustParseAddr("203.0.113.7"), inbound: "socks5"}
	if !m.match(v) {
		t.Fatal("approved manual SOCKS example did not match")
	}
	v.inbound = "http"
	if m.match(v) {
		t.Fatal("HTTP matched a SOCKS-only rule")
	}
}

func TestMatchEmptyConditionsAndHostnameBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name   string
		rule   config.Rule
		target target
		want   bool
	}{
		{"empty conditions pass", config.Rule{}, target{}, true},
		{"domain normalization", config.Rule{Domains: []string{"EXAMPLE.COM."}}, target{host: normalize("EXAMPLE.COM.")}, true},
		{"domain is exact", config.Rule{Domains: []string{"example.com"}}, target{host: "a.example.com"}, false},
		{"suffix apex", config.Rule{Suffixes: []string{"example.com"}}, target{host: "example.com"}, true},
		{"suffix label boundary", config.Rule{Suffixes: []string{"example.com"}}, target{host: "evilexample.com"}, false},
		{"domain and suffix both required", config.Rule{Domains: []string{"a.example.com"}, Suffixes: []string{"other.example"}}, target{host: "a.example.com"}, false},
		{"domain and suffix both satisfied", config.Rule{Domains: []string{"a.example.com"}, Suffixes: []string{"example.com"}}, target{host: "a.example.com"}, true},
		{"literal IPv4 CIDR", config.Rule{CIDRs: []string{"203.0.113.0/24"}}, target{host: "203.0.113.7"}, true},
		{"literal mapped IPv4 CIDR", config.Rule{CIDRs: []string{"203.0.113.0/24"}}, target{host: "::ffff:203.0.113.7"}, true},
		{"literal IPv6 CIDR", config.Rule{CIDRs: []string{"2001:db8::/32"}}, target{host: "2001:db8::7"}, true},
		{"hostname cannot match CIDR", config.Rule{CIDRs: []string{"0.0.0.0/0", "::/0"}}, target{host: "localhost"}, false},
		{"source IPv6", config.Rule{Sources: []string{"2001:db8::/32"}}, target{source: netip.MustParseAddr("2001:db8::7")}, true},
		{"unspecified metadata not required", config.Rule{Suffixes: []string{"example.com"}}, target{host: "a.example.com"}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := compileTarget(tt.rule).match(tt.target); got != tt.want {
				t.Fatalf("match(%+v) = %v, want %v", tt.target, got, tt.want)
			}
		})
	}
}

func TestRuleSetMergeUnionsListsWithoutDroppingOwnConditions(t *testing.T) {
	for _, tt := range []struct {
		name                       string
		own                        config.Rule
		set                        config.RuleSet
		ownTarget, setTarget, miss target
	}{
		{"domains", config.Rule{Domains: []string{"a.example"}}, config.RuleSet{Domains: []string{"b.example"}}, target{host: "a.example"}, target{host: "b.example"}, target{host: "c.example"}},
		{"suffixes", config.Rule{Suffixes: []string{"a.example"}}, config.RuleSet{Suffixes: []string{"b.example"}}, target{host: "x.a.example"}, target{host: "x.b.example"}, target{host: "x.c.example"}},
		{"CIDRs", config.Rule{CIDRs: []string{"192.0.2.0/24"}}, config.RuleSet{CIDRs: []string{"203.0.113.0/24"}}, target{host: "192.0.2.7"}, target{host: "203.0.113.7"}, target{host: "198.51.100.7"}},
		{"sources", config.Rule{Sources: []string{"192.0.2.0/24"}}, config.RuleSet{Sources: []string{"203.0.113.0/24"}}, target{source: netip.MustParseAddr("192.0.2.7")}, target{source: netip.MustParseAddr("203.0.113.7")}, target{source: netip.MustParseAddr("198.51.100.7")}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.own.Sets = []string{"s1", "s1"}
			m := compileRule(tt.own, map[string]config.RuleSet{"s1": tt.set})
			if !m.match(tt.ownTarget) || !m.match(tt.setTarget) || m.match(tt.miss) {
				t.Fatal("rule-set union omitted an alternative or widened beyond the listed conditions")
			}
			if compileTarget(tt.own).match(tt.setTarget) {
				t.Fatal("compileTarget expanded a referenced rule set")
			}
		})
	}
	m := compileRule(config.Rule{Suffixes: []string{"a.example"}, Sets: []string{"s1", "s2"}}, map[string]config.RuleSet{
		"s1": {Suffixes: []string{"b.example"}},
		"s2": {Suffixes: []string{"c.example"}, Sources: []string{"203.0.113.0/24"}},
	})
	for _, host := range []string{"x.a.example", "x.b.example", "x.c.example"} {
		if !m.match(target{host: host, source: netip.MustParseAddr("203.0.113.7")}) || m.match(target{host: host}) {
			t.Fatalf("rule sets must union suffixes and AND the added source condition: %s", host)
		}
	}
}

func TestMatchPortBoundaries(t *testing.T) {
	m := compileTarget(config.Rule{Ports: []string{"1", "8000-8100", "65535"}})
	for _, tt := range []struct {
		port string
		want bool
	}{
		{"1", true},
		{"8000", true},
		{"8050", true},
		{"8100", true},
		{"65535", true},
		{"0", false},
		{"7999", false},
		{"8101", false},
		{"65536", false},
		{"", false},
		{"https", false},
	} {
		if got := m.match(target{port: tt.port}); got != tt.want {
			t.Errorf("port %q: match = %v, want %v", tt.port, got, tt.want)
		}
	}
}

func TestParseRulePortRange(t *testing.T) {
	for _, tt := range []struct {
		value  string
		lo, hi uint16
	}{
		{"1", 1, 1},
		{"443", 443, 443},
		{"65535", 65535, 65535},
		{"1-65535", 1, 65535},
		{"8000-8100", 8000, 8100},
		{"443-443", 443, 443},
	} {
		got, err := parsePortRange(tt.value)
		if err != nil || got != (portRange{lo: tt.lo, hi: tt.hi}) {
			t.Errorf("parsePortRange(%q) = %+v, %v; want %d-%d", tt.value, got, err, tt.lo, tt.hi)
		}
	}
	for _, value := range []string{"", "0", "65536", "70000", "443-80", "0-443", "443-65536", "-1", "80-", "-443", "80-81-82", "https", "1.5"} {
		if _, err := parsePortRange(value); err == nil {
			t.Errorf("invalid port range %q accepted", value)
		}
	}
}
