package access

import (
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestParsePeer(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantIP  string
		wantErr bool
	}{
		{
			name:   "ipv4 with port",
			input:  "192.0.2.1:12345",
			wantIP: "192.0.2.1",
		},
		{
			name:   "ipv4 bare",
			input:  "192.0.2.1",
			wantIP: "192.0.2.1",
		},
		{
			name:   "ipv4 loopback with port",
			input:  "127.0.0.1:80",
			wantIP: "127.0.0.1",
		},
		{
			name:   "ipv6 bracketed with port",
			input:  "[2001:db8::1]:8080",
			wantIP: "2001:db8::1",
		},
		{
			name:   "ipv6 bare",
			input:  "2001:db8::1",
			wantIP: "2001:db8::1",
		},
		{
			name:   "ipv6 bracketed without port",
			input:  "[2001:db8::1]",
			wantIP: "2001:db8::1",
		},
		{
			name:   "ipv6 loopback",
			input:  "::1",
			wantIP: "::1",
		},
		{
			name:   "ipv6 loopback with port",
			input:  "[::1]:80",
			wantIP: "::1",
		},
		{
			name:   "ipv6 with zone and port strips zone",
			input:  "[fe80::1%eth0]:53",
			wantIP: "fe80::1",
		},
		{
			name:   "ipv6 with zone bare strips zone",
			input:  "fe80::1%eth0",
			wantIP: "fe80::1",
		},
		{
			name:   "ipv6 with zone bracketed strips zone",
			input:  "[fe80::1%eth0]",
			wantIP: "fe80::1",
		},
		{
			name:   "ipv4-mapped ipv6 with port unmaps",
			input:  "[::ffff:192.0.2.1]:80",
			wantIP: "192.0.2.1",
		},
		{
			name:   "ipv4-mapped ipv6 bare unmaps",
			input:  "::ffff:192.0.2.1",
			wantIP: "192.0.2.1",
		},
		{
			name:   "ipv4-mapped ipv6 bracketed unmaps",
			input:  "[::ffff:192.0.2.1]",
			wantIP: "192.0.2.1",
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name:    "whitespace only",
			input:   "   ",
			wantErr: true,
		},
		{
			name:    "invalid host",
			input:   "not-an-ip:80",
			wantErr: true,
		},
		{
			name:    "invalid port non-numeric",
			input:   "192.0.2.1:foo",
			wantErr: true,
		},
		{
			name:    "missing port after colon",
			input:   "192.0.2.1:",
			wantErr: true,
		},
		{
			name:    "invalid port out of range",
			input:   "192.0.2.1:70000",
			wantErr: true,
		},
		{
			name:    "invalid ipv4 octet",
			input:   "192.0.2.300",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			addr, err := ParsePeer(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParsePeer(%q) expected error, got addr %v", tc.input, addr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParsePeer(%q) unexpected error: %v", tc.input, err)
			}
			if addr.String() != tc.wantIP {
				t.Fatalf("ParsePeer(%q) = %s, want %s", tc.input, addr.String(), tc.wantIP)
			}
			if addr.Zone() != "" {
				t.Fatalf("ParsePeer(%q) zone not stripped: %q", tc.input, addr.Zone())
			}
			if addr.Is4In6() {
				t.Fatalf("ParsePeer(%q) mapped IPv4 not unmapped: %v", tc.input, addr)
			}
		})
	}
}

