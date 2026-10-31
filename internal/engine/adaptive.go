package engine

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"rillway/internal/config"
	"rillway/internal/hostlocal"
	"rillway/internal/outbound"
	"slices"
	"sort"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/publicsuffix"
)

type sample struct {
	sequence uint64
	family   string
	at       time.Time
	latency  time.Duration
	success  bool
	failure  bool
}
type adaptiveState struct {
	address, probeAddress         string
	network, current, reason      string
	rule, family, initial         string
	matched                       bool
	candidates                    []string
	samples                       map[string][]sample
	lastUsed, switched, lastProbe time.Time
	winner                        string
	winnerEvidence, nextSample    uint64
	streak, probeIndex            int
	target                        target // Ingress of the last published probe address, revalidated on Update.
}

func adaptiveDefaults(a config.Adaptive) config.Adaptive {
	if a.WindowSeconds <= 0 {
		a.WindowSeconds = 600
	}
	if a.MinSamples < 3 {
		a.MinSamples = 3
	}
	if a.ImprovementPercent <= 0 {
		a.ImprovementPercent = 25
	}
	if a.ImprovementMillis < 0 {
		a.ImprovementMillis = 50
	}
	if a.CooldownSeconds <= 0 {
		a.CooldownSeconds = 600
	}
	if a.ProbesPerMinute <= 0 || a.ProbesPerMinute > 12 {
		a.ProbesPerMinute = 12
	}
	if a.ProbeConcurrency <= 0 || a.ProbeConcurrency > 2 {
		a.ProbeConcurrency = 2
	}
	if a.ProbeTimeoutSeconds <= 0 || a.ProbeTimeoutSeconds > 4 {
		a.ProbeTimeoutSeconds = 4
	}
	return a
}

func adaptiveGroupAddress(address string) string {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return address
	}
	host = normalize(host)
	if host == "" {
		return address
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return net.JoinHostPort(host, port)
	}
	if etld1, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil && etld1 != "" {
		return net.JoinHostPort(etld1, port)
	}
	return net.JoinHostPort(host, port)
}

func adaptiveDestinationKey(address, network string, r route) string {
	if r.matched {
		return address + "|" + network + "|" + r.rule
	}
	return address + "|" + network
}

func (e *Engine) destinationLocked(key, address, network string, r route) *adaptiveState {
	if d := e.destinations[key]; d != nil && sameAdaptivePolicy(d, r) {
		return d
	}
	if e.destinations[key] == nil && len(e.destinations) >= maxDestinations {
		var oldestKey string
		var oldest time.Time
		for k, d := range e.destinations {
			if oldest.IsZero() || d.lastUsed.Before(oldest) {
				oldestKey, oldest = k, d.lastUsed
			}
		}
		delete(e.destinations, oldestKey)
	}
	d := newAdaptiveState(address, network, r, e.now())
	e.destinations[key] = d
	return d
}

// initialOutbound is the configured starting point before any learning.
func initialOutbound(r route) string {
	for _, id := range r.candidates {
		if id == r.outbound {
			return id
		}
	}
	return r.candidates[0]
}

func sameAdaptivePolicy(d *adaptiveState, r route) bool {
	return len(r.candidates) > 0 && r.matched == d.matched && r.rule == d.rule && r.family == d.family && slices.Equal(r.candidates, d.candidates) && initialOutbound(r) == d.initial
}

func newAdaptiveState(address, network string, r route, now time.Time) *adaptiveState {
	current := initialOutbound(r)
	return &adaptiveState{address: address, probeAddress: address, network: network, current: current, reason: "initial policy; waiting for comparable samples", rule: r.rule, matched: r.matched, family: r.family, initial: current, candidates: append([]string(nil), r.candidates...), samples: make(map[string][]sample), lastUsed: now, switched: now, target: r.target}
}

func (e *Engine) chooseLocked(d *adaptiveState) string { return d.current }

func (e *Engine) adaptiveAttemptOrderLocked(primary string, candidates []string, now time.Time) []string {
	order := make([]string, 1, len(candidates))
	order[0] = primary
	for _, id := range candidates {
		if id != primary && e.providers[id] != nil {
			order = append(order, id)
		}
	}
	copy(order[1:], preferAvailable(order[1:], e, now, e.cfg.Adaptive))
	return order
}

