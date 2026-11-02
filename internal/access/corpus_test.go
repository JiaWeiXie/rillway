package access_test

import (
	"fmt"
	"math/rand"
	"net/netip"
	"rillway/internal/access"
	"strings"
	"testing"
)

// linearDecision is deliberately test-only: no production Compile, ParsePrefix,
// trie or Decide calls. The caller supplies valid rules and canonical peers.
func linearDecision(rules []access.RuleSpec, peer netip.Addr) access.Decision {
	best := -1
	var result access.Decision
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		for _, raw := range rule.CIDRs {
			prefix := netip.MustParsePrefix(strings.TrimSpace(raw)).Masked()
			if !prefix.Contains(peer) || prefix.Bits() < best {
				continue
			}
			allow := rule.Action == "allow"
			if prefix.Bits() > best || (result.Allowed && !allow) {
				best = prefix.Bits()
				result = access.Decision{Allowed: allow, RuleID: rule.ID}
			}
		}
	}
	return result
}

func corpusRule(id, action string, enabled bool, cidrs ...string) access.RuleSpec {
	return access.RuleSpec{ID: id, Action: action, Enabled: enabled, CIDRs: cidrs}
}

func TestSourceACLBypassCorpus(t *testing.T) {
	t.Run("policy", func(t *testing.T) {
		cases := []struct {
			name  string
			rules []access.RuleSpec
			peer  string
			want  access.Decision
		}{
			{"default-v4", nil, "192.0.2.1", access.Decision{}},
			{"default-v6", nil, "2001:db8::1", access.Decision{}},
			{"v4-root", []access.RuleSpec{corpusRule("root", "allow", true, "0.0.0.0/0")}, "255.255.255.255", access.Decision{Allowed: true, RuleID: "root"}},
			{"v6-root", []access.RuleSpec{corpusRule("root", "allow", true, "::/0")}, "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", access.Decision{Allowed: true, RuleID: "root"}},
			{"narrow-allow", []access.RuleSpec{corpusRule("wide", "deny", true, "0.0.0.0/0"), corpusRule("narrow", "allow", true, "192.0.2.0/24")}, "192.0.2.1", access.Decision{Allowed: true, RuleID: "narrow"}},
			{"equal-deny-later", []access.RuleSpec{corpusRule("allow", "allow", true, "2001:db8::/32"), corpusRule("deny", "deny", true, "2001:db8::/32")}, "2001:db8::1", access.Decision{RuleID: "deny"}},
			{"equal-deny-first", []access.RuleSpec{corpusRule("deny", "deny", true, "192.0.2.0/24"), corpusRule("allow", "allow", true, "192.0.2.0/24")}, "192.0.2.1", access.Decision{RuleID: "deny"}},
			{"earliest-allow", []access.RuleSpec{corpusRule("first", "allow", true, "192.0.2.129/24"), corpusRule("second", "allow", true, "192.0.2.0/24")}, "192.0.2.1", access.Decision{Allowed: true, RuleID: "first"}},
			{"earliest-deny", []access.RuleSpec{corpusRule("first", "deny", true, "::/0"), corpusRule("second", "deny", true, "::/0")}, "2001:db8::1", access.Decision{RuleID: "first"}},
			{"disabled", []access.RuleSpec{corpusRule("disabled", "allow", false, "0.0.0.0/0")}, "192.0.2.1", access.Decision{}},
			{"duplicate-cidr", []access.RuleSpec{corpusRule("same", "allow", true, "192.0.2.1/24", "192.0.2.0/24")}, "192.0.2.1", access.Decision{Allowed: true, RuleID: "same"}},
			{"families-separate", []access.RuleSpec{corpusRule("v4", "allow", true, "0.0.0.0/0")}, "2001:db8::1", access.Decision{}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				peer := netip.MustParseAddr(tc.peer)
				if got := linearDecision(tc.rules, peer); got != tc.want {
					t.Fatalf("oracle = %+v, hand-checked expectation = %+v", got, tc.want)
				}
				compareCorpus(t, tc.rules, peer)
			})
		}
	})
	t.Run("boundaries", func(t *testing.T) {
		for _, raw := range []string{"0.0.0.1/0", "127.255.255.255/1", "192.0.2.1/31", "192.0.2.1/32", "::1/0", "7fff::1/1", "2001:db8::1/127", "2001:db8::1/128"} {
			t.Run(raw, func(t *testing.T) {
				prefix := netip.MustParsePrefix(raw).Masked()
				for i, peer := range boundaryPeers(prefix) {
					t.Run(fmt.Sprintf("peer-%d", i), func(t *testing.T) {
						compareCorpus(t, []access.RuleSpec{corpusRule("boundary", "allow", true, raw)}, peer)
					})
				}
			})
		}
	})
	t.Run("seeded", func(t *testing.T) {
		// Fixed seed and bounds keep failures reproducible and ordinary CI finite.
		rng := rand.New(rand.NewSource(8171))
		for n := range 32 {
			t.Run(fmt.Sprintf("set-%02d", n), func(t *testing.T) {
				var rules []access.RuleSpec
				var peers []netip.Addr
				for i := range 12 {
					var b [16]byte
					_, _ = rng.Read(b[:])
					addr := netip.AddrFrom16(b)
					bits := []int{0, 1, 32, 64, 127, 128}[rng.Intn(6)]
					if i%2 == 0 {
						addr = netip.AddrFrom4([4]byte(b[:4]))
						bits = []int{0, 1, 24, 31, 32}[rng.Intn(5)]
					}
					prefix := netip.PrefixFrom(addr, bits)
					rules = append(rules, corpusRule(fmt.Sprintf("r%d", i), []string{"allow", "deny"}[rng.Intn(2)], i%5 != 0, prefix.String()))
					peers = append(peers, addr)
					peers = append(peers, boundaryPeers(prefix.Masked())...)
				}
				for i, peer := range peers {
					t.Run(fmt.Sprintf("peer-%03d", i), func(t *testing.T) { compareCorpus(t, rules, peer) })
				}
			})
		}
	})
	t.Run("validation", testCorpusValidation)
	t.Run("peer", testCorpusPeers)
}