func TestParsePrefix(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantPrefix string
		wantErr    bool
	}{
		{
			name:       "canonical ipv4",
			input:      "192.168.1.0/24",
			wantPrefix: "192.168.1.0/24",
		},
		{
			name:       "non-canonical ipv4 gets masked",
			input:      "192.168.1.50/24",
			wantPrefix: "192.168.1.0/24",
		},
		{
			name:       "single host ipv4",
			input:      "10.0.0.1/32",
			wantPrefix: "10.0.0.1/32",
		},
		{
			name:       "default route ipv4",
			input:      "0.0.0.0/0",
			wantPrefix: "0.0.0.0/0",
		},
		{
			name:       "canonical ipv6",
			input:      "2001:db8::/32",
			wantPrefix: "2001:db8::/32",
		},
		{
			name:       "non-canonical ipv6 gets masked",
			input:      "2001:db8::1234/64",
			wantPrefix: "2001:db8::/64",
		},
		{
			name:       "single host ipv6",
			input:      "::1/128",
			wantPrefix: "::1/128",
		},
		{
			name:       "default route ipv6",
			input:      "::/0",
			wantPrefix: "::/0",
		},
		{
			name:    "mapped-ipv6 prefix rejected",
			input:   "::ffff:192.168.1.0/120",
			wantErr: true,
		},
		{
			name:    "mapped-ipv6 single host rejected",
			input:   "::ffff:192.0.2.1/128",
			wantErr: true,
		},
		{
			name:    "zoned prefix rejected",
			input:   "fe80::1%eth0/64",
			wantErr: true,
		},
		{
			name:    "bare ip rejected",
			input:   "192.168.1.1",
			wantErr: true,
		},
		{
			name:    "malformed prefix",
			input:   "garbage/24",
			wantErr: true,
		},
		{
			name:    "empty prefix",
			input:   "",
			wantErr: true,
		},
		{
			name:    "prefix bits out of range ipv4",
			input:   "192.168.1.0/33",
			wantErr: true,
		},
		{
			name:    "prefix bits out of range ipv6",
			input:   "::1/129",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prefix, err := ParsePrefix(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParsePrefix(%q) expected error, got %v", tc.input, prefix)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParsePrefix(%q) unexpected error: %v", tc.input, err)
			}
			if prefix.String() != tc.wantPrefix {
				t.Fatalf("ParsePrefix(%q) = %s, want %s", tc.input, prefix.String(), tc.wantPrefix)
			}
		})
	}
}

func TestDefaultDeny(t *testing.T) {
	// Empty policy denies everything.
	p, err := Compile(nil)
	if err != nil {
		t.Fatalf("Compile(nil) unexpected error: %v", err)
	}

	for _, ipStr := range []string{"127.0.0.1", "10.0.0.1", "::1", "2001:db8::1"} {
		ip := netip.MustParseAddr(ipStr)
		d := p.Decide(ip)
		if d.Allowed || d.RuleID != "" {
			t.Fatalf("empty policy decided %v for %s, want deny with empty ruleID", d, ipStr)
		}
	}

	// Nil policy denies everything safely without panic.
	var nilPolicy *Policy
	d := nilPolicy.Decide(netip.MustParseAddr("127.0.0.1"))
	if d.Allowed || d.RuleID != "" {
		t.Fatalf("nil policy decided %v, want deny with empty ruleID", d)
	}

	// Invalid IP address denies safely.
	d = p.Decide(netip.Addr{})
	if d.Allowed || d.RuleID != "" {
		t.Fatalf("invalid addr decided %v, want deny with empty ruleID", d)
	}
}

