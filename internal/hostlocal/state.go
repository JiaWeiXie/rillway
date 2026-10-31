package hostlocal

import (
	"errors"
	"net/netip"
	"sort"
	"sync"
	"sync/atomic"
)

const (
	MaxRecent       = 8
	MaxPortCounters = 128
)

// State publishes one immutable policy to all consumers. A completed check
// authorizes that operation; publication does not revoke an already-authorized
// connect/send or an established TCP stream.
type State struct {
	current              atomic.Pointer[Policy]
	mu                   sync.Mutex
	actual, hypothetical uint64
	ports                map[uint16]PortCounts
	overflow             PortCounts
	recent               []Observation
}

func NewState(p *Policy) *State {
	s := &State{ports: make(map[uint16]PortCounts)}
	s.Publish(p)
	return s
}

// Publish must be called only after the corresponding configuration was saved.
func (s *State) Publish(p *Policy) { s.current.Store(p) }
func (s *State) Load() *Policy     { return s.current.Load() }

// PortCounts covers external-to-host-local policy decisions, not all traffic.
// Port zero is the aggregate overflow bucket, never an actual destination port.
type PortCounts struct {
	Port                                uint16 `json:"port"`
	ActualDenials                       uint64 `json:"actual_denials"`
	HypotheticalLocalClientsOnlyDenials uint64 `json:"hypothetical_local_clients_only_denials"`
}

type Observation struct {
	Source                             string `json:"source,omitempty"`
	Destination                        string `json:"destination,omitempty"`
	Port                               uint16 `json:"port"`
	ActualDenial                       bool   `json:"actual_denial"`
	HypotheticalLocalClientsOnlyDenial bool   `json:"hypothetical_local_clients_only_denial"`
	Reason                             string `json:"reason"`
}

type Snapshot struct {
	EffectiveMode                       string        `json:"effective_mode"`
	ActualDenials                       uint64        `json:"actual_denials"`
	HypotheticalLocalClientsOnlyDenials uint64        `json:"hypothetical_local_clients_only_denials"`
	ExternalHostLocalByPort             []PortCounts  `json:"external_host_local_by_port"`
	PortOverflow                        PortCounts    `json:"port_overflow"`
	Recent                              []Observation `json:"recent"`
}

func (s *State) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := Snapshot{EffectiveMode: s.Load().Mode(), ActualDenials: s.actual, HypotheticalLocalClientsOnlyDenials: s.hypothetical, PortOverflow: s.overflow, Recent: append([]Observation(nil), s.recent...)}
	for _, counts := range s.ports {
		result.ExternalHostLocalByPort = append(result.ExternalHostLocalByPort, counts)
	}
	sort.Slice(result.ExternalHostLocalByPort, func(i, j int) bool {
		return result.ExternalHostLocalByPort[i].Port < result.ExternalHostLocalByPort[j].Port
	})
	return result
}

// Event belongs to one admitted ingress operation. Do not retain one in a UDP
// association: every datagram has its own event. Explain/probes have no event.
type Event struct {
	mu                                   sync.Mutex
	state                                *State
	source                               netip.Addr
	port                                 uint16
	completed, final, permittedCandidate bool
	dst                                  netip.AddrPort
	decision                             Decision
	deniedDst                            netip.AddrPort
	deniedDecision                       Decision
}

func (s *State) Begin(source netip.Addr, port uint16) *Event {
	return &Event{state: s, source: canonical(source), port: port}
}

// ObserveCandidate is concurrency-safe for dual-stack attempts. It supplies
// evidence only: rejecting a candidate does not mean the request was refused.
func (e *Event) ObserveCandidate(dst netip.AddrPort, d Decision) {
	if e == nil || d.NeedsDirectIP {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.completed {
		return
	}
	if d.Denied {
		e.deniedDst, e.deniedDecision = dst, d
	} else {
		e.permittedCandidate = true
	}
}

// Finalize supplies the actual final identity, replacing candidate observations.
// A successful allowed candidate wins over any other candidate's refusal.
func (e *Event) Finalize(dst netip.AddrPort, d Decision) {
	if e == nil || d.NeedsDirectIP {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.completed {
		e.dst, e.decision, e.final = dst, d, true
	}
}

// Complete commits once. A transport/DNS failure is not an actual policy denial;
// a wrapped denial is counted only when no permitted candidate was attempted.
func (e *Event) Complete(err error) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.completed {
		return
	}
	e.completed = true
	if !e.final {
		if !errors.Is(err, ErrDenied) || e.permittedCandidate || !e.deniedDecision.Denied {
			return
		}
		e.dst, e.decision = e.deniedDst, e.deniedDecision
	}
	d := e.decision
	if !d.Denied && !d.HypotheticalDenied {
		return
	}
	s := e.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if d.Denied {
		s.actual++
	}
	if d.HypotheticalDenied {
		s.hypothetical++
	}
	if d.HostLocal && !e.source.IsLoopback() {
		counts, exists := s.ports[e.port]
		overflow := !exists && len(s.ports) >= MaxPortCounters
		if overflow {
			counts = s.overflow
		} else {
			counts.Port = e.port
		}
		if d.Denied {
			counts.ActualDenials++
		}
		if d.HypotheticalDenied {
			counts.HypotheticalLocalClientsOnlyDenials++
		}
		if overflow {
			s.overflow = counts
		} else {
			s.ports[e.port] = counts
		}
	}
	o := Observation{Port: e.port, ActualDenial: d.Denied, HypotheticalLocalClientsOnlyDenial: d.HypotheticalDenied, Reason: d.Reason}
	if e.source.IsValid() {
		o.Source = e.source.String()
	}
	if e.dst.IsValid() {
		o.Destination = e.dst.String()
	}
	if len(s.recent) == MaxRecent {
		copy(s.recent, s.recent[1:])
		s.recent = s.recent[:MaxRecent-1]
	}
	s.recent = append(s.recent, o)
}
