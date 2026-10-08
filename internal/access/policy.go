package access

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"time"
)

const (
	ActionAllow = "allow"
	ActionDeny  = "deny"

	DecisionAllow = "allow"
	DecisionDeny  = "deny"

	MaxRules = 256
	MaxCIDRs = 1024
)

var validID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// RuleSpec defines a candidate source-access rule for compilation.
type RuleSpec struct {
	ID      string
	Action  string
	CIDRs   []string
	Enabled bool
}

// Decision represents the result of evaluating an admitted client IP.
type Decision struct {
	Allowed bool
	RuleID  string
}

// SourceSnapshot captures recent source activity for telemetry and management.
type SourceSnapshot struct {
	GeneratedAt time.Time      `json:"generated_at"`
	Clients     []SourceClient `json:"clients"`
}

// SourceClient describes the state and connection metrics of an observed source.
type SourceClient struct {
	Address             string    `json:"address"`
	Decision            string    `json:"decision"`
	RuleID              string    `json:"rule_id,omitempty"`
	ActiveConnections   int       `json:"active_connections"`
	AcceptedConnections uint64    `json:"accepted_connections"`
	DeniedConnections   uint64    `json:"denied_connections"`
	CapacityRejections  uint64    `json:"capacity_rejections"`
	LastSeen            time.Time `json:"last_seen"`
}

type trieNode struct {
	hasRule bool
	allowed bool
	ruleID  string
	order   int
	zero    *trieNode
	one     *trieNode
}

func (n *trieNode) applyRule(allowed bool, ruleID string, order int) {
	if !n.hasRule {
		n.hasRule = true
		n.allowed = allowed
		n.ruleID = ruleID
		n.order = order
		return
	}
	// Tie-breaking at the exact same prefix:
	// 1. deny wins over allow.
	if !allowed && n.allowed {
		n.allowed = false
		n.ruleID = ruleID
		n.order = order
		return
	}
	// 2. among rules with the winning action, earliest enabled rule in configuration order supplies rule_id.
	if allowed == n.allowed {
		if order < n.order {
			n.ruleID = ruleID
			n.order = order
		}
	}
	// If incoming is allow and existing is deny, existing deny wins.
}

// Policy is an immutable, compiled source-access policy backed by separate IPv4 and IPv6 binary tries.
type Policy struct {
	v4 *trieNode
	v6 *trieNode
}

// ParsePeer parses only the socket peer address, stripping any port and IPv6 zone,
// and unmapping IPv4-mapped IPv6 addresses. It never inspects Forwarded or X-Forwarded-For headers.
func ParsePeer(remote string) (netip.Addr, error) {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return netip.Addr{}, errors.New("empty peer address")
	}

	if ap, err := netip.ParseAddrPort(remote); err == nil {
		addr := ap.Addr().WithZone("").Unmap()
		if !addr.IsValid() {
			return netip.Addr{}, fmt.Errorf("invalid peer address %q", remote)
		}
		return addr, nil
	}

	bare := strings.Trim(remote, "[]")
	addr, err := netip.ParseAddr(bare)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("invalid peer address %q: %w", remote, err)
	}
	addr = addr.WithZone("").Unmap()
	if !addr.IsValid() {
		return netip.Addr{}, fmt.Errorf("invalid peer address %q", remote)
	}
	return addr, nil
}

// ParsePrefix parses and canonically masks a CIDR prefix string, rejecting
// mapped-IPv6 prefixes, zoned prefixes, and malformed input.
func ParsePrefix(raw string) (netip.Prefix, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return netip.Prefix{}, errors.New("empty prefix")
	}

	p, err := netip.ParsePrefix(raw)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("invalid CIDR prefix %q: %w", raw, err)
	}

	addr := p.Addr()
	if addr.Is4In6() {
		return netip.Prefix{}, fmt.Errorf("mapped-IPv6 prefix %q is not allowed", raw)
	}
	if addr.Zone() != "" {
		return netip.Prefix{}, fmt.Errorf("zoned prefix %q is not allowed", raw)
	}

	return p.Masked(), nil
}