func TestLongestPrefixSelection(t *testing.T) {
	rules := []RuleSpec{
		{
			ID:      "v4-broad",
			Action:  ActionAllow,
			CIDRs:   []string{"10.0.0.0/8"},
			Enabled: true,
		},
		{
			ID:      "v4-mid",
			Action:  ActionDeny,
			CIDRs:   []string{"10.1.0.0/16"},
			Enabled: true,
		},
		{
			ID:      "v4-narrow",
			Action:  ActionAllow,
			CIDRs:   []string{"10.1.2.0/24"},
			Enabled: true,
		},
		{
			ID:      "v4-host",
			Action:  ActionDeny,
			CIDRs:   []string{"10.1.2.5/32"},
			Enabled: true,
		},
		{
			ID:      "v6-broad",
			Action:  ActionAllow,
			CIDRs:   []string{"2001:db8::/32"},
			Enabled: true,
		},
		{
			ID:      "v6-mid",
			Action:  ActionDeny,
			CIDRs:   []string{"2001:db8:1::/48"},
			Enabled: true,
		},
		{
			ID:      "v6-narrow",
			Action:  ActionAllow,
			CIDRs:   []string{"2001:db8:1:2::/64"},
			Enabled: true,
		},
	}

	p, err := Compile(rules)
	if err != nil {
		t.Fatalf("Compile unexpected error: %v", err)
	}

	cases := []struct {
		ip          string
		wantAllowed bool
		wantRuleID  string
	}{
		{"10.2.0.1", true, "v4-broad"},
		{"10.1.5.1", false, "v4-mid"},
		{"10.1.2.10", true, "v4-narrow"},
		{"10.1.2.5", false, "v4-host"},
		{"11.0.0.1", false, ""}, // no match
		{"2001:db8:ffff::1", true, "v6-broad"},
		{"2001:db8:1:99::1", false, "v6-mid"},
		{"2001:db8:1:2::1", true, "v6-narrow"},
		{"2001:db9::1", false, ""}, // no match
	}

	for _, c := range cases {
		ip := netip.MustParseAddr(c.ip)
		d := p.Decide(ip)
		if d.Allowed != c.wantAllowed || d.RuleID != c.wantRuleID {
			t.Fatalf("Decide(%s) = %+v, want Allowed=%v RuleID=%q", c.ip, d, c.wantAllowed, c.wantRuleID)
		}
	}
}

func TestEqualPrefixDenyPrecedence(t *testing.T) {
	// Case 1: allow rule declared before deny rule at same prefix.
	rules1 := []RuleSpec{
		{
			ID:      "allow-first",
			Action:  ActionAllow,
			CIDRs:   []string{"192.168.1.0/24"},
			Enabled: true,
		},
		{
			ID:      "deny-second",
			Action:  ActionDeny,
			CIDRs:   []string{"192.168.1.0/24"},
			Enabled: true,
		},
	}

	p1, err := Compile(rules1)
	if err != nil {
		t.Fatalf("Compile rules1 error: %v", err)
	}
	d1 := p1.Decide(netip.MustParseAddr("192.168.1.50"))
	if d1.Allowed || d1.RuleID != "deny-second" {
		t.Fatalf("expected deny to beat allow at same prefix: got %+v", d1)
	}

	// Case 2: deny rule declared before allow rule at same prefix.
	rules2 := []RuleSpec{
		{
			ID:      "deny-first",
			Action:  ActionDeny,
			CIDRs:   []string{"192.168.1.0/24"},
			Enabled: true,
		},
		{
			ID:      "allow-second",
			Action:  ActionAllow,
			CIDRs:   []string{"192.168.1.0/24"},
			Enabled: true,
		},
	}

	p2, err := Compile(rules2)
	if err != nil {
		t.Fatalf("Compile rules2 error: %v", err)
	}
	d2 := p2.Decide(netip.MustParseAddr("192.168.1.50"))
	if d2.Allowed || d2.RuleID != "deny-first" {
		t.Fatalf("expected deny to beat allow at same prefix: got %+v", d2)
	}

	// Case 3: IPv6 equal prefix deny precedence.
	rulesV6 := []RuleSpec{
		{
			ID:      "v6-allow",
			Action:  ActionAllow,
			CIDRs:   []string{"2001:db8::/48"},
			Enabled: true,
		},
		{
			ID:      "v6-deny",
			Action:  ActionDeny,
			CIDRs:   []string{"2001:db8::/48"},
			Enabled: true,
		},
	}
	pV6, err := Compile(rulesV6)
	if err != nil {
		t.Fatalf("Compile rulesV6 error: %v", err)
	}
	dV6 := pV6.Decide(netip.MustParseAddr("2001:db8::1"))
	if dV6.Allowed || dV6.RuleID != "v6-deny" {
		t.Fatalf("expected IPv6 deny to beat allow at same prefix: got %+v", dV6)
	}
}

