package hostlocal

import (
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"testing"
)

func TestIndependentActualAndHypotheticalCounters(t *testing.T) {
	for _, tc := range []struct {
		mode, source, target string
		actual, hypothetical uint64
	}{
		{Open, "192.0.2.2", "127.0.0.1:3000", 0, 1},
		{Strict, "127.0.0.1", "127.0.0.1:3000", 1, 0},
		{Open, "127.0.0.1", "169.254.169.254:80", 1, 1},
		{LocalClientsOnly, "192.0.2.2", "127.0.0.1:3000", 1, 1},
	} {
		t.Run(tc.mode+tc.source+tc.target, func(t *testing.T) {
			p := policy(t, tc.mode)
			s := NewState(p)
			dst, source := netip.MustParseAddrPort(tc.target), netip.MustParseAddr(tc.source)
			e := s.Begin(source, dst.Port())
			d := p.CheckIP(dst, source)
			for range 4 {
				e.ObserveCandidate(dst, d)
			}
			e.Finalize(dst, d)
			var err error
			if d.Denied {
				err = ErrDenied
			}
			e.Complete(err)
			e.Complete(err)
			got := s.Snapshot()
			if got.ActualDenials != tc.actual || got.HypotheticalLocalClientsOnlyDenials != tc.hypothetical || len(got.Recent) != 1 {
				t.Fatal(got)
			}
			if source.IsLoopback() && len(got.ExternalHostLocalByPort) != 0 {
				t.Fatal("local source in external port counters", got)
			}
		})
	}
}

func TestOpenScopedExceptionCountersIncludingBaseline(t *testing.T) {
	for _, target := range []string{"127.0.0.1:3000", "169.254.169.254:80"} {
		for _, source := range []string{"192.0.2.2", "198.51.100.2"} {
			dst, src := netip.MustParseAddrPort(target), netip.MustParseAddr(source)
			p := policy(t, Open, RuleSpec{IP: dst.Addr(), Lo: dst.Port(), Hi: dst.Port(), From: []string{"192.0.2.0/24"}})
			s := NewState(p)
			e := s.Begin(src, dst.Port())
			e.Finalize(dst, p.CheckIP(dst, src))
			e.Complete(nil)
			got := s.Snapshot()
			var wantHypothetical, wantActual uint64
			if source == "198.51.100.2" {
				wantHypothetical = 1
				if IsBaseline(dst.Addr()) {
					wantActual = 1
				}
			}
			if got.ActualDenials != wantActual || got.HypotheticalLocalClientsOnlyDenials != wantHypothetical {
				t.Fatal(target, source, got)
			}
		}
	}
}

func TestCandidateAttemptsAreNotIngressDenials(t *testing.T) {
	source := netip.MustParseAddr("192.0.2.2")
	local := netip.MustParseAddrPort("127.0.0.1:3000")
	public := netip.MustParseAddrPort("198.51.100.2:3000")
	for _, tc := range []struct {
		name             string
		allowed, success bool
		err              error
		count            uint64
	}{
		{"all blocked", false, false, fmt.Errorf("wrapped: %w", ErrDenied), 1},
		{"mixed success", true, true, nil, 0},
		{"mixed transport failure", true, false, errors.New("connect failed"), 0},
		{"mixed surfaces denied candidate", true, false, fmt.Errorf("wrapped: %w", ErrDenied), 0},
		{"DNS failed", false, false, errors.New("lookup failed"), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := policy(t, LocalClientsOnly)
			s := NewState(p)
			e := s.Begin(source, 3000)
			e.ObserveCandidate(local, p.CheckIP(local, source))
			if tc.allowed {
				e.ObserveCandidate(public, p.CheckIP(public, source))
			}
			if tc.success {
				e.Finalize(public, p.CheckIP(public, source))
			}
			e.Complete(tc.err)
			got := s.Snapshot()
			if got.ActualDenials != tc.count || got.HypotheticalLocalClientsOnlyDenials != tc.count {
				t.Fatal(got)
			}
		})
	}
}

