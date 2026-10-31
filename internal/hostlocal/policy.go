// Package hostlocal evaluates host-local destination policy without DNS, I/O,
// observations or provider-specific assumptions. Own-listener checks are a
// separate, mandatory outer boundary and cannot be overridden by these rules.
package hostlocal

import (
	"errors"
	"net"
	"net/netip"
	"rillway/internal/access"
	"strings"
)

const (
	Open             = "open"
	LocalClientsOnly = "local-clients-only"
	Strict           = "strict"
	MaxRules         = 256
	MaxSources       = 1024
)

// ErrDenied is safe for clients; do not substitute an address-bearing dial error.
var ErrDenied = errors.New("host-local access is restricted; ask the administrator to review the client and destination exception")

// RuleSpec is a config-independent exception. AnySource distinguishes an
// omitted source restriction from an explicitly empty (invalid) restriction.
type RuleSpec struct {
	IP        netip.Addr
	Lo, Hi    uint16
	From      []string
	AnySource bool
}

type rule struct {
	ip        netip.Addr
	lo, hi    uint16
	from      *access.Policy
	anySource bool
}

// Policy is immutable after Compile and may be shared across concurrent dials.
type Policy struct {
	mode  string
	rules []rule
	own   map[netip.Addr]bool
}

// Decision describes an observed policy decision, not a connectivity test.
// HypotheticalDenied applies local-clients-only with the same exceptions and
// baseline. It is independent of Denied: the two must never be summed.
// NeedsDirectIP is provisional; neither flag may be counted until resolved.
type Decision struct {
	Denied             bool
	HypotheticalDenied bool
	HostLocal          bool
	NeedsDirectIP      bool
	Reason             string
}

// Compile takes ownership of no caller data. ownIPs classify destinations only;
// only a loopback socket peer is a local source, including on a multi-IP host.
func Compile(mode string, specs []RuleSpec, ownIPs []netip.Addr) (*Policy, error) {
	if mode == "" {
		mode = LocalClientsOnly
	}
	if mode != Open && mode != LocalClientsOnly && mode != Strict {
		return nil, errors.New("invalid host-local mode")
	}
	if len(specs) > MaxRules {
		return nil, errors.New("too many host-local exceptions")
	}
	p := &Policy{mode: mode, own: make(map[netip.Addr]bool)}
	for _, ip := range ownIPs {
		if ip.IsValid() {
			p.own[canonical(ip)] = true
		}
	}
	total := 0
	for _, spec := range specs {
		total += len(spec.From)
		if !spec.IP.IsValid() || spec.Lo == 0 || spec.Hi < spec.Lo || total > MaxSources {
			return nil, errors.New("invalid host-local exception or source limit")
		}
		if spec.AnySource && len(spec.From) != 0 || !spec.AnySource && len(spec.From) == 0 {
			return nil, errors.New("host-local exception must omit sources or provide a nonempty source list")
		}
		var from *access.Policy
		if !spec.AnySource {
			var err error
			from, err = access.CompileAllowlist(spec.From)
			if err != nil {
				return nil, errors.New("invalid host-local exception source CIDR")
			}
		}
		p.rules = append(p.rules, rule{ip: canonical(spec.IP), lo: spec.Lo, hi: spec.Hi, from: from, anySource: spec.AnySource})
	}
	return p, nil
}

func canonical(ip netip.Addr) netip.Addr { return ip.WithZone("").Unmap() }

func (p *Policy) Mode() string { return p.mode }

func (p *Policy) excepted(dst netip.AddrPort, source netip.Addr) bool {
	for _, r := range p.rules {
		if r.ip == dst.Addr() && r.lo <= dst.Port() && dst.Port() <= r.hi && (r.anySource || r.from.Decide(source).Allowed) {
			return true
		}
	}
	return false
}

// IsBaseline identifies the exceptionable link-local and metadata baseline.
// Own proxy, management, PAC and UDP relay listeners are checked separately.
func IsBaseline(ip netip.Addr) bool {
	ip = canonical(ip)
	return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip == netip.MustParseAddr("fd00:ec2::254") ||
		ip == netip.MustParseAddr("192.0.0.192") ||
		ip == netip.MustParseAddr("100.100.100.200")
}

func (p *Policy) decide(local, baseline, exception bool, source netip.Addr) Decision {
	d := Decision{HostLocal: local}
	if exception || !local {
		return d
	}
	localSource := canonical(source).IsLoopback()
	d.HypotheticalDenied = baseline || !localSource
	d.Denied = baseline || p.mode == Strict || p.mode == LocalClientsOnly && !localSource
	if baseline {
		d.Reason = "protected_metadata"
	} else if d.Denied || d.HypotheticalDenied {
		d.Reason = "host_local"
	}
	return d
}

// CheckIP checks a literal or a verified numeric direct destination. It does not
// assume private/overlay subnets belong to this host.
func (p *Policy) CheckIP(dst netip.AddrPort, source netip.Addr) Decision {
	if !dst.IsValid() || dst.Port() == 0 {
		return Decision{Denied: true, Reason: "invalid_destination"}
	}
	ip := canonical(dst.Addr())
	dst = netip.AddrPortFrom(ip, dst.Port())
	baseline := IsBaseline(ip)
	local := baseline || ip.IsLoopback() || ip.IsUnspecified() || p.own[ip]
	return p.decide(local, baseline, p.excepted(dst, canonical(source)), source)
}

func localhost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	return host == "localhost" || strings.HasSuffix(host, ".localhost")
}

// CheckAddress evaluates literals immediately. A localhost spelling needs a
// selected direct provider's numeric check to match IP-only exceptions. Other
// names have unknown resolved identity, never an invented public/local result.
func (p *Policy) CheckAddress(address string, source netip.Addr) Decision {
	if dst, err := netip.ParseAddrPort(address); err == nil {
		return p.CheckIP(dst, source)
	}
	host, _, err := net.SplitHostPort(address)
	if err == nil && localhost(host) {
		return Decision{HostLocal: true, NeedsDirectIP: true, Reason: "requires_direct_resolution"}
	}
	return Decision{}
}

// CheckResolved also retains the host-local classification of localhost names,
// even if a nonstandard resolver maps them outside loopback. Exceptions match
// only the numeric result actually being attempted, not an assumed alias.
func (p *Policy) CheckResolved(address string, dst netip.AddrPort, source netip.Addr) Decision {
	d := p.CheckIP(dst, source)
	if !dst.IsValid() || dst.Port() == 0 {
		return d
	}
	host, _, err := net.SplitHostPort(address)
	if err == nil && localhost(host) {
		dst = netip.AddrPortFrom(canonical(dst.Addr()), dst.Port())
		return p.decide(true, IsBaseline(dst.Addr()), p.excepted(dst, canonical(source)), source)
	}
	return d
}

// CheckRemoteName cannot prove an IP exception for a remote-resolved localhost
// name. It denies only when mode/source needs that proof. Existing stricter
// provider private-route policies still apply. Ordinary names remain literal-only.
func (p *Policy) CheckRemoteName(address string, source netip.Addr) Decision {
	d := p.CheckAddress(address, source)
	if d.NeedsDirectIP {
		d = p.decide(true, false, false, source)
		if d.Denied {
			d.Reason = "unverified_localhost_exception"
		}
	}
	return d
}