func TestStableRuleID(t *testing.T) {
	// Equal prefix with same winning action: earliest enabled rule in configuration order wins.
	// Both deny:
	rulesDeny := []RuleSpec{
		{
			ID:      "deny-1",
			Action:  ActionDeny,
			CIDRs:   []string{"10.0.0.0/16"},
			Enabled: true,
		},
		{
			ID:      "deny-2",
			Action:  ActionDeny,
			CIDRs:   []string{"10.0.0.0/16"},
			Enabled: true,
		},
	}
	pDeny, err := Compile(rulesDeny)
	if err != nil {
		t.Fatalf("Compile error: %v", err)
	}
	dDeny := pDeny.Decide(netip.MustParseAddr("10.0.1.1"))
	if dDeny.Allowed || dDeny.RuleID != "deny-1" {
		t.Fatalf("expected earliest enabled deny ruleID 'deny-1', got %+v", dDeny)
	}

	// Both allow:
	rulesAllow := []RuleSpec{
		{
			ID:      "allow-1",
			Action:  ActionAllow,
			CIDRs:   []string{"10.0.0.0/16"},
			Enabled: true,
		},
		{
			ID:      "allow-2",
			Action:  ActionAllow,
			CIDRs:   []string{"10.0.0.0/16"},
			Enabled: true,
		},
	}
	pAllow, err := Compile(rulesAllow)
	if err != nil {
		t.Fatalf("Compile error: %v", err)
	}
	dAllow := pAllow.Decide(netip.MustParseAddr("10.0.1.1"))
	if !dAllow.Allowed || dAllow.RuleID != "allow-1" {
		t.Fatalf("expected earliest enabled allow ruleID 'allow-1', got %+v", dAllow)
	}

	// When earlier rule is disabled, next enabled rule supplies rule_id:
	rulesDisabledFirst := []RuleSpec{
		{
			ID:      "disabled-deny",
			Action:  ActionDeny,
			CIDRs:   []string{"10.0.0.0/16"},
			Enabled: false,
		},
		{
			ID:      "enabled-deny",
			Action:  ActionDeny,
			CIDRs:   []string{"10.0.0.0/16"},
			Enabled: true,
		},
	}
	pDisabledFirst, err := Compile(rulesDisabledFirst)
	if err != nil {
		t.Fatalf("Compile error: %v", err)
	}
	dDisabledFirst := pDisabledFirst.Decide(netip.MustParseAddr("10.0.1.1"))
	if dDisabledFirst.Allowed || dDisabledFirst.RuleID != "enabled-deny" {
		t.Fatalf("expected enabled-deny to win, got %+v", dDisabledFirst)
	}
}

func TestDisabledRules(t *testing.T) {
	// Rule is disabled: should not match.
	rules := []RuleSpec{
		{
			ID:      "disabled-allow",
			Action:  ActionAllow,
			CIDRs:   []string{"192.168.1.0/24"},
			Enabled: false,
		},
	}
	p, err := Compile(rules)
	if err != nil {
		t.Fatalf("Compile unexpected error: %v", err)
	}
	d := p.Decide(netip.MustParseAddr("192.168.1.1"))
	if d.Allowed || d.RuleID != "" {
		t.Fatalf("disabled rule matched unexpectedly: got %+v", d)
	}

	// Disabled rule with invalid CIDR must still fail syntax validation.
	badRules := []RuleSpec{
		{
			ID:      "disabled-bad-cidr",
			Action:  ActionAllow,
			CIDRs:   []string{"invalid-cidr"},
			Enabled: false,
		},
	}
	if _, err := Compile(badRules); err == nil {
		t.Fatal("Compile expected error for disabled rule with invalid CIDR, got nil")
	}

	// Disabled rule with invalid ID must fail syntax validation.
	badIDRules := []RuleSpec{
		{
			ID:      "-bad-id",
			Action:  ActionAllow,
			CIDRs:   []string{"10.0.0.0/8"},
			Enabled: false,
		},
	}
	if _, err := Compile(badIDRules); err == nil {
		t.Fatal("Compile expected error for disabled rule with invalid ID, got nil")
	}

	// Disabled rule with invalid action must fail syntax validation.
	badActionRules := []RuleSpec{
		{
			ID:      "valid-id",
			Action:  "drop",
			CIDRs:   []string{"10.0.0.0/8"},
			Enabled: false,
		},
	}
	if _, err := Compile(badActionRules); err == nil {
		t.Fatal("Compile expected error for disabled rule with invalid action, got nil")
	}

	// Disabled rule with empty CIDRs must fail syntax validation.
	emptyCIDRSRules := []RuleSpec{
		{
			ID:      "valid-id",
			Action:  ActionAllow,
			CIDRs:   nil,
			Enabled: false,
		},
	}
	if _, err := Compile(emptyCIDRSRules); err == nil {
		t.Fatal("Compile expected error for disabled rule with empty CIDRs, got nil")
	}
}

