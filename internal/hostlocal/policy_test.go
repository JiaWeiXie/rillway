package hostlocal

import (
	"net/netip"
	"testing"
)

func policy(t *testing.T, mode string, rules ...RuleSpec) *Policy {
	t.Helper()
	p, err := Compile(mode, rules, []netip.Addr{netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("fd00::10")})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestModeSourceDestinationMatrix(t *testing.T) {
	sources := []struct {
		ip    string
		local bool
	}{
		{"127.0.0.2", true},
		{"::1", true},
		{"::ffff:127.0.0.1", true},
		{"192.0.2.10", false},
		{"172.17.0.1", false},
		{"fd00::10", false},
		{"198.51.100.7", false},
		{"", false},
	}
	targets := []struct {
		ip              string
		local, baseline bool
	}{
		{"127.99.1.2", true, false},
		{"::1", true, false},
		{"::ffff:127.0.0.1", true, false},
		{"0.0.0.0", true, false},
		{"::", true, false},
		{"192.0.2.10", true, false},
		{"fd00::10", true, false},
		{"169.254.169.254", true, true},
		{"fe80::1%eth0", true, true},
		{"ff02::1", true, true},
		{"fd00:ec2::254", true, true},
		{"192.0.0.192", true, true},
		{"100.100.100.200", true, true},
		{"172.17.0.2", false, false},
		{"fd00::20", false, false},
		{"198.51.100.20", false, false},
	}
	for _, mode := range []string{"", Open, LocalClientsOnly, Strict} {
		p := policy(t, mode)
		for _, s := range sources {
			var source netip.Addr
			if s.ip != "" {
				source = netip.MustParseAddr(s.ip)
			}
			for _, target := range targets {
				t.Run(mode+"/"+s.ip+"/"+target.ip, func(t *testing.T) {
					dst := netip.AddrPortFrom(netip.MustParseAddr(target.ip), 3000)
					got := p.CheckIP(dst, source)
					want := target.baseline || target.local && (mode == Strict || mode != Open && !s.local)
					hypothetical := target.local && (target.baseline || !s.local)
					if got.Denied != want || got.HypotheticalDenied != hypothetical || got.HostLocal != target.local || got.NeedsDirectIP {
						t.Fatalf("decision=%+v want denied=%v hypothetical=%v local=%v", got, want, hypothetical, target.local)
					}
				})
			}
		}
	}
}

func TestExceptionUnionSourceRangeAndImmutability(t *testing.T) {
	from := []string{"192.0.2.0/24"}
	specs := []RuleSpec{
		{IP: netip.MustParseAddr("::ffff:127.0.0.1"), Lo: 3000, Hi: 3002, From: from},
		{IP: netip.MustParseAddr("127.0.0.1"), Lo: 3001, Hi: 3001, From: []string{"198.51.100.0/24"}},
		{IP: netip.MustParseAddr("fe80::1%eth0"), Lo: 8080, Hi: 8080, AnySource: true},
	}
	p := policy(t, Strict, specs...)
	from[0] = "203.0.113.0/24"
	specs[0].IP = netip.MustParseAddr("203.0.113.1")
	for _, tc := range []struct {
		target, source string
		denied         bool
	}{
		{"127.0.0.1:2999", "192.0.2.2", true},
		{"127.0.0.1:3000", "192.0.2.2", false},
		{"127.0.0.1:3002", "192.0.2.2", false},
		{"127.0.0.1:3003", "192.0.2.2", true},
		{"127.0.0.1:3000", "198.51.100.2", true},
		{"127.0.0.1:3001", "198.51.100.2", false},
		{"127.0.0.1:3000", "203.0.113.2", true},
		{"127.0.0.1:3000", "::ffff:192.0.2.2", false},
		{"[fe80::1%other]:8080", "203.0.113.2", false},
	} {
		d := p.CheckIP(netip.MustParseAddrPort(tc.target), netip.MustParseAddr(tc.source))
		if d.Denied != tc.denied || !tc.denied && d.HypotheticalDenied {
			t.Fatalf("%+v: %+v", tc, d)
		}
	}
	if !p.CheckIP(netip.MustParseAddrPort("127.0.0.1:3000"), netip.Addr{}).Denied {
		t.Fatal("unknown source matched a scoped exception")
	}
	if p.CheckIP(netip.MustParseAddrPort("[fe80::1]:8080"), netip.Addr{}).Denied {
		t.Fatal("omitted source did not mean every source")
	}
}

func TestLocalhostNeedsVerifiedNumericException(t *testing.T) {
	source := netip.MustParseAddr("192.0.2.2")
	p := policy(t, Strict, RuleSpec{IP: netip.MustParseAddr("127.0.0.1"), Lo: 3000, Hi: 3000, AnySource: true})
	for _, address := range []string{"LOCALHOST.:3000", "sub.localhost:3000", "sub.LOCALHOST.:3000"} {
		if got := p.CheckAddress(address, source); !got.NeedsDirectIP || got.Denied || got.HypotheticalDenied {
			t.Fatal(address, got)
		}
		if got := p.CheckResolved(address, netip.MustParseAddrPort("127.0.0.1:3000"), source); got.Denied || got.HypotheticalDenied {
			t.Fatal(address, got)
		}
		for _, numeric := range []string{"[::1]:3000", "198.51.100.2:3000"} {
			if got := p.CheckResolved(address, netip.MustParseAddrPort(numeric), source); !got.Denied {
				t.Fatal("invented alias permission", address, numeric, got)
			}
		}
		if got := p.CheckRemoteName(address, source); !got.Denied || got.Reason != "unverified_localhost_exception" {
			t.Fatal(got)
		}
	}
	for _, name := range []string{"notlocalhost:3000", "localhost.example:3000", "ordinary.invalid:3000"} {
		if got := p.CheckAddress(name, source); got.HostLocal || got.NeedsDirectIP || got.Denied {
			t.Fatal("invented remote DNS identity", name, got)
		}
	}
	if policy(t, Open).CheckRemoteName("localhost:3000", source).Denied {
		t.Fatal("open mode newly denied permitted remote name")
	}
	if policy(t, LocalClientsOnly).CheckRemoteName("localhost:3000", netip.MustParseAddr("::1")).Denied {
		t.Fatal("loopback source newly denied permitted remote name")
	}
	if !p.CheckResolved("ordinary.invalid:3000", netip.MustParseAddrPort("[::1]:3000"), source).Denied {
		t.Fatal("direct resolved local address escaped strict")
	}
}

func TestCompileRejectsInvalidRules(t *testing.T) {
	valid := RuleSpec{IP: netip.MustParseAddr("127.0.0.1"), Lo: 1, Hi: 1, AnySource: true}
	for _, spec := range []RuleSpec{
		{},
		{IP: valid.IP, Lo: 0, Hi: 1, AnySource: true},
		{IP: valid.IP, Lo: 2, Hi: 1, AnySource: true},
		{IP: valid.IP, Lo: 1, Hi: 1},
		{IP: valid.IP, Lo: 1, Hi: 1, From: []string{"not-cidr"}},
		{IP: valid.IP, Lo: 1, Hi: 1, From: []string{"::ffff:192.0.2.0/120"}},
		{IP: valid.IP, Lo: 1, Hi: 1, AnySource: true, From: []string{"0.0.0.0/0"}},
	} {
		if _, err := Compile(Strict, []RuleSpec{spec}, nil); err == nil {
			t.Fatal("accepted invalid rule", spec)
		}
	}
	if _, err := Compile("typo", nil, nil); err == nil {
		t.Fatal("accepted invalid mode")
	}
	if _, err := Compile(Open, make([]RuleSpec, MaxRules+1), nil); err == nil {
		t.Fatal("unbounded rules")
	}
	if !policy(t, Open).CheckIP(netip.AddrPort{}, netip.Addr{}).Denied {
		t.Fatal("invalid target allowed")
	}
}
