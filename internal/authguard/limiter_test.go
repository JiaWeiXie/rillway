package authguard

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestWindowPeerIdentityAndCapacity(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := New()
	l.now = func() time.Time { return now }
	for range failureLimit {
		if !l.Allow("192.0.2.1:1234") {
			t.Fatal("blocked early")
		}
		l.Failure("192.0.2.1:1234")
	}
	if l.Allow("[::ffff:192.0.2.1]:5678") {
		t.Fatal("new socket or mapped IPv6 bypassed limit")
	}
	if !l.Allow("192.0.2.2:1234") {
		t.Fatal("unrelated peer blocked")
	}
	now = now.Add(window)
	if !l.Allow("192.0.2.1:1234") {
		t.Fatal("expired block retained")
	}
	for i := range clientLimit {
		l.Failure(fmt.Sprintf("[2001:db8:%x::1]:1234", i+1))
	}
	if l.Allow("192.0.2.3:1234") {
		t.Fatal("overflow must fail closed")
	}
	l.Failure("192.0.2.3:1234")
	if len(l.peers) != clientLimit {
		t.Fatal("unbounded peers")
	}
	now = now.Add(window)
	if !l.Allow("192.0.2.3:1234") || len(l.peers) != 0 {
		t.Fatal("expired peers not reclaimed")
	}
}

func TestIPv6PeersShareSlash64Budget(t *testing.T) {
	l := New()
	for i := range failureLimit {
		l.Failure(fmt.Sprintf("[2001:db8:1:1::%x]:1", i+1))
	}
	if l.Allow("[2001:db8:1:1::ffff]:1") {
		t.Fatal("IPv6 address rotation bypassed the /64 budget")
	}
	if !l.Allow("[2001:db8:1:2::1]:1") {
		t.Fatal("unrelated /64 blocked")
	}
}

func TestConcurrentFailures(t *testing.T) {
	l := New()
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() { l.Allow("192.0.2.1:1234"); l.Failure("192.0.2.1:1234") })
	}
	wg.Wait()
	if l.Allow("192.0.2.1:4321") {
		t.Fatal("concurrent guesses did not block peer")
	}
}