func TestCanonicalMasking(t *testing.T) {
	// Rule with host bits set should be masked to prefix base.
	rules := []RuleSpec{
		{
			ID:      "masked-rule",
			Action:  ActionAllow,
			CIDRs:   []string{"192.168.1.123/24"},
			Enabled: true,
		},
	}
	p, err := Compile(rules)
	if err != nil {
		t.Fatalf("Compile unexpected error: %v", err)
	}

	for _, host := range []string{"192.168.1.1", "192.168.1.254"} {
		d := p.Decide(netip.MustParseAddr(host))
		if !d.Allowed || d.RuleID != "masked-rule" {
			t.Fatalf("Decide(%s) = %+v, want allowed by masked-rule", host, d)
		}
	}
	dOutside := p.Decide(netip.MustParseAddr("192.168.2.1"))
	if dOutside.Allowed {
		t.Fatalf("Decide(192.168.2.1) unexpectedly allowed: %+v", dOutside)
	}
}

func TestIPv4MappedPeerUnmapping(t *testing.T) {
	rules := []RuleSpec{
		{
			ID:      "local-net",
			Action:  ActionAllow,
			CIDRs:   []string{"127.0.0.0/8"},
			Enabled: true,
		},
	}
	p, err := Compile(rules)
	if err != nil {
		t.Fatalf("Compile unexpected error: %v", err)
	}

	addr, err := ParsePeer("[::ffff:127.0.0.1]:8080")
	if err != nil {
		t.Fatalf("ParsePeer unexpected error: %v", err)
	}
	d := p.Decide(addr)
	if !d.Allowed || d.RuleID != "local-net" {
		t.Fatalf("mapped peer decide = %+v, want allowed by local-net", d)
	}

	// Even if an unmapped address is passed directly to Decide, Decide unmaps it.
	mappedDirect := netip.MustParseAddr("::ffff:127.0.0.1")
	d2 := p.Decide(mappedDirect)
	if !d2.Allowed || d2.RuleID != "local-net" {
		t.Fatalf("mapped direct decide = %+v, want allowed by local-net", d2)
	}
}

func TestIPv6PeerZones(t *testing.T) {
	rules := []RuleSpec{
		{
			ID:      "link-local",
			Action:  ActionAllow,
			CIDRs:   []string{"fe80::/64"},
			Enabled: true,
		},
	}
	p, err := Compile(rules)
	if err != nil {
		t.Fatalf("Compile unexpected error: %v", err)
	}

	addr, err := ParsePeer("[fe80::1%eth0]:1234")
	if err != nil {
		t.Fatalf("ParsePeer error: %v", err)
	}
	d := p.Decide(addr)
	if !d.Allowed || d.RuleID != "link-local" {
		t.Fatalf("zoned peer decide = %+v, want allowed by link-local", d)
	}
}