func TestAllActualCandidatesDeniedUsesHypotheticalCandidateSet(t *testing.T) {
	source := netip.MustParseAddr("127.0.0.1")
	local := netip.MustParseAddrPort("127.0.0.1:3000")
	baseline := netip.MustParseAddrPort("169.254.169.254:3000")
	otherBaseline := netip.MustParseAddrPort("[fe80::1]:3000")
	for _, tc := range []struct {
		name         string
		first, last  netip.AddrPort
		hypothetical uint64
	}{
		{"local then baseline", local, baseline, 0},
		{"baseline then local", baseline, local, 0},
		{"all baseline", baseline, otherBaseline, 1},
	} {
		for _, concurrent := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/concurrent=%t", tc.name, concurrent), func(t *testing.T) {
				p := policy(t, Strict)
				s := NewState(p)
				e := s.Begin(source, 3000)
				observe := func(dst netip.AddrPort) { e.ObserveCandidate(dst, p.CheckIP(dst, source)) }
				if concurrent {
					var wg sync.WaitGroup
					for range 32 {
						wg.Go(func() { observe(tc.first) })
						wg.Go(func() { observe(tc.last) })
					}
					wg.Wait()
				} else {
					observe(tc.first)
					observe(tc.last)
				}
				var wg sync.WaitGroup
				for range 8 {
					wg.Go(func() { e.Complete(fmt.Errorf("wrapped: %w", ErrDenied)) })
				}
				wg.Wait()
				got := s.Snapshot()
				if got.ActualDenials != 1 || got.HypotheticalLocalClientsOnlyDenials != tc.hypothetical || len(got.Recent) != 1 || len(got.ExternalHostLocalByPort) != 0 {
					t.Fatal(got)
				}
				if !got.Recent[0].ActualDenial || got.Recent[0].HypotheticalLocalClientsOnlyDenial != (tc.hypothetical == 1) {
					t.Fatal("recent event disagrees with aggregate counters", got)
				}
			})
		}
	}
}

func TestFinalizedDenialRetainsAuthoritativeIdentity(t *testing.T) {
	source := netip.MustParseAddr("127.0.0.1")
	p := policy(t, Strict)
	s := NewState(p)
	e := s.Begin(source, 3000)
	local := netip.MustParseAddrPort("127.0.0.1:3000")
	baseline := netip.MustParseAddrPort("169.254.169.254:3000")
	e.ObserveCandidate(local, p.CheckIP(local, source))
	e.Finalize(baseline, p.CheckIP(baseline, source))
	e.Complete(ErrDenied)
	got := s.Snapshot()
	if got.ActualDenials != 1 || got.HypotheticalLocalClientsOnlyDenials != 1 || len(got.Recent) != 1 || got.Recent[0].Destination != baseline.String() {
		t.Fatal(got)
	}
}

func TestEventsBoundedAndSnapshotIndependent(t *testing.T) {
	p := policy(t, Open)
	s := NewState(p)
	source := netip.MustParseAddr("192.0.2.2")
	for port := uint16(1); port <= MaxPortCounters+12; port++ {
		dst := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), port)
		e := s.Begin(source, port)
		e.Finalize(dst, p.CheckIP(dst, source))
		e.Complete(nil)
	}
	got := s.Snapshot()
	if got.ActualDenials != 0 || got.HypotheticalLocalClientsOnlyDenials != MaxPortCounters+12 || len(got.Recent) != MaxRecent || len(got.ExternalHostLocalByPort) != MaxPortCounters || got.PortOverflow.HypotheticalLocalClientsOnlyDenials != 12 {
		t.Fatal(got)
	}
	got.Recent[0].Reason = "mutated"
	got.ExternalHostLocalByPort[0].ActualDenials = 999
	next := s.Snapshot()
	if next.Recent[0].Reason == "mutated" || next.ExternalHostLocalByPort[0].ActualDenials != 0 {
		t.Fatal("snapshot exposed mutable state")
	}
}

func TestConcurrentPolicyPublicationAndIdempotentEvents(t *testing.T) {
	open, strict := policy(t, Open), policy(t, Strict)
	s := NewState(open)
	source := netip.MustParseAddr("192.0.2.2")
	dst := netip.MustParseAddrPort("127.0.0.1:3000")
	e := s.Begin(source, 3000)
	e.Finalize(dst, strict.CheckIP(dst, source))
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			for range 50 {
				s.Publish(strict)
				_ = s.Load().CheckIP(dst, source)
				e.ObserveCandidate(dst, open.CheckIP(dst, source))
				e.Complete(ErrDenied)
				_ = s.Snapshot()
				s.Publish(open)
			}
		})
	}
	wg.Wait()
	got := s.Snapshot()
	if got.ActualDenials != 1 || got.HypotheticalLocalClientsOnlyDenials != 1 {
		t.Fatal("event was counted repeatedly", got)
	}
	before := got
	for range 10 {
		_ = s.Load().CheckIP(dst, source)
	}
	if after := s.Snapshot(); before.ActualDenials != after.ActualDenials || before.HypotheticalLocalClientsOnlyDenials != after.HypotheticalLocalClientsOnlyDenials {
		t.Fatal("pure checks recorded observations")
	}
}