// Compile validates and compiles candidate rules into an immutable Policy.
// Validation enforces at most 256 rules, 1,024 total CIDRs, unique IDs matching validID,
// action exactly "allow" or "deny", and at least one CIDR per rule.
// Disabled rules are syntax-validated but omitted from the trie.
func Compile(rules []RuleSpec) (*Policy, error) {
	if len(rules) > MaxRules {
		return nil, fmt.Errorf("rule count %d exceeds maximum of %d", len(rules), MaxRules)
	}

	totalCIDRs := 0
	seenIDs := make(map[string]struct{}, len(rules))

	for _, r := range rules {
		totalCIDRs += len(r.CIDRs)
		if totalCIDRs > MaxCIDRs {
			return nil, fmt.Errorf("total CIDR count exceeds maximum of %d", MaxCIDRs)
		}
		if !validID.MatchString(r.ID) {
			return nil, fmt.Errorf("invalid rule ID %q", r.ID)
		}
		if _, exists := seenIDs[r.ID]; exists {
			return nil, fmt.Errorf("duplicate rule ID %q", r.ID)
		}
		seenIDs[r.ID] = struct{}{}

		if r.Action != ActionAllow && r.Action != ActionDeny {
			return nil, fmt.Errorf("rule %q: invalid action %q (must be %q or %q)", r.ID, r.Action, ActionAllow, ActionDeny)
		}
		if len(r.CIDRs) == 0 {
			return nil, fmt.Errorf("rule %q: must specify at least one CIDR", r.ID)
		}

		// Validate all CIDRs even if rule is disabled.
		for _, raw := range r.CIDRs {
			if _, err := ParsePrefix(raw); err != nil {
				return nil, fmt.Errorf("rule %q: %w", r.ID, err)
			}
		}
	}

	p := &Policy{}
	for order, r := range rules {
		if !r.Enabled {
			continue
		}
		allowed := r.Action == ActionAllow
		for _, raw := range r.CIDRs {
			prefix, err := ParsePrefix(raw)
			if err != nil {
				return nil, fmt.Errorf("rule %q: %w", r.ID, err)
			}
			p.insert(prefix, allowed, r.ID, order)
		}
	}
	return p, nil
}

func (p *Policy) insert(prefix netip.Prefix, allowed bool, ruleID string, order int) {
	addr := prefix.Addr()
	bits := prefix.Bits()

	if addr.Is4() {
		if p.v4 == nil {
			p.v4 = &trieNode{}
		}
		node := p.v4
		b := addr.As4()
		for i := range bits {
			bit := (b[i/8] >> (7 - (i % 8))) & 1
			if bit == 0 {
				if node.zero == nil {
					node.zero = &trieNode{}
				}
				node = node.zero
			} else {
				if node.one == nil {
					node.one = &trieNode{}
				}
				node = node.one
			}
		}
		node.applyRule(allowed, ruleID, order)
	} else if addr.Is6() {
		if p.v6 == nil {
			p.v6 = &trieNode{}
		}
		node := p.v6
		b := addr.As16()
		for i := range bits {
			bit := (b[i/8] >> (7 - (i % 8))) & 1
			if bit == 0 {
				if node.zero == nil {
					node.zero = &trieNode{}
				}
				node = node.zero
			} else {
				if node.one == nil {
					node.one = &trieNode{}
				}
				node = node.one
			}
		}
		node.applyRule(allowed, ruleID, order)
	}
}

// CompileAllowlist compiles a list of CIDRs as an allowlist policy.
// An empty slice produces an intentional deny-all policy.
func CompileAllowlist(cidrs []string) (*Policy, error) {
	if len(cidrs) == 0 {
		return Compile(nil)
	}
	return Compile([]RuleSpec{
		{
			ID:      "allowlist",
			Action:  ActionAllow,
			CIDRs:   cidrs,
			Enabled: true,
		},
	})
}

// Decide evaluates a canonical IP address against the compiled policy.
// Admission lookup is allocation-free and bounded to at most 32 (IPv4) or 128 (IPv6) bit checks.
// The deepest prefix wins; at equal prefix lengths, deny wins over allow;
// among winning action rules, earliest enabled rule in configuration order supplies rule_id.
// No match is denied with an empty rule_id.
func (p *Policy) Decide(addr netip.Addr) Decision {
	if p == nil {
		return Decision{Allowed: false, RuleID: ""}
	}
	addr = addr.Unmap()
	if !addr.IsValid() {
		return Decision{Allowed: false, RuleID: ""}
	}

	var curr *trieNode
	var match *trieNode

	if addr.Is4() {
		curr = p.v4
		if curr != nil && curr.hasRule {
			match = curr
		}
		b := addr.As4()
		for i := range 32 {
			if curr == nil {
				break
			}
			bit := (b[i/8] >> (7 - (i % 8))) & 1
			if bit == 0 {
				curr = curr.zero
			} else {
				curr = curr.one
			}
			if curr != nil && curr.hasRule {
				match = curr
			}
		}
	} else if addr.Is6() {
		curr = p.v6
		if curr != nil && curr.hasRule {
			match = curr
		}
		b := addr.As16()
		for i := range 128 {
			if curr == nil {
				break
			}
			bit := (b[i/8] >> (7 - (i % 8))) & 1
			if bit == 0 {
				curr = curr.zero
			} else {
				curr = curr.one
			}
			if curr != nil && curr.hasRule {
				match = curr
			}
		}
	}

	if match != nil {
		return Decision{
			Allowed: match.allowed,
			RuleID:  match.ruleID,
		}
	}
	return Decision{
		Allowed: false,
		RuleID:  "",
	}
}