func TestSizeBounds(t *testing.T) {
	// Rule count: 256 is allowed.
	rules256 := make([]RuleSpec, MaxRules)
	for i := range MaxRules {
		rules256[i] = RuleSpec{
			ID:      fmt.Sprintf("rule-%d", i),
			Action:  ActionAllow,
			CIDRs:   []string{"10.0.0.1/32"},
			Enabled: false,
		}
	}
	if _, err := Compile(rules256); err != nil {
		t.Fatalf("Compile with 256 rules should succeed, got %v", err)
	}

	// Rule count: 257 is rejected.
	rules257 := make([]RuleSpec, MaxRules+1)
	for i := range MaxRules + 1 {
		rules257[i] = RuleSpec{
			ID:      fmt.Sprintf("rule-%d", i),
			Action:  ActionAllow,
			CIDRs:   []string{"10.0.0.1/32"},
			Enabled: false,
		}
	}
	if _, err := Compile(rules257); err == nil {
		t.Fatal("Compile with 257 rules should fail, got nil error")
	}

	// Total CIDRs: 1024 is allowed.
	cidrs1024 := make([]string, MaxCIDRs)
	for i := range MaxCIDRs {
		cidrs1024[i] = fmt.Sprintf("10.%d.%d.0/24", i/256, i%256)
	}
	rulesCIDRs1024 := []RuleSpec{
		{
			ID:      "big-rule",
			Action:  ActionAllow,
			CIDRs:   cidrs1024,
			Enabled: false,
		},
	}
	if _, err := Compile(rulesCIDRs1024); err != nil {
		t.Fatalf("Compile with 1024 CIDRs should succeed, got %v", err)
	}

	// Total CIDRs: 1025 is rejected.
	cidrs1025 := append(cidrs1024, "10.4.0.0/24")
	rulesCIDRs1025 := []RuleSpec{
		{
			ID:      "too-big-rule",
			Action:  ActionAllow,
			CIDRs:   cidrs1025,
			Enabled: false,
		},
	}
	if _, err := Compile(rulesCIDRs1025); err == nil {
		t.Fatal("Compile with 1025 CIDRs should fail, got nil error")
	}

	// Rule ID: exactly 64 valid characters is allowed.
	valid64ID := strings.Repeat("a", 64)
	rule64 := []RuleSpec{
		{
			ID:      valid64ID,
			Action:  ActionAllow,
			CIDRs:   []string{"10.0.0.0/8"},
			Enabled: true,
		},
	}
	if _, err := Compile(rule64); err != nil {
		t.Fatalf("Compile with 64-char ID should succeed, got %v", err)
	}

	// Rule ID: 65 characters is rejected.
	invalid65ID := strings.Repeat("a", 65)
	rule65 := []RuleSpec{
		{
			ID:      invalid65ID,
			Action:  ActionAllow,
			CIDRs:   []string{"10.0.0.0/8"},
			Enabled: true,
		},
	}
	if _, err := Compile(rule65); err == nil {
		t.Fatal("Compile with 65-char ID should fail, got nil error")
	}

	// Rule ID: invalid starting character.
	badStartRule := []RuleSpec{
		{
			ID:      "-invalid",
			Action:  ActionAllow,
			CIDRs:   []string{"10.0.0.0/8"},
			Enabled: true,
		},
	}
	if _, err := Compile(badStartRule); err == nil {
		t.Fatal("Compile with '-' starting ID should fail, got nil error")
	}

	// Duplicate rule ID rejected.
	dupRule := []RuleSpec{
		{
			ID:      "rule-1",
			Action:  ActionAllow,
			CIDRs:   []string{"10.0.0.0/8"},
			Enabled: true,
		},
		{
			ID:      "rule-1",
			Action:  ActionDeny,
			CIDRs:   []string{"192.168.0.0/16"},
			Enabled: true,
		},
	}
	if _, err := Compile(dupRule); err == nil {
		t.Fatal("Compile with duplicate rule ID should fail, got nil error")
	}
}

