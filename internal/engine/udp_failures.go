package engine

import (
	"errors"
	"net"
	"rillway/internal/outbound"
	"sort"
)

// UDP dial failure reasons. The set is fixed so counters stay low-cardinality.
const (
	UDPFailureUnsupported = "unsupported" // the outbound cannot carry UDP
	UDPFailureUnavailable = "unavailable" // the outbound or group is disabled, missing, failed or retired, or has no eligible member
	UDPFailureBlocked     = "blocked"     // private-network protection, or the outbound connected back to this daemon
	UDPFailureTimeout     = "timeout"
	UDPFailureNetwork     = "network"
)

// UDPDialFailure counts UDP destinations that could not be forwarded since the
// daemon started. Outbound is the last outbound attempted, or the configured
// target when routing refused before any attempt. Destinations are never kept.
type UDPDialFailure struct {
	Outbound string `json:"outbound"`
	Reason   string `json:"reason"`
	Count    uint64 `json:"count"`
}

type udpFailureKey struct{ outbound, reason string }

func udpFailureReason(err error) string {
	var route *routeBlockError
	var timeout net.Error
	switch {
	case errors.Is(err, errLocalPeer):
		return UDPFailureBlocked
	case errors.As(err, &route):
		if route.reason == "private_protected" {
			return UDPFailureBlocked
		}
		return UDPFailureUnavailable // the routed outbound or group cannot be used now
	case errors.Is(err, outbound.ErrUDPUnsupported):
		return UDPFailureUnsupported
	case errors.Is(err, outbound.ErrUnavailable):
		return UDPFailureUnavailable
	case errors.As(err, &timeout) && timeout.Timeout():
		return UDPFailureTimeout
	default:
		return UDPFailureNetwork
	}
}

func (e *Engine) recordUDPFailureLocked(outboundID, reason string) {
	if e.udpFailures == nil {
		e.udpFailures = make(map[udpFailureKey]uint64)
	}
	e.udpFailures[udpFailureKey{outboundID, reason}]++
}

// pruneUDPFailuresLocked keeps the counters bounded by the configuration:
// a removed outbound or group no longer reports failures.
func (e *Engine) pruneUDPFailuresLocked() {
	for key := range e.udpFailures {
		if e.providers[key.outbound] == nil && e.groups[key.outbound] == nil {
			delete(e.udpFailures, key)
		}
	}
}

func (e *Engine) udpFailuresLocked() []UDPDialFailure {
	result := make([]UDPDialFailure, 0, len(e.udpFailures))
	for key, count := range e.udpFailures {
		result = append(result, UDPDialFailure{Outbound: key.outbound, Reason: key.reason, Count: count})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Outbound == result[j].Outbound {
			return result[i].Reason < result[j].Reason
		}
		return result[i].Outbound < result[j].Outbound
	})
	return result
}