func networkFailure(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var nerr net.Error
	return errors.Is(err, context.DeadlineExceeded) ||
		errors.As(err, &nerr) && nerr.Timeout() ||
		errors.Is(err, outbound.ErrWARPReset) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.EPIPE)
}

func (e *Engine) recordLocked(d *adaptiveState, id string, latency time.Duration, err error) {
	family := "unknown"
	switch d.network {
	case "tcp4":
		family = "ipv4"
	case "tcp6":
		family = "ipv6"
	}
	e.recordFamilyLocked(d, id, latency, err, family)
}

func (e *Engine) recordFamilyLocked(d *adaptiveState, id string, latency time.Duration, err error, family string) {
	if errors.Is(err, context.Canceled) || errors.Is(err, hostlocal.ErrDenied) {
		return
	}
	a := adaptiveDefaults(e.cfg.Adaptive)
	now := e.now()
	d.nextSample++
	ss := append(d.samples[id], sample{sequence: d.nextSample, family: family, at: now, latency: latency, success: err == nil, failure: networkFailure(err)})
	cutoff := now.Add(-time.Duration(a.WindowSeconds) * time.Second)
	i := 0
	for i < len(ss) && ss[i].at.Before(cutoff) {
		i++
	}
	ss = ss[i:]
	if len(ss) > 20 {
		ss = ss[len(ss)-20:]
	}
	d.samples[id] = ss
	e.evaluateLocked(d, a, now)
}

func score(ss []sample, cutoff time.Time, minimum int) (time.Duration, float64, bool) {
	var latencies []time.Duration
	total := 0
	for _, s := range ss {
		if s.at.Before(cutoff) {
			continue
		}
		total++
		if s.success {
			latencies = append(latencies, s.latency)
		}
	}
	if len(latencies) < minimum {
		return 0, 0, false
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	return latencies[len(latencies)/2], float64(len(latencies)) / float64(total), true
}

func (e *Engine) evaluateLocked(d *adaptiveState, a config.Adaptive, now time.Time) {
	current := d.samples[d.current]
	failures := 0
	for i := len(current) - 1; i >= 0; i-- {
		if !current[i].failure || now.Sub(current[i].at) > time.Duration(a.WindowSeconds)*time.Second {
			break
		}
		failures++
	}
	if failures >= 3 {
		for _, id := range d.candidates {
			if id == d.current {
				continue
			}
			ss := d.samples[id]
			if len(ss) > 0 && ss[len(ss)-1].success && now.Sub(ss[len(ss)-1].at) <= time.Minute {
				d.current, d.switched, d.reason = id, now, "three consecutive connection failures; alternative recently succeeded"
				d.streak, d.winner = 0, ""
				return
			}
		}
	}
	cutoff := now.Add(-time.Duration(a.WindowSeconds) * time.Second)
	baseline, successRate, ok := score(current, cutoff, a.MinSamples)
	if !ok {
		d.streak, d.winner = 0, ""
		return
	}
	best := ""
	bestLatency := baseline
	for _, id := range d.candidates {
		if id == d.current {
			continue
		}
		latency, rate, valid := score(d.samples[id], cutoff, a.MinSamples)
		if !valid || rate < successRate {
			continue
		}
		if latency < bestLatency && baseline-latency >= time.Duration(a.ImprovementMillis)*time.Millisecond && float64(baseline-latency) >= float64(baseline)*float64(a.ImprovementPercent)/100 {
			best, bestLatency = id, latency
		}
	}
	if best == "" {
		d.streak, d.winner = 0, ""
		d.winnerEvidence = 0
		return
	}
	evidence := d.samples[best][len(d.samples[best])-1].sequence
	if d.winner == best {
		if evidence != d.winnerEvidence {
			d.streak++
		}
	} else {
		d.winner, d.streak = best, 1
	}
	d.winnerEvidence = evidence
	if d.streak >= 2 && now.Sub(d.switched) >= time.Duration(a.CooldownSeconds)*time.Second {
		d.current, d.switched, d.reason = best, now, "two observations met latency improvement and success-rate thresholds"
		d.streak, d.winner = 0, ""
	}
}

type probeJob struct {
	key, id, address, network string
	state                     *adaptiveState
	provider                  outbound.Provider
	timeout                   time.Duration
	source                    netip.Addr
	inbound                   string
	providerType              string
	generation                uint64
}

// scheduleProbes is kept deterministic for clock-controlled budget tests.
func (e *Engine) scheduleProbes() []probeJob {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.cfg.Adaptive.Enabled {
		return nil
	}
	a := adaptiveDefaults(e.cfg.Adaptive)
	now := e.now()
	i := 0
	for i < len(e.probeTimes) && now.Sub(e.probeTimes[i]) >= time.Minute {
		i++
	}
	e.probeTimes = e.probeTimes[i:]
	keys := make([]string, 0, len(e.destinations))
	for k := range e.destinations {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return e.destinations[keys[i]].lastProbe.Before(e.destinations[keys[j]].lastProbe)
	})
	var jobs []probeJob
	for _, key := range keys {
		if len(e.probeTimes) >= a.ProbesPerMinute || e.probes >= a.ProbeConcurrency {
			break
		}
		d := e.destinations[key]
		probeAddr := d.probeAddress
		if probeAddr == "" {
			probeAddr = d.address
		}
		host, port, err := net.SplitHostPort(probeAddr)
		if err != nil || (port != "80" && port != "443") || privateHost(normalize(host), e.cfg.PAC) || now.Sub(d.lastUsed) > 5*time.Minute || !d.lastProbe.IsZero() && now.Sub(d.lastProbe) < time.Minute || len(d.candidates) < 2 {
			continue
		}
		// A shared domain's last-used host can acquire a different rule or
		// ingress policy after a config update. Never probe it using old candidates.
		t := d.target
		t.host, t.port = normalize(host), port
		r, err := e.selectRoute(t)
		if err != nil || !sameAdaptivePolicy(d, r) {
			continue
		}
		id := ""
		for range len(d.candidates) {
			cand := d.candidates[d.probeIndex%len(d.candidates)]
			d.probeIndex++
			if e.providers[cand] == nil {
				continue
			}
			ss := d.samples[cand]
			if len(ss) > 0 && now.Sub(ss[len(ss)-1].at) < time.Minute {
				continue
			}
			id = cand
			break
		}
		if id == "" {
			continue
		}
		p := e.providers[id]
		d.lastProbe = now
		e.probeTimes = append(e.probeTimes, now)
		e.probes++
		providerType := ""
		for _, o := range e.cfg.Outbounds {
			if o.ID == id {
				providerType = o.Type
				break
			}
		}
		jobs = append(jobs, probeJob{key: key, id: id, address: probeAddr, network: d.network, state: d, provider: p, timeout: time.Duration(a.ProbeTimeoutSeconds) * time.Second, source: t.source, inbound: t.inbound, providerType: providerType, generation: e.generation})
	}
	return jobs
}

