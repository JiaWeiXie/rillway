// Package engine routes new TCP streams without changing existing connections.
package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"reflect"
	"rillway/internal/config"
	"rillway/internal/outbound"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxFlows        = 2048
	maxDestinations = 2048
)

type route struct {
	outbound   string
	rule       string
	family     string
	candidates []string
	fixed      bool
}

// Snapshot contains bounded, in-memory connection metadata. Payloads and HTTP
// headers are never retained. IP is empty when a remote proxy did not reveal it.
type Snapshot struct {
	At           time.Time     `json:"at"`
	Flows        []Flow        `json:"flows"`
	Destinations []Destination `json:"destinations"`
	Totals       Totals        `json:"totals"`
}

// Totals cover the retained flows, not an unbounded lifetime history.
type Totals struct {
	ActiveConnections int     `json:"active_connections"`
	UploadBytes       uint64  `json:"upload_bytes"`
	DownloadBytes     uint64  `json:"download_bytes"`
	UploadRate        float64 `json:"upload_bytes_per_second"`
	DownloadRate      float64 `json:"download_bytes_per_second"`
}

type Flow struct {
	ID            uint64    `json:"id"`
	Host          string    `json:"host"`
	Port          string    `json:"port"`
	IP            string    `json:"ip,omitempty"`
	Family        string    `json:"family"`
	Outbound      string    `json:"outbound"`
	Rule          string    `json:"rule"`
	Started       time.Time `json:"started"`
	Closed        bool      `json:"closed"`
	ConnectMillis float64   `json:"connect_ms"`
	UploadBytes   uint64    `json:"upload_bytes"`
	DownloadBytes uint64    `json:"download_bytes"`
	UploadRate    float64   `json:"upload_bytes_per_second"`
	DownloadRate  float64   `json:"download_bytes_per_second"`
}

type Destination struct {
	Address      string        `json:"address"`
	Family       string        `json:"family"`
	Outbound     string        `json:"outbound"`
	Reason       string        `json:"reason"`
	LastUsed     time.Time     `json:"last_used"`
	LastSwitch   time.Time     `json:"last_switch"`
	Measurements []Measurement `json:"measurements"`
}

// Measurement separates confirmed IP families for observation. Automatic
// routing compares end-to-end paths; a remote DNS proxy can remain unknown.
type Measurement struct {
	Outbound     string  `json:"outbound"`
	Family       string  `json:"family"`
	Samples      int     `json:"samples"`
	Successful   int     `json:"successful"`
	MedianMillis float64 `json:"median_connect_ms"`
}

type Engine struct {
	mu                sync.Mutex
	cfg               config.Config
	providers         map[string]outbound.Provider
	flows             map[uint64]*flowState
	destinations      map[string]*adaptiveState
	sequence          atomic.Uint64
	now               func() time.Time
	ctx               context.Context
	cancel            context.CancelFunc
	done              chan struct{}
	probeTimes        []time.Time
	probes            int
	generation        uint64
	destinationPolicy func(string) (blocked, internal bool)
}

// SetDestinationPolicy excludes this daemon's own endpoints from observations
// and adaptive measurements. Literal internal services are dialed locally;
// proxy endpoints are refused. DNS aliases are checked only after the selected
// provider resolves them, never through an additional host DNS lookup.
func (e *Engine) SetDestinationPolicy(policy func(string) (blocked, internal bool)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.destinationPolicy = policy
}

func New(cfg config.Config, providers map[string]outbound.Provider) *Engine {
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{flows: make(map[uint64]*flowState), destinations: make(map[string]*adaptiveState), now: time.Now, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	e.Update(cfg, providers)
	go e.probeLoop()
	return e
}

// Update atomically replaces routing for new connections. Providers remain
// owned by the caller; an established connection is never interrupted here.
// Learned adaptive routes survive only when the destination keeps the same
// rule, family, initial outbound, candidate order and provider instances;
// otherwise new connections restart from the configured initial outbound.
func (e *Engine) Update(cfg config.Config, providers map[string]outbound.Provider) {
	e.mu.Lock()
	defer e.mu.Unlock()
	previous := e.providers
	e.cfg = cloneConfig(cfg)
	e.generation++
	e.providers = make(map[string]outbound.Provider, len(providers))
	for id, p := range providers {
		e.providers[id] = p
	}
	kept := make(map[string]*adaptiveState, len(e.destinations))
	for key, d := range e.destinations {
		if e.unchangedPolicyLocked(d, previous) {
			kept[key] = d
		}
	}
	e.destinations = kept
}

func (e *Engine) unchangedPolicyLocked(d *adaptiveState, previous map[string]outbound.Provider) bool {
	host, _, err := net.SplitHostPort(d.address)
	if err != nil {
		return false
	}
	r, err := e.selectRoute(host)
	// d.candidates is never empty, so the equal check guards initialOutbound.
	if err != nil || r.rule != d.rule || r.family != d.family || !slices.Equal(r.candidates, d.candidates) || initialOutbound(r) != d.initial {
		return false
	}
	for _, id := range d.candidates {
		if !sameProvider(previous[id], e.providers[id]) {
			return false
		}
	}
	return true
}

// sameProvider compares provider identity without panicking on value types
// whose fields are not comparable.
func sameProvider(a, b outbound.Provider) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	return va.Type() == vb.Type() && va.Comparable() && va.Equal(vb)
}

