package config

import (
	"errors"
	"net"
	"net/netip"
	"rillway/internal/access"
	"rillway/internal/hostlocal"
	"strconv"
	"strings"
)

// HostLocal is optional on disk. Omission protects immediately, including after
// upgrade; reading an old file does not rewrite it. Setup writes the mode.
type HostLocal struct {
	Mode  string          `json:"mode"`
	Allow []HostLocalRule `json:"allow,omitempty"`
}

type HostLocalRule struct {
	To   string   `json:"to"`
	From []string `json:"from,omitempty"`
}

func (c Config) HostLocalMode() string {
	if c.HostLocal == nil || c.HostLocal.Mode == "" {
		return hostlocal.LocalClientsOnly
	}
	return c.HostLocal.Mode
}

// CompileHostLocal performs no interface enumeration or DNS. At runtime ownIPs
// supplies the separate host-destination set, never a source-local allowlist.
func (c Config) CompileHostLocal(ownIPs []netip.Addr) (*hostlocal.Policy, error) {
	var rules []hostlocal.RuleSpec
	if c.HostLocal != nil {
		if len(c.HostLocal.Allow) > hostlocal.MaxRules {
			return nil, errors.New("too many host-local exceptions")
		}
		for _, entry := range c.HostLocal.Allow {
			host, ports, err := net.SplitHostPort(entry.To)
			if err != nil {
				return nil, errors.New("host-local exception requires a literal IP and port or port range")
			}
			ip, err := netip.ParseAddr(host)
			if err != nil {
				return nil, errors.New("host-local exception requires a literal IP")
			}
			ip = ip.WithZone("").Unmap()
			lo, hi, err := ParsePortRange(ports)
			if err != nil {
				return nil, errors.New("invalid host-local exception port range")
			}
			for _, listener := range []string{c.Listeners.HTTP, c.Listeners.SOCKS5, c.Listeners.Admin, c.Listeners.PAC} {
				if exceptionHitsListener(ip, lo, hi, listener, ownIPs) {
					return nil, errors.New("host-local exceptions cannot allow Rillway listeners")
				}
			}
			rules = append(rules, hostlocal.RuleSpec{IP: ip, Lo: lo, Hi: hi, From: entry.From, AnySource: entry.From == nil})
		}
	}
	return hostlocal.Compile(c.HostLocalMode(), rules, ownIPs)
}

func exceptionHitsListener(ip netip.Addr, lo, hi uint16, listener string, own []netip.Addr) bool {
	host, port, err := net.SplitHostPort(listener)
	if err != nil {
		return false
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 || uint16(n) < lo || hi < uint16(n) {
		return false
	}
	bound, err := netip.ParseAddr(host)
	if err != nil && host != "" {
		// Do not resolve a configured listener merely to validate an exception.
		// A named bind may be any local address: reject potentially matching
		// local endpoints, with actual bound sockets still authoritative.
		name := strings.TrimSuffix(strings.ToLower(host), ".")
		if name == "localhost" || strings.HasSuffix(name, ".localhost") {
			return ip.IsLoopback()
		}
		if ip.IsLoopback() || ip.IsUnspecified() {
			return true
		}
		for _, local := range own {
			if local.WithZone("").Unmap() == ip {
				return true
			}
		}
		return false
	}
	if bound.IsValid() {
		bound = bound.WithZone("").Unmap()
	}
	if bound == ip {
		return true
	}
	if host == "" || bound.IsUnspecified() {
		if bound.Is4() && !ip.Is4() {
			return false
		}
		if ip.IsLoopback() || ip.IsUnspecified() {
			return true
		}
		for _, local := range own {
			if local.WithZone("").Unmap() == ip {
				return true
			}
		}
	}
	return false
}

// HostLocalWarnings contains only fixed operator guidance, never submitted data.
// Offline output cannot establish listener identities or report live traffic.
func (c Config) HostLocalWarnings() []string {
	var warnings []string
	if c.HostLocal == nil {
		return warnings
	}
	broad := false
	baseline, ranges := false, false
	for _, entry := range c.HostLocal.Allow {
		if host, ports, err := net.SplitHostPort(entry.To); err == nil {
			if ip, err := netip.ParseAddr(host); err == nil && hostlocal.IsBaseline(ip) {
				baseline = true
			}
			if lo, hi, err := ParsePortRange(ports); err == nil && lo != hi {
				ranges = true
			}
		}
		if entry.From == nil {
			broad = true
		}
		for _, raw := range entry.From {
			if prefix, err := access.ParsePrefix(raw); err == nil && prefix.Bits() == 0 {
				broad = true
			}
		}
	}
	if broad {
		warnings = append(warnings, "A host-local exception applies to every source in an address family or to all clients.")
	}
	if baseline {
		warnings = append(warnings, "A host-local exception permits a protected link-local or metadata destination.")
	}
	if ranges {
		warnings = append(warnings, "A host-local exception permits a range of destination ports.")
	}
	return warnings
}
