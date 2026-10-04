package outbound

import (
	"bufio"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// WGConfig is a deliberately restricted wg-quick configuration. Shell hooks,
// host route mutations, SaveConfig and unknown fields are rejected.
type WGConfig struct {
	Addresses  []netip.Addr
	DNS        []netip.Addr
	MTU        int
	privateKey string
	listenPort uint16
	peers      []wgPeer
}
type wgPeer struct {
	publicKey, presharedKey, endpoint string
	allowed                           []netip.Prefix
	keepalive                         uint16
}

func ParseWireGuard(text string) (WGConfig, error) {
	c := WGConfig{MTU: 1420}
	section := ""
	var peer *wgPeer
	seenInterface := false
	seen := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 4096), 65536)
	for line := 1; scanner.Scan(); line++ {
		s := strings.TrimSpace(strings.SplitN(scanner.Text(), "#", 2)[0])
		if s == "" {
			continue
		}
		if strings.HasPrefix(s, "[") {
			switch s {
			case "[Interface]":
				if seenInterface {
					return c, errors.New("duplicate Interface section")
				}
				seenInterface = true
				section = "interface"
			case "[Peer]":
				if !seenInterface {
					return c, errors.New("peer before interface")
				}
				section = "peer"
				c.peers = append(c.peers, wgPeer{})
				peer = &c.peers[len(c.peers)-1]
			default:
				return c, fmt.Errorf("line %d: unknown section", line)
			}
			seen = map[string]bool{}
			continue
		}
		key, value, ok := strings.Cut(s, "=")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !ok || section == "" || value == "" {
			return c, fmt.Errorf("line %d: invalid field", line)
		}
		if seen[key] {
			return c, fmt.Errorf("line %d: duplicate %s", line, key)
		}
		seen[key] = true
		var err error
		switch section + "." + key {
		case "interface.PrivateKey":
			c.privateKey, err = wgKey(value)
		case "interface.Address":
			for _, v := range strings.Split(value, ",") {
				p, e := netip.ParsePrefix(strings.TrimSpace(v))
				if e != nil {
					err = errors.New("address must contain IP/CIDR")
					break
				}
				c.Addresses = append(c.Addresses, p.Addr())
			}
		case "interface.DNS":
			for _, v := range strings.Split(value, ",") {
				a, e := netip.ParseAddr(strings.TrimSpace(v))
				if e != nil {
					err = errors.New("DNS must contain literal IPs, not search domains")
					break
				}
				c.DNS = append(c.DNS, a)
			}
		case "interface.MTU":
			c.MTU, err = strconv.Atoi(value)
			if err == nil && (c.MTU < 1280 || c.MTU > 65535) {
				err = errors.New("MTU must be 1280..65535")
			}
		case "interface.ListenPort":
			c.listenPort, err = parseUint16(value)
		case "peer.PublicKey":
			peer.publicKey, err = wgKey(value)
		case "peer.PresharedKey":
			peer.presharedKey, err = wgKey(value)
		case "peer.Endpoint":
			host, port, e := net.SplitHostPort(value)
			if e != nil || host == "" {
				err = errors.New("endpoint must be host:port")
			} else if p, e := parseUint16(port); e != nil || p == 0 {
				err = errors.New("invalid Endpoint port")
			} else {
				peer.endpoint = value
			}
		case "peer.AllowedIPs":
			for _, v := range strings.Split(value, ",") {
				p, e := netip.ParsePrefix(strings.TrimSpace(v))
				if e != nil {
					err = errors.New("invalid AllowedIPs")
					break
				}
				peer.allowed = append(peer.allowed, p.Masked())
			}
		case "peer.PersistentKeepalive":
			peer.keepalive, err = parseUint16(value)
		default:
			return c, fmt.Errorf("line %d: unsupported field %q (shell hooks and host network changes are forbidden)", line, key)
		}
		if err != nil {
			return c, fmt.Errorf("line %d: %s: %w", line, key, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return c, err
	}
	if c.privateKey == "" || len(c.Addresses) == 0 || len(c.peers) == 0 {
		return c, errors.New("WireGuard requires PrivateKey, Address and at least one Peer")
	}
	for _, p := range c.peers {
		if p.publicKey == "" || p.endpoint == "" || len(p.allowed) == 0 {
			return c, errors.New("each Peer requires PublicKey, Endpoint and AllowedIPs")
		}
	}
	for _, a := range c.DNS {
		if !c.allows(a) {
			return c, errors.New("tunnel DNS is outside peer AllowedIPs")
		}
	}
	return c, nil
}

func (c WGConfig) allows(a netip.Addr) bool {
	for _, p := range c.peers {
		for _, prefix := range p.allowed {
			if prefix.Contains(a.Unmap()) {
				return true
			}
		}
	}
	return false
}

func parseUint16(s string) (uint16, error) { v, e := strconv.ParseUint(s, 10, 16); return uint16(v), e }

func wgKey(s string) (string, error) {
	b, e := base64.StdEncoding.DecodeString(s)
	if e != nil || len(b) != 32 {
		return "", errors.New("key must be base64 encoding of 32 bytes")
	}
	zero := true
	for _, v := range b {
		if v != 0 {
			zero = false
		}
	}
	if zero {
		return "", errors.New("all-zero key is invalid")
	}
	return hex.EncodeToString(b), nil
}