func cloneConfig(c config.Config) config.Config {
	c.Outbounds = append([]config.Outbound(nil), c.Outbounds...)
	c.Rules = append([]config.Rule(nil), c.Rules...)
	for i := range c.Rules {
		c.Rules[i].Domains = append([]string(nil), c.Rules[i].Domains...)
		c.Rules[i].Suffixes = append([]string(nil), c.Rules[i].Suffixes...)
		c.Rules[i].CIDRs = append([]string(nil), c.Rules[i].CIDRs...)
		c.Rules[i].Candidates = append([]string(nil), c.Rules[i].Candidates...)
	}
	c.Adaptive.Candidates = append([]string(nil), c.Adaptive.Candidates...)
	c.PAC.BypassDomains = append([]config.PACBypass(nil), c.PAC.BypassDomains...)
	c.PAC.BypassCIDRs = append([]config.PACBypass(nil), c.PAC.BypassCIDRs...)
	return c
}

func (e *Engine) Close() error {
	e.cancel()
	<-e.done
	return nil
}

func normalize(host string) string { return strings.TrimSuffix(strings.ToLower(host), ".") }

func suffixMatch(host, suffix string) bool {
	suffix = normalize(strings.TrimPrefix(suffix, "."))
	return host == suffix || strings.HasSuffix(host, "."+suffix)
}

func matches(r config.Rule, host string) bool {
	for _, domain := range r.Domains {
		if host == normalize(domain) {
			return true
		}
	}
	for _, suffix := range r.Suffixes {
		if suffixMatch(host, suffix) {
			return true
		}
	}
	// Never resolve a hostname through the host DNS merely to test a CIDR rule.
	// Name resolution belongs exclusively to the selected outbound.
	if ip, err := netip.ParseAddr(host); err == nil {
		for _, cidr := range r.CIDRs {
			if prefix, err := netip.ParsePrefix(cidr); err == nil && prefix.Contains(ip.Unmap()) {
				return true
			}
		}
	}
	return false
}

func privateIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	return !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || netip.MustParsePrefix("100.64.0.0/10").Contains(ip)
}

func privateHost(host string, pac config.PAC) bool {
	if ip, err := netip.ParseAddr(host); err == nil {
		if privateIP(ip) {
			return true
		}
		for _, cidr := range pac.EnabledCIDRs() {
			if p, err := netip.ParsePrefix(cidr); err == nil && p.Contains(ip.Unmap()) {
				return true
			}
		}
		return false
	}
	if !strings.Contains(host, ".") || suffixMatch(host, "local") || suffixMatch(host, "localhost") || suffixMatch(host, "internal") || suffixMatch(host, "home.arpa") {
		return true
	}
	for _, suffix := range pac.EnabledDomains() {
		if suffixMatch(host, suffix) {
			return true
		}
	}
	return false
}

func (e *Engine) warpOutbound(id string) bool {
	for _, p := range e.cfg.Outbounds {
		if p.ID == id {
			return p.Type == "warp"
		}
	}
	return false
}

func (e *Engine) publicWireGuard(id string) bool {
	for _, p := range e.cfg.Outbounds {
		if p.ID == id {
			return p.Type == "wireguard" && p.PublicInternet
		}
	}
	return false
}

func (e *Engine) adaptiveCandidates(ids []string) []string {
	var valid []string
	for _, id := range ids {
		if e.providers[id] == nil {
			continue
		}
		for _, p := range e.cfg.Outbounds {
			if p.ID == id && p.Enabled && (p.Type == "direct" || p.Type == "warp" || p.Type == "wireguard" && p.PublicInternet) {
				valid = append(valid, id)
				break
			}
		}
	}
	return valid
}