func TestCompileAllowlist(t *testing.T) {
	// Standard allowlist
	p, err := CompileAllowlist([]string{"127.0.0.0/8", "::1/128"})
	if err != nil {
		t.Fatalf("CompileAllowlist error: %v", err)
	}

	dLoop4 := p.Decide(netip.MustParseAddr("127.0.0.1"))
	if !dLoop4.Allowed || dLoop4.RuleID != "allowlist" {
		t.Fatalf("Decide(127.0.0.1) = %+v, want allowed with ruleID 'allowlist'", dLoop4)
	}

	dLoop6 := p.Decide(netip.MustParseAddr("::1"))
	if !dLoop6.Allowed || dLoop6.RuleID != "allowlist" {
		t.Fatalf("Decide(::1) = %+v, want allowed with ruleID 'allowlist'", dLoop6)
	}

	dExternal := p.Decide(netip.MustParseAddr("192.168.1.1"))
	if dExternal.Allowed || dExternal.RuleID != "" {
		t.Fatalf("Decide(192.168.1.1) = %+v, want denied with empty ruleID", dExternal)
	}

	// Empty allowlist compiles to intentional deny-all
	pEmpty, err := CompileAllowlist(nil)
	if err != nil {
		t.Fatalf("CompileAllowlist(nil) unexpected error: %v", err)
	}
	dEmpty := pEmpty.Decide(netip.MustParseAddr("127.0.0.1"))
	if dEmpty.Allowed {
		t.Fatalf("CompileAllowlist(nil) allowed 127.0.0.1, want deny-all")
	}

	// Malformed CIDR in allowlist fails compilation
	if _, err := CompileAllowlist([]string{"invalid"}); err == nil {
		t.Fatal("CompileAllowlist with invalid CIDR should fail")
	}

	// Mapped-IPv6 CIDR in allowlist fails compilation
	if _, err := CompileAllowlist([]string{"::ffff:127.0.0.1/120"}); err == nil {
		t.Fatal("CompileAllowlist with mapped-IPv6 CIDR should fail")
	}
}

func TestAllocationFreeDecide(t *testing.T) {
	rules := []RuleSpec{
		{
			ID:      "v4-broad",
			Action:  ActionAllow,
			CIDRs:   []string{"10.0.0.0/8"},
			Enabled: true,
		},
		{
			ID:      "v4-mid",
			Action:  ActionDeny,
			CIDRs:   []string{"10.1.0.0/16"},
			Enabled: true,
		},
		{
			ID:      "v4-host",
			Action:  ActionAllow,
			CIDRs:   []string{"10.1.2.3/32"},
			Enabled: true,
		},
		{
			ID:      "v6-net",
			Action:  ActionAllow,
			CIDRs:   []string{"2001:db8::/32"},
			Enabled: true,
		},
	}
	p, err := Compile(rules)
	if err != nil {
		t.Fatalf("Compile error: %v", err)
	}

	ip4 := netip.MustParseAddr("10.1.2.3")
	allocs4 := testing.AllocsPerRun(1000, func() {
		d := p.Decide(ip4)
		if !d.Allowed {
			t.Fatal("expected allowed")
		}
	})
	if allocs4 != 0 {
		t.Fatalf("Decide(IPv4) allocated %f times per run, want 0", allocs4)
	}

	ip6 := netip.MustParseAddr("2001:db8::99")
	allocs6 := testing.AllocsPerRun(1000, func() {
		d := p.Decide(ip6)
		if !d.Allowed {
			t.Fatal("expected allowed")
		}
	})
	if allocs6 != 0 {
		t.Fatalf("Decide(IPv6) allocated %f times per run, want 0", allocs6)
	}
}

