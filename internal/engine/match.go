package engine

import (
	"net/netip"
	"rillway/internal/config"
	"slices"
	"strconv"
	"strings"
)

type target struct {
	host    string
	port    string
	source  netip.Addr
	inbound string
}

type portRange struct{ lo, hi uint16 }

type matcher struct {
	domains  map[string]struct{}
	suffixes []string
	cidrs    []netip.Prefix
	sources  []netip.Prefix
	inbounds []string
	ports    []portRange
}

func normalize(host string) string { return strings.TrimSuffix(strings.ToLower(host), ".") }

func suffixMatch(host, suffix string) bool {
	suffix = normalize(strings.TrimPrefix(suffix, "."))
	return host == suffix || strings.HasSuffix(host, "."+suffix)
}

// parsePortRange shares the configuration validator's port syntax.
func parsePortRange(v string) (portRange, error) {
	lo, hi, err := config.ParsePortRange(v)
	return portRange{lo: lo, hi: hi}, err
}

func compileTarget(r config.Rule) matcher {
	m := matcher{inbounds: slices.Clone(r.Inbounds)}
	if len(r.Domains) > 0 {
		m.domains = make(map[string]struct{}, len(r.Domains))
		for _, domain := range r.Domains {
			m.domains[normalize(domain)] = struct{}{}
		}
	}
	for _, suffix := range r.Suffixes {
		suffix = normalize(strings.TrimPrefix(suffix, "."))
		if !slices.Contains(m.suffixes, suffix) {
			m.suffixes = append(m.suffixes, suffix)
		}
	}
	m.cidrs = compilePrefixes(r.CIDRs)
	m.sources = compilePrefixes(r.Sources)
	for _, text := range r.Ports {
		if ports, err := parsePortRange(text); err == nil && !slices.Contains(m.ports, ports) {
			m.ports = append(m.ports, ports)
		}
	}
	return m
}

func compilePrefixes(list []string) []netip.Prefix {
	var prefixes []netip.Prefix
	for _, text := range list {
		if prefix, err := netip.ParsePrefix(text); err == nil {
			prefix = prefix.Masked()
			if !slices.Contains(prefixes, prefix) {
				prefixes = append(prefixes, prefix)
			}
		}
	}
	return prefixes
}

func compileRule(r config.Rule, sets map[string]config.RuleSet) matcher {
	if len(r.Sets) == 0 {
		return compileTarget(r)
	}
	// These lists belong to the compiler, never to the caller's configuration.
	r.Domains = slices.Clone(r.Domains)
	r.Suffixes = slices.Clone(r.Suffixes)
	r.CIDRs = slices.Clone(r.CIDRs)
	r.Sources = slices.Clone(r.Sources)
	for _, id := range r.Sets {
		set, ok := sets[id]
		if !ok {
			continue // Configuration validation rejects missing rule sets.
		}
		r.Domains = append(r.Domains, set.Domains...)
		r.Suffixes = append(r.Suffixes, set.Suffixes...)
		r.CIDRs = append(r.CIDRs, set.CIDRs...)
		r.Sources = append(r.Sources, set.Sources...)
	}
	return compileTarget(r)
}

func prefixesMatch(prefixes []netip.Prefix, ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	ip = ip.WithZone("").Unmap()
	for _, prefix := range prefixes {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

// match requires every populated condition group to match. Alternatives within
// a group are ORed; unknown ingress metadata never satisfies its conditions.
func (m matcher) match(t target) bool {
	if len(m.domains) > 0 {
		if _, ok := m.domains[t.host]; !ok {
			return false
		}
	}
	if len(m.suffixes) > 0 {
		matched := false
		for _, suffix := range m.suffixes {
			if t.host == suffix || strings.HasSuffix(t.host, "."+suffix) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(m.cidrs) > 0 {
		// Only client-provided literal IPs match. No host DNS lookup is allowed.
		ip, err := netip.ParseAddr(t.host)
		if err != nil || !prefixesMatch(m.cidrs, ip) {
			return false
		}
	}
	if len(m.sources) > 0 && !prefixesMatch(m.sources, t.source) {
		return false
	}
	if len(m.inbounds) > 0 && (t.inbound == "" || !slices.Contains(m.inbounds, t.inbound)) {
		return false
	}
	if len(m.ports) > 0 {
		port, err := strconv.ParseUint(t.port, 10, 16)
		if err != nil || port == 0 {
			return false
		}
		matched := false
		for _, ports := range m.ports {
			if uint16(port) >= ports.lo && uint16(port) <= ports.hi {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