func (e *Engine) selectRoute(host string) (route, error) {
	r := route{outbound: e.cfg.DefaultOutbound, rule: "default", family: "auto"}
	var adaptiveRule *config.Rule
	for _, rule := range e.cfg.Rules {
		if !matches(rule, host) {
			continue
		}
		if rule.Adaptive {
			if adaptiveRule == nil {
				rr := rule
				adaptiveRule = &rr
			}
			continue
		}
		r.outbound, r.rule, r.family = rule.Outbound, rule.ID, rule.Family
		r.fixed = true
		return e.protectRoute(r, host)
	}
	ids := e.cfg.Adaptive.Candidates
	if adaptiveRule != nil {
		r.rule, r.family = adaptiveRule.ID, adaptiveRule.Family
		ids = adaptiveRule.Candidates
		if adaptiveRule.Outbound != "" {
			r.outbound = adaptiveRule.Outbound
		}
	}
	if e.cfg.Adaptive.Enabled && !privateHost(host, e.cfg.PAC) {
		r.candidates = e.adaptiveCandidates(ids)
	}
	return e.protectRoute(r, host)
}

func (e *Engine) protectRoute(r route, host string) (route, error) {
	if r.family == "" {
		r.family = "auto"
	}
	if privateHost(host, e.cfg.PAC) && (e.warpOutbound(r.outbound) || e.publicWireGuard(r.outbound) && !r.fixed) {
		return r, fmt.Errorf("private destination %q cannot use public outbound %q", host, r.outbound)
	}
	if e.providers[r.outbound] == nil {
		return r, fmt.Errorf("outbound %q is unavailable; no fallback configured", r.outbound)
	}
	return r, nil
}

func (e *Engine) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, errors.New("only TCP is supported")
	}
	if err := e.ctx.Err(); err != nil {
		return nil, errors.New("engine is closed")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	host = normalize(host)
	address = net.JoinHostPort(host, port)
	e.mu.Lock()
	policy := e.destinationPolicy
	e.mu.Unlock()
	if policy != nil {
		blocked, internal := policy(address)
		if blocked {
			return nil, errors.New("proxy destination is a local proxy listener")
		}
		if internal {
			return (&net.Dialer{}).DialContext(ctx, network, address)
		}
	}
	e.mu.Lock()
	r, err := e.selectRoute(host)
	if err != nil {
		e.mu.Unlock()
		return nil, err
	}
	switch r.family {
	case "ipv4":
		network = "tcp4"
	case "ipv6":
		network = "tcp6"
	}
	key := address + "|" + network
	var state *adaptiveState
	generation := e.generation
	if len(r.candidates) > 0 {
		state = e.destinations[key]
		if state == nil {
			// Do not publish a new adaptive destination until provider resolution
			// confirms it is not this daemon. Otherwise probes can race the dial.
			state = newAdaptiveState(address, network, r, e.now())
		}
		r.outbound = e.chooseLocked(state)
		state.lastUsed = e.now()
	}
	p := e.providers[r.outbound]
	started := e.now()
	e.mu.Unlock()
	conn, err := p.DialContext(ctx, network, address)
	elapsed := e.now().Sub(started)
	if err == nil && conn != nil && policy != nil {
		blocked, internal := policy(conn.RemoteAddr().String())
		if blocked || internal {
			e.mu.Lock()
			if state != nil && e.destinations[key] == state {
				delete(e.destinations, key)
			}
			e.mu.Unlock()
			if blocked {
				_ = conn.Close()
				return nil, errors.New("proxy destination resolves to a local proxy listener")
			}
			return conn, nil
		}
	}
	ip := ""
	if conn != nil {
		ip = destinationIP(conn, host)
	}
	family := addressFamily(ip)
	if state != nil {
		e.mu.Lock()
		// An in-flight dial from a previous config must not populate the new policy.
		if e.generation == generation {
			state = e.destinationLocked(key, address, network, r)
			state.lastUsed = e.now()
			e.recordFamilyLocked(state, r.outbound, elapsed, err, family)
		}
		e.mu.Unlock()
	}
	if err != nil {
		return nil, err
	}
	f := &flowState{flow: Flow{ID: e.sequence.Add(1), Host: host, Port: port, IP: ip, Family: family, Outbound: r.outbound, Rule: r.rule, Started: started, ConnectMillis: float64(elapsed) / float64(time.Millisecond)}, now: e.now}
	e.mu.Lock()
	e.pruneFlowsLocked()
	e.flows[f.flow.ID] = f
	e.mu.Unlock()
	return &trackedConn{Conn: conn, flow: f}, nil
}

func addressFamily(ip string) string {
	if addr, err := netip.ParseAddr(ip); err == nil {
		if addr.Unmap().Is4() {
			return "ipv4"
		}
		return "ipv6"
	}
	return "unknown"
}

func destinationIP(conn net.Conn, host string) string {
	if c, ok := conn.(interface{ DestinationIP() string }); ok {
		return c.DestinationIP()
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.String()
	}
	// A generic connection's RemoteAddr can be an upstream SOCKS listener.
	// Only providers explicitly implementing DestinationIP may identify it.
	return ""
}

