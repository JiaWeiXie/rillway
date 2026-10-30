package engine

import (
	"errors"
	"net"
	"net/netip"
	"rillway/internal/config"
	"strconv"
	"strings"
)

// routeBlockError keeps routing failures classifiable without parsing their text.
// Dial callers retain the existing error message; previews expose only the code.
type routeBlockError struct {
	reason  string
	message string
}

func (err *routeBlockError) Error() string { return err.message }

func explainTarget(req config.ExplainRequest) (target, error) {
	t := target{host: normalize(req.Host), port: req.Port, inbound: req.Inbound}
	if strings.TrimSpace(t.host) == "" {
		return target{}, errors.New("route host is required")
	}
	port, err := strconv.ParseUint(req.Port, 10, 16)
	if err != nil || port == 0 {
		return target{}, errors.New("route port must be a number from 1 to 65535")
	}
	if req.Source != "" {
		source, err := netip.ParseAddr(req.Source)
		if err != nil {
			return target{}, errors.New("route source must be an IP address")
		}
		t.source = source.WithZone("").Unmap()
	}
	switch req.Inbound {
	case "", "http", "socks5":
	default:
		return target{}, errors.New("route inbound must be http or socks5")
	}
	switch req.Family {
	case "", "auto", "ipv4", "ipv6":
	default:
		return target{}, errors.New("route family must be auto, ipv4 or ipv6")
	}
	return t, nil
}

func explainBlock(result config.ExplainResult, err error) config.ExplainResult {
	result.Blocked = true
	result.Reason = "target_unavailable"
	if blocked, ok := err.(*routeBlockError); ok {
		result.Reason = blocked.reason
	}
	return result
}

// Explain evaluates the configured route without connecting, probing or learning.
// Adaptive routes report their configured initial outbound, not a prediction of
// the next learned route. Group ordering uses current health as a preference only.
func (e *Engine) Explain(req config.ExplainRequest) (config.ExplainResult, error) {
	t, err := explainTarget(req)
	if err != nil {
		return config.ExplainResult{}, err
	}
	if req.Family == "" {
		req.Family = "auto"
	}
	e.mu.Lock()
	policy, pac := e.destinationPolicy, e.cfg.PAC
	e.mu.Unlock()
	address := net.JoinHostPort(t.host, t.port)
	if policy != nil {
		if reason := policy(address); reason != "" {
			if reason == "proxy_listener" {
				reason = "proxy_loop" // the reason code the route preview has always reported
			}
			return config.ExplainResult{Blocked: true, TargetKind: "default", Private: privateHost(t.host, pac), Family: req.Family, Reason: reason}, nil
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	r, err := e.selectRoute(t)
	result := config.ExplainResult{
		RuleID:     r.rule,
		TargetKind: "default",
		TargetID:   r.outbound,
		Private:    privateHost(t.host, e.cfg.PAC),
		Family:     r.family,
		Reason:     "default",
	}
	if r.matched {
		result.TargetKind = "outbound"
		result.Reason = "adaptive_initial"
		if r.fixed {
			result.Reason = "fixed_rule"
		}
	}
	if e.groups[r.outbound] != nil {
		result.TargetKind = "group"
	}
	// Like DialContext, an explicit rule family overrides the requested network;
	// an automatic rule retains an explicitly requested IP family.
	if result.Family == "auto" && (req.Family == "ipv4" || req.Family == "ipv6") {
		result.Family = req.Family
	}
	if err != nil {
		return explainBlock(result, err), nil
	}

	resolved := r.outbound
	if len(r.candidates) > 0 {
		resolved = initialOutbound(r)
		result.Reason = "adaptive_initial"
	}
	if g := e.groups[resolved]; g != nil {
		order, err := e.groupOrderLocked(g, t, r.fixed, e.now(), false)
		if err != nil {
			return explainBlock(result, err), nil
		}
		result.Order = order
		result.ResolvedID = order[0]
		switch g.cfg.Type {
		case "select":
			result.Reason = "group_select"
		case "fallback":
			result.Reason = "group_fallback"
		case "load-balance":
			if g.cfg.Strategy == "round-robin" {
				result.Reason = "group_round_robin"
			} else {
				result.Reason = "group_consistent_hashing"
			}
		}
	} else {
		for _, outbound := range e.cfg.Outbounds {
			if outbound.ID == resolved && !outbound.Enabled {
				return explainBlock(result, &routeBlockError{reason: "target_unavailable", message: "outbound is disabled"}), nil
			}
		}
		result.ResolvedID = resolved
	}
	return result, nil
}