func compareCorpus(t *testing.T, rules []access.RuleSpec, peer netip.Addr) {
	t.Helper()
	policy, err := access.Compile(rules)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := policy.Decide(peer), linearDecision(rules, peer); got != want {
		t.Fatalf("peer %s: trie = %+v, linear = %+v; rules = %+v", peer, got, want, rules)
	}
}

func boundaryPeers(prefix netip.Prefix) []netip.Addr {
	first := prefix.Addr()
	b := first.As16()
	start := prefix.Bits()
	if first.Is4() {
		start += 96
	}
	for bit := start; bit < 128; bit++ {
		b[bit/8] |= 1 << (7 - bit%8)
	}
	last := netip.AddrFrom16(b).Unmap()
	peers := []netip.Addr{first, last}
	for _, peer := range []netip.Addr{first.Prev(), first.Next(), last.Prev(), last.Next()} {
		if peer.IsValid() {
			peers = append(peers, peer)
		}
	}
	return peers
}

func testCorpusValidation(t *testing.T) {
	cases := []struct {
		name  string
		rules []access.RuleSpec
	}{
		{"disabled-invalid-prefix", []access.RuleSpec{corpusRule("off", "allow", false, "not-a-prefix")}},
		{"disabled-invalid-action", []access.RuleSpec{corpusRule("off", "ALLOW", false, "192.0.2.0/24")}},
		{"disabled-empty-cidrs", []access.RuleSpec{corpusRule("off", "allow", false)}},
		{"duplicate-id", []access.RuleSpec{corpusRule("same", "allow", true, "::/0"), corpusRule("same", "deny", false, "::/0")}},
		{"invalid-id", []access.RuleSpec{corpusRule("bad id", "allow", true, "::/0")}},
		{"mapped-prefix", []access.RuleSpec{corpusRule("bad", "allow", true, "::ffff:192.0.2.1/128")}},
		{"zoned-prefix", []access.RuleSpec{corpusRule("bad", "allow", true, "fe80::%eth0/64")}},
		{"prefix-length-v4", []access.RuleSpec{corpusRule("bad", "allow", true, "192.0.2.0/33")}},
		{"prefix-length-v6", []access.RuleSpec{corpusRule("bad", "allow", true, "::/129")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if policy, err := access.Compile(tc.rules); err == nil || policy != nil {
				t.Fatal("invalid rules compiled", policy, err)
			}
		})
	}
	for _, kind := range []string{"rules", "cidrs"} {
		for _, over := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s-over-%t", kind, over), func(t *testing.T) {
				// Literal documented limits: do not move expectations with production constants.
				n := 256
				if kind == "cidrs" {
					n = 1024
				}
				if over {
					n++
				}
				var rules []access.RuleSpec
				if kind == "rules" {
					for i := range n {
						rules = append(rules, corpusRule(fmt.Sprintf("r%d", i), "allow", true, "192.0.2.0/24"))
					}
				} else {
					rules = []access.RuleSpec{corpusRule("many", "allow", false, strings.Fields(strings.Repeat("192.0.2.0/24 ", n))...)}
				}
				_, err := access.Compile(rules)
				if (err != nil) != over {
					t.Fatalf("over=%t, error=%v", over, err)
				}
			})
		}
	}
}