func (e *Engine) pruneFlowsLocked() {
	now := e.now()
	var oldest uint64
	var oldestAt time.Time
	for id, f := range e.flows {
		if now.Sub(f.flow.Started) > 24*time.Hour {
			delete(e.flows, id)
			continue
		}
		if oldestAt.IsZero() || f.flow.Started.Before(oldestAt) {
			oldest, oldestAt = id, f.flow.Started
		}
	}
	if len(e.flows) >= maxFlows {
		delete(e.flows, oldest)
	}
}

func (e *Engine) Snapshot() Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := Snapshot{At: e.now(), Flows: []Flow{}, Destinations: []Destination{}}
	for id, f := range e.flows {
		if s.At.Sub(f.flow.Started) > 24*time.Hour {
			delete(e.flows, id)
			continue
		}
		flow := f.snapshot(s.At)
		s.Flows = append(s.Flows, flow)
		if !flow.Closed {
			s.Totals.ActiveConnections++
		}
		s.Totals.UploadBytes += flow.UploadBytes
		s.Totals.DownloadBytes += flow.DownloadBytes
		s.Totals.UploadRate += flow.UploadRate
		s.Totals.DownloadRate += flow.DownloadRate
	}
	for key, d := range e.destinations {
		if s.At.Sub(d.lastUsed) > 24*time.Hour {
			delete(e.destinations, key)
			continue
		}
		s.Destinations = append(s.Destinations, Destination{Address: d.address, Family: d.network, Outbound: d.current, Reason: d.reason, LastUsed: d.lastUsed, LastSwitch: d.switched, Measurements: measurements(d, s.At.Add(-time.Duration(adaptiveDefaults(e.cfg.Adaptive).WindowSeconds)*time.Second))})
	}
	sort.Slice(s.Flows, func(i, j int) bool { return s.Flows[i].ID > s.Flows[j].ID })
	sort.Slice(s.Destinations, func(i, j int) bool { return s.Destinations[i].Address < s.Destinations[j].Address })
	return s
}

func measurements(d *adaptiveState, cutoff time.Time) []Measurement {
	result := []Measurement{}
	for id, samples := range d.samples {
		groups := make(map[string][]sample)
		for _, s := range samples {
			if !s.at.Before(cutoff) {
				groups[s.family] = append(groups[s.family], s)
			}
		}
		for family, ss := range groups {
			m := Measurement{Outbound: id, Family: family, Samples: len(ss)}
			for _, s := range ss {
				if s.success {
					m.Successful++
				}
			}
			if median, _, valid := score(ss, cutoff, 1); valid {
				m.MedianMillis = float64(median) / float64(time.Millisecond)
			}
			result = append(result, m)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Outbound == result[j].Outbound {
			return result[i].Family < result[j].Family
		}
		return result[i].Outbound < result[j].Outbound
	})
	return result
}

func (e *Engine) Statuses(ctx context.Context) []outbound.Status {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	e.mu.Lock()
	ps := make([]outbound.Provider, 0, len(e.providers))
	for _, p := range e.providers {
		ps = append(ps, p)
	}
	e.mu.Unlock()
	statuses := make([]outbound.Status, 0, len(ps))
	for _, p := range ps {
		statuses = append(statuses, p.Status(ctx))
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].ID < statuses[j].ID })
	return statuses
}

type bucket struct {
	second           int64
	upload, download uint64
}
type flowState struct {
	mu      sync.Mutex
	flow    Flow
	buckets [6]bucket
	now     func() time.Time
}

func (f *flowState) add(n int, upload bool) {
	if n <= 0 {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	second := f.now().Unix()
	b := &f.buckets[second%6]
	if b.second != second {
		*b = bucket{second: second}
	}
	if upload {
		f.flow.UploadBytes += uint64(n)
		b.upload += uint64(n)
	} else {
		f.flow.DownloadBytes += uint64(n)
		b.download += uint64(n)
	}
}

func (f *flowState) snapshot(now time.Time) Flow {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.flow
	for _, b := range f.buckets {
		if b.second > now.Unix()-5 && b.second <= now.Unix() {
			s.UploadRate += float64(b.upload) / 5
			s.DownloadRate += float64(b.download) / 5
		}
	}
	return s
}

type trackedConn struct {
	net.Conn
	flow *flowState
}

func (c *trackedConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.flow.add(n, false)
	return n, err
}

func (c *trackedConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.flow.add(n, true)
	return n, err
}

func (c *trackedConn) Close() error {
	c.flow.mu.Lock()
	c.flow.flow.Closed = true
	c.flow.mu.Unlock()
	return c.Conn.Close()
}

func (c *trackedConn) CloseWrite() error {
	if w, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return w.CloseWrite()
	}
	return c.Close()
}