func TestTelemetryTypesJSON(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	snapshot := SourceSnapshot{
		GeneratedAt: now,
		Clients: []SourceClient{
			{
				Address:             "192.0.2.1",
				Decision:            DecisionAllow,
				RuleID:              "local-clients",
				ActiveConnections:   3,
				AcceptedConnections: 10,
				DeniedConnections:   0,
				CapacityRejections:  0,
				LastSeen:            now,
			},
			{
				Address:             "198.51.100.2",
				Decision:            DecisionDeny,
				RuleID:              "",
				ActiveConnections:   0,
				AcceptedConnections: 0,
				DeniedConnections:   5,
				CapacityRejections:  1,
				LastSeen:            now,
			},
		},
	}

	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("json.Marshal(SourceSnapshot) error: %v", err)
	}

	jsonStr := string(data)
	// Verify RFC 3339 timestamp
	if !strings.Contains(jsonStr, `"2026-10-08T12:00:00Z"`) {
		t.Fatalf("expected RFC 3339 timestamp in JSON, got: %s", jsonStr)
	}
	// Verify exact field names
	expectedKeys := []string{
		`"generated_at"`,
		`"clients"`,
		`"address"`,
		`"decision"`,
		`"rule_id"`,
		`"active_connections"`,
		`"accepted_connections"`,
		`"denied_connections"`,
		`"capacity_rejections"`,
		`"last_seen"`,
	}
	for _, key := range expectedKeys {
		if !strings.Contains(jsonStr, key) {
			t.Fatalf("expected JSON to contain key %s, got: %s", key, jsonStr)
		}
	}

	// Verify unmarshaling round-trip
	var unmarshaled SourceSnapshot
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}
	if len(unmarshaled.Clients) != 2 {
		t.Fatalf("expected 2 clients, got %d", len(unmarshaled.Clients))
	}
	if unmarshaled.Clients[0].Address != "192.0.2.1" || unmarshaled.Clients[0].Decision != DecisionAllow {
		t.Fatalf("unexpected unmarshaled client 0: %+v", unmarshaled.Clients[0])
	}
	if unmarshaled.Clients[1].Address != "198.51.100.2" || unmarshaled.Clients[1].Decision != DecisionDeny {
		t.Fatalf("unexpected unmarshaled client 1: %+v", unmarshaled.Clients[1])
	}
}

func TestSocketIntegration(t *testing.T) {
	policy, err := Compile([]RuleSpec{
		{
			ID:      "loopback-v4",
			Action:  ActionAllow,
			CIDRs:   []string{"127.0.0.0/8"},
			Enabled: true,
		},
		{
			ID:      "loopback-v6",
			Action:  ActionAllow,
			CIDRs:   []string{"::1/128"},
			Enabled: true,
		},
	})
	if err != nil {
		t.Fatalf("Compile error: %v", err)
	}

	// Test with real IPv4 TCP socket listener
	l4, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen tcp4 error: %v", err)
	}
	defer func() { _ = l4.Close() }()

	serverAddr := l4.Addr().String()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := l4.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		peerStr := conn.RemoteAddr().String()
		peerIP, err := ParsePeer(peerStr)
		if err != nil {
			t.Errorf("ParsePeer(%q) error: %v", peerStr, err)
			return
		}

		decision := policy.Decide(peerIP)
		if !decision.Allowed || decision.RuleID != "loopback-v4" {
			t.Errorf("Decide(%s) = %+v, want allowed by loopback-v4", peerIP, decision)
		}
	}()

	clientConn, err := net.Dial("tcp4", serverAddr)
	if err != nil {
		t.Fatalf("Dial tcp4 error: %v", err)
	}
	_ = clientConn.Close()
	<-done

	// Test with real IPv6 TCP socket listener if supported
	if l6, err := net.Listen("tcp6", "[::1]:0"); err == nil {
		defer func() { _ = l6.Close() }()
		serverAddr6 := l6.Addr().String()
		done6 := make(chan struct{})
		go func() {
			defer close(done6)
			conn, err := l6.Accept()
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()

			peerStr := conn.RemoteAddr().String()
			peerIP, err := ParsePeer(peerStr)
			if err != nil {
				t.Errorf("ParsePeer(%q) error: %v", peerStr, err)
				return
			}

			decision := policy.Decide(peerIP)
			if !decision.Allowed || decision.RuleID != "loopback-v6" {
				t.Errorf("Decide(%s) = %+v, want allowed by loopback-v6", peerIP, decision)
			}
		}()

		if clientConn6, err := net.Dial("tcp6", serverAddr6); err == nil {
			_ = clientConn6.Close()
			<-done6
		}
	}
}
