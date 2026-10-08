// Package authguard bounds failed authentication attempts using the socket peer.
// IPv6 peers are grouped by /64.
package authguard

import (
	"crypto/sha256"
	"crypto/subtle"
	"net"
	"net/netip"
	"sync"
	"time"
)

const (
	failureLimit = 20
	clientLimit  = 256
	window       = time.Minute
)

type entry struct {
	failures int
	until    time.Time
}

// Limiter allows at most 20 failed attempts per peer in one minute. IPv6 peers
// are grouped by /64. Successful requests are unlimited unless temporarily blocked.
// Forwarded headers are never used; memory is bounded and excess peers fail closed.
type Limiter struct {
	mu    sync.Mutex
	peers map[string]entry
	now   func() time.Time
}

func New() *Limiter { return &Limiter{peers: make(map[string]entry), now: time.Now} }

// SecretEqual compares secrets in constant time without revealing their lengths.
func SecretEqual(a, b string) bool {
	aa, bb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(aa[:], bb[:]) == 1
}

func peer(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return "unknown"
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return "unknown"
	}
	ip = ip.Unmap()
	if ip.Is6() {
		return netip.PrefixFrom(ip, 64).Masked().String()
	}
	return ip.String()
}

// Allow must run before comparing credentials. A valid token does not bypass a
// temporary block, so each guess cannot still be tested after the limit is hit.
func (l *Limiter) Allow(address string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for key, e := range l.peers {
		if !now.Before(e.until) {
			delete(l.peers, key)
		}
	}
	e, exists := l.peers[peer(address)]
	if exists {
		return e.failures < failureLimit
	}
	return len(l.peers) < clientLimit
}

func (l *Limiter) Failure(address string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	key := peer(address)
	now := l.now()
	e, exists := l.peers[key]
	if !exists || !now.Before(e.until) {
		if !exists && len(l.peers) >= clientLimit {
			return
		}
		e = entry{until: now.Add(window)}
	}
	if e.failures < failureLimit {
		e.failures++
	}
	l.peers[key] = e
}
