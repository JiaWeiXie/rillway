package engine

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"rillway/internal/config"
	"slices"
	"sync/atomic"
	"time"
)

type groupRuntime struct {
	cfg     config.OutboundGroup
	counter atomic.Uint64
}

type healthState struct {
	lastSuccess         time.Time
	lastFailure         time.Time
	consecutiveFailures int
}

func jumpHash(key uint64, buckets int) int {
	if buckets <= 1 {
		return 0
	}
	b, j := int64(-1), int64(0)
	for j < int64(buckets) {
		b = j
		key = key*2862933555777941757 + 1
		j = int64(float64(b+1) * (float64(uint64(1)<<31) / float64((key>>33)+1)))
	}
	return int(b)
}

func memberHash(text string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(text))
	return h.Sum64()
}

// The target host is already normalized; retain the full hostname as its key.
func destinationKey(host string) string { return host }

func (e *Engine) availableLocked(id string, _ time.Time, _ config.Adaptive) bool {
	h := e.health[id]
	return h == nil || h.consecutiveFailures < 3 || h.lastSuccess.After(h.lastFailure)
}

func privateEligible(o config.Outbound, fixed bool) bool {
	switch o.Type {
	case "direct", "tailscale":
		return true
	case "wireguard":
		return !o.PublicInternet || fixed
	default:
		return false
	}
}

// Unavailable members remain last-resort attempts, in their original order.
func preferAvailable(ids []string, e *Engine, now time.Time, a config.Adaptive) []string {
	firstUnavailable := -1
	for i, id := range ids {
		if !e.availableLocked(id, now, a) {
			if firstUnavailable < 0 {
				firstUnavailable = i
			}
		} else if firstUnavailable >= 0 {
			ordered := make([]string, len(ids))
			copy(ordered, ids[:firstUnavailable])
			n := firstUnavailable
			for _, member := range ids[firstUnavailable:] {
				if e.availableLocked(member, now, a) {
					ordered[n] = member
					n++
				}
			}
			for _, member := range ids[firstUnavailable:] {
				if !e.availableLocked(member, now, a) {
					ordered[n] = member
					n++
				}
			}
			return ordered
		}
	}
	return ids
}

func rotate(ids []string, start int) []string {
	if start != 0 {
		slices.Reverse(ids[:start])
		slices.Reverse(ids[start:])
		slices.Reverse(ids)
	}
	return ids
}

// groupAttemptOrderLocked is the sole strategy boundary. Its caller holds mu.
func (e *Engine) groupAttemptOrderLocked(g *groupRuntime, t target, fixed bool, now time.Time) ([]string, error) {
	eligible := make([]string, 0, len(g.cfg.Members))
	private := privateHost(t.host, e.cfg.PAC)
	for _, id := range g.cfg.Members {
		if e.providers[id] == nil {
			continue
		}
		for _, o := range e.cfg.Outbounds {
			if o.ID == id {
				if o.Enabled && (!private || privateEligible(o, fixed)) {
					eligible = append(eligible, id)
				}
				break
			}
		}
	}
	if len(eligible) == 0 {
		return nil, fmt.Errorf("outbound group %q has no eligible member for this destination", g.cfg.ID)
	}
	switch g.cfg.Type {
	case "select":
		if !slices.Contains(eligible, g.cfg.Selected) {
			return nil, fmt.Errorf("selected member %q of group %q is unavailable for this destination", g.cfg.Selected, g.cfg.ID)
		}
		eligible[0] = g.cfg.Selected
		return eligible[:1], nil
	case "fallback":
		return preferAvailable(eligible, e, now, e.cfg.Adaptive), nil
	case "load-balance":
		var start int
		switch g.cfg.Strategy {
		case "round-robin":
			start = int((g.counter.Add(1) - 1) % uint64(len(eligible)))
		case "consistent-hashing":
			keyText := destinationKey(t.host)
			if g.cfg.Key == "source" && t.source.IsValid() {
				keyText = t.source.String()
			}
			start = jumpHash(memberHash(keyText), len(eligible))
		default:
			return nil, fmt.Errorf("unknown strategy %q for outbound group %q", g.cfg.Strategy, g.cfg.ID)
		}
		return preferAvailable(rotate(eligible, start), e, now, e.cfg.Adaptive), nil
	default:
		return nil, fmt.Errorf("unknown outbound group type %q", g.cfg.Type)
	}
}

func (e *Engine) recordHealth(id string, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.recordHealthLocked(id, err)
}

func (e *Engine) recordHealthLocked(id string, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	if e.health == nil {
		e.health = make(map[string]*healthState)
	}
	h := e.health[id]
	if h == nil {
		h = &healthState{}
		e.health[id] = h
	}
	if err == nil {
		h.lastSuccess = e.now()
		h.consecutiveFailures = 0
	} else {
		h.lastFailure = e.now()
		h.consecutiveFailures++
	}
}