func (e *Engine) probeLoop() {
	var workers sync.WaitGroup
	defer func() { workers.Wait(); close(e.done) }()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			for _, job := range e.scheduleProbes() {
				workers.Add(1)
				go func() {
					defer workers.Done()
					ctx, cancel := context.WithTimeout(e.ctx, job.timeout)
					defer cancel()
					started := e.now()
					conn, err := e.dialProbe(ctx, job)
					e.completeProbe(job, started, conn, err)
				}()
			}
		}
	}
}

func (e *Engine) completeProbe(job probeJob, started time.Time, conn net.Conn, err error) {
	family := "unknown"
	internal := false
	if conn != nil {
		e.mu.Lock()
		policy := e.destinationPolicy
		e.mu.Unlock()
		if policy != nil {
			internal = policy(conn.RemoteAddr().String()) != ""
		}
		host, _, _ := net.SplitHostPort(job.address)
		family = addressFamily(destinationIP(conn, host))
		_ = conn.Close()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.probes--
	if errors.Is(err, hostlocal.ErrDenied) {
		return
	}
	if job.state != nil && e.destinations[job.key] == job.state || sameProvider(job.provider, e.providers[job.id]) {
		e.recordHealthLocked(job.id, err)
	}
	if e.destinations[job.key] == job.state {
		if internal {
			if job.address == job.state.probeAddress {
				delete(e.destinations, job.key)
			}
		} else {
			e.recordFamilyLocked(job.state, job.id, e.now().Sub(started), err, family)
		}
	}
}
