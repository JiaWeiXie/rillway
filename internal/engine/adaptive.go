package engine

import (
	"context"
	"errors"
	"net"
	"rillway/internal/config"
	"rillway/internal/outbound"
	"sort"
	"sync"
	"syscall"
	"time"
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
	address, network, current, reason string
	rule, family, initial             string
	candidates                        []string
	samples                           map[string][]sample
	lastUsed, switched, lastProbe     time.Time
	winner                            string
	winnerEvidence, nextSample        uint64
	streak, probeIndex                int
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

func (e *Engine) destinationLocked(key, address, network string, r route) *adaptiveState {
	if d := e.destinations[key]; d != nil {
		return d
	}
	if len(e.destinations) >= maxDestinations {
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

func newAdaptiveState(address, network string, r route, now time.Time) *adaptiveState {
	current := initialOutbound(r)
	return &adaptiveState{address: address, network: network, current: current, reason: "initial policy; waiting for comparable samples", rule: r.rule, family: r.family, initial: current, candidates: append([]string(nil), r.candidates...), samples: make(map[string][]sample), lastUsed: now, switched: now}
}

func (e *Engine) chooseLocked(d *adaptiveState) string { return d.current }

func networkFailure(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var nerr net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &nerr) && nerr.Timeout() || errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ECONNREFUSED)
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
	if errors.Is(err, context.Canceled) {
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
		host, port, err := net.SplitHostPort(d.address)
		if err != nil || (port != "80" && port != "443") || privateHost(normalize(host), e.cfg.PAC) || now.Sub(d.lastUsed) > 5*time.Minute || !d.lastProbe.IsZero() && now.Sub(d.lastProbe) < time.Minute || len(d.candidates) < 2 {
			continue
		}
		id := d.candidates[d.probeIndex%len(d.candidates)]
		d.probeIndex++
		p := e.providers[id]
		if p == nil {
			continue
		}
		d.lastProbe = now
		e.probeTimes = append(e.probeTimes, now)
		e.probes++
		jobs = append(jobs, probeJob{key: key, id: id, address: d.address, network: d.network, state: d, provider: p, timeout: time.Duration(a.ProbeTimeoutSeconds) * time.Second})
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
					conn, err := job.provider.DialContext(ctx, job.network, job.address)
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
			blocked, ignored := policy(conn.RemoteAddr().String())
			internal = blocked || ignored
		}
		host, _, _ := net.SplitHostPort(job.address)
		family = addressFamily(destinationIP(conn, host))
		_ = conn.Close()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.probes--
	if e.destinations[job.key] == job.state {
		if internal {
			delete(e.destinations, job.key)
		} else {
			e.recordFamilyLocked(job.state, job.id, e.now().Sub(started), err, family)
		}
	}
}