func testCorpusPeers(t *testing.T) {
	rules := []access.RuleSpec{corpusRule("v4", "allow", true, "192.0.2.1/32"), corpusRule("v6", "deny", true, "2001:db8::1/128"), corpusRule("link", "allow", true, "fe80::1/128")}
	policy, err := access.Compile(rules)
	if err != nil {
		t.Fatal(err)
	}
	// These are parser inputs, not proof that arbitrary strings can come from a
	// kernel TCP peer. PR2 exercises actual socket admission separately.
	cases := []struct{ name, input, want string }{
		{"socket-v4-port1", "192.0.2.1:1", "192.0.2.1"},
		{"socket-v4-port65535", "192.0.2.1:65535", "192.0.2.1"},
		{"socket-mapped", "[::ffff:192.0.2.1]:1234", "192.0.2.1"},
		{"socket-v6", "[2001:db8::1]:443", "2001:db8::1"},
		{"socket-v6-zone", "[fe80::1%test0]:53", "fe80::1"},
		{"socket-denied-neighbor", "192.0.2.2:1", "192.0.2.2"},
		{"string-whitespace", " \t192.0.2.1:80\r\n", "192.0.2.1"},
		{"string-bare", "192.0.2.1", "192.0.2.1"},
		{"string-mapped", "::ffff:c000:201", "192.0.2.1"},
		{"string-repeated-brackets", "[[192.0.2.1]]", "192.0.2.1"},
		{"string-unbalanced-bracket", "[2001:db8::1", "2001:db8::1"},
		{"string-zone", "fe80::1%test0", "fe80::1"},
		{"invalid-empty", "", ""},
		{"invalid-bracketed-port", "[[192.0.2.1]]:80", ""},
		{"invalid-port-overflow", "192.0.2.1:65536", ""},
		{"invalid-port-negative", "192.0.2.1:-1", ""},
		{"invalid-port-hex", "192.0.2.1:0x50", ""},
		{"invalid-port-empty", "192.0.2.1:", ""},
		{"invalid-short-v4", "192.1:80", ""},
		{"invalid-octal-v4", "0300.0.2.1:80", ""},
		{"invalid-hex-v4", "0xc0000201:80", ""},
		{"invalid-unicode", "１９２.0.2.1:80", ""},
		{"invalid-control", "192.0.2.1\x00:80", ""},
		{"invalid-newline", "192.0.2.1\n:80", ""},
		{"invalid-header", "192.0.2.1:80, 127.0.0.1", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			peer, err := access.ParsePeer(tc.input)
			if tc.want == "" {
				if err == nil || peer.IsValid() {
					t.Fatal("malformed parser input accepted", peer, err)
				}
				return
			}
			want := netip.MustParseAddr(tc.want)
			if err != nil || peer != want {
				t.Fatalf("peer=%v error=%v, want=%v", peer, err, want)
			}
			if got, expected := policy.Decide(peer), linearDecision(rules, want); got != expected {
				t.Fatalf("decision=%+v, expected=%+v", got, expected)
			}
		})
	}
}
