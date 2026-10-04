package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	ErrConflict = errors.New("configuration revision conflict")
	validID     = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
)

func Default(stateDir string) Config {
	return Config{
		Version: 1, Revision: 1,
		Listeners: Listeners{HTTP: "127.0.0.1:17890", SOCKS5: "127.0.0.1:17891", Admin: "127.0.0.1:17892", PAC: "127.0.0.1:17893"},
		Security:  Security{AllowedClients: []string{"127.0.0.0/8", "::1/128"}, AdminTokenFile: filepath.Join(stateDir, "admin.token"), TLSCertFile: filepath.Join(stateDir, "admin.crt"), TLSKeyFile: filepath.Join(stateDir, "admin.key")},
		Outbounds: []Outbound{{ID: "direct", Type: "direct", Enabled: true, PublicInternet: true}, {ID: "warp", Type: "warp", Enabled: false, PublicInternet: true, ProxyAddress: "127.0.0.1:40000", WARPBinary: "warp-cli"}},
		Rules: []Rule{
			{ID: "github-origin", Domains: []string{"github.com", "api.github.com", "codeload.github.com"}, Outbound: "direct", Family: "auto"},
			{ID: "github-cdn", Domains: []string{"raw.githubusercontent.com", "avatars.githubusercontent.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com"}, Suffixes: []string{"githubassets.com"}, Outbound: "warp", Family: "auto"},
		}, DefaultOutbound: "direct",
		Adaptive: Adaptive{Candidates: []string{"direct", "warp"}, WindowSeconds: 600, MinSamples: 3, ImprovementPercent: 25, ImprovementMillis: 50, CooldownSeconds: 600, ProbesPerMinute: 12, ProbeConcurrency: 2, ProbeTimeoutSeconds: 4},
		PAC:      PAC{ProxyAddress: "127.0.0.1:17890", BypassDomains: []string{"local", "ts.net", "tailscale.com"}, BypassCIDRs: []string{"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "100.64.0.0/10", "::1/128", "fc00::/7", "fe80::/10"}},
	}
}

func Decode(data []byte) (Config, error) {
	var c Config
	if len(data) > 1<<20 {
		return c, errors.New("configuration exceeds 1 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return c, errors.New("configuration must contain one JSON object")
	}
	return c, Validate(c)
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	return Decode(data)
}

func Validate(c Config) error {
	if c.Version != 1 {
		return fmt.Errorf("unsupported config version %d", c.Version)
	}
	if c.Revision == 0 {
		return errors.New("revision must be positive")
	}
	seen := map[string]bool{}
	for _, addr := range []string{c.Listeners.HTTP, c.Listeners.SOCKS5, c.Listeners.Admin, c.Listeners.PAC} {
		if addr == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return fmt.Errorf("listener %q: %w", addr, err)
		}
		if seen[addr] {
			return fmt.Errorf("duplicate listener %q", addr)
		}
		seen[addr] = true
	}
	if c.Listeners.Admin == "" {
		return errors.New("admin listener is required")
	}
	if len(c.Security.AllowedClients) == 0 {
		return errors.New("allowed_clients must not be empty")
	}
	for _, p := range c.Security.AllowedClients {
		if _, err := netip.ParsePrefix(p); err != nil {
			return fmt.Errorf("allowed_clients: %w", err)
		}
	}
	for _, s := range []string{c.Security.AdminTokenFile, c.Security.TLSCertFile, c.Security.TLSKeyFile} {
		if s == "" {
			return errors.New("management token and TLS file paths are required")
		}
	}
	if (c.Security.ProxyUsername == "") != (c.Security.ProxyPasswordFile == "") {
		return errors.New("proxy username and password file must be configured together")
	}
	outs := map[string]Outbound{}
	for _, o := range c.Outbounds {
		if !validID.MatchString(o.ID) {
			return fmt.Errorf("invalid outbound ID %q", o.ID)
		}
		if _, ok := outs[o.ID]; ok {
			return fmt.Errorf("duplicate outbound %q", o.ID)
		}
		outs[o.ID] = o
		switch o.Type {
		case "direct":
		case "warp":
			if o.ProxyAddress == "" {
				return errors.New("WARP proxy_address is required")
			}
			if _, _, err := net.SplitHostPort(o.ProxyAddress); err != nil {
				return err
			}
		case "wireguard":
			if o.ConfigFile == "" {
				return errors.New("WireGuard config_file is required")
			}
		case "tailscale":
			if o.StateDir == "" {
				return errors.New("tailscale state_dir is required")
			}
		default:
			return fmt.Errorf("unknown outbound type %q", o.Type)
		}
	}
	if o, ok := outs["direct"]; !ok || o.Type != "direct" || !o.Enabled || !o.PublicInternet {
		return PublicError{Message: DirectRequired}
	}
	if o, ok := outs[c.DefaultOutbound]; !ok || !o.Enabled {
		return errors.New("default_outbound must refer to an enabled outbound")
	}
	checkCandidates := func(ids []string) error {
		if len(ids) == 0 {
			return errors.New("adaptive candidates must not be empty")
		}
		for _, id := range ids {
			o, ok := outs[id]
			if !ok {
				return fmt.Errorf("unknown candidate %q", id)
			}
			if o.Type == "tailscale" || !o.PublicInternet {
				return fmt.Errorf("candidate %q is not a public internet outbound", id)
			}
		}
		return nil
	}
	rules := map[string]bool{}
	for _, r := range c.Rules {
		if !validID.MatchString(r.ID) || rules[r.ID] {
			return fmt.Errorf("invalid or duplicate rule ID %q", r.ID)
		}
		rules[r.ID] = true
		if len(r.Domains)+len(r.Suffixes)+len(r.CIDRs) == 0 {
			return fmt.Errorf("rule %q has no match", r.ID)
		}
		for _, d := range append(append([]string{}, r.Domains...), r.Suffixes...) {
			if !validDomain(d) {
				return fmt.Errorf("invalid rule domain %q", d)
			}
		}
		for _, p := range r.CIDRs {
			if _, err := netip.ParsePrefix(p); err != nil {
				return fmt.Errorf("rule CIDR: %w", err)
			}
		}
		if r.Family != "" && r.Family != "auto" && r.Family != "ipv4" && r.Family != "ipv6" {
			return fmt.Errorf("unknown address family %q", r.Family)
		}
		if r.Adaptive {
			if r.Outbound != "" {
				return errors.New("adaptive rule cannot also set outbound")
			}
			if err := checkCandidates(r.Candidates); err != nil {
				return err
			}
		} else if _, ok := outs[r.Outbound]; !ok {
			return fmt.Errorf("unknown outbound %q", r.Outbound)
		}
	}
	if err := checkCandidates(c.Adaptive.Candidates); err != nil {
		return err
	}
	a := c.Adaptive
	if a.WindowSeconds < 1 || a.WindowSeconds > 86400 || a.MinSamples < 3 || a.MinSamples > 20 || a.ImprovementPercent < 1 || a.ImprovementPercent > 99 || a.ImprovementMillis < 0 || a.CooldownSeconds < 1 || a.ProbesPerMinute < 1 || a.ProbesPerMinute > 12 || a.ProbeConcurrency < 1 || a.ProbeConcurrency > 2 || a.ProbeTimeoutSeconds < 1 || a.ProbeTimeoutSeconds > 4 {
		return errors.New("invalid adaptive settings or probe budget exceeds limits")
	}
	if _, _, err := net.SplitHostPort(c.PAC.ProxyAddress); err != nil {
		return fmt.Errorf("PAC proxy address: %w", err)
	}
	for _, d := range c.PAC.BypassDomains {
		if !validDomain(d) {
			return fmt.Errorf("invalid bypass domain %q", d)
		}
	}
	for _, p := range c.PAC.BypassCIDRs {
		if _, err := netip.ParsePrefix(p); err != nil {
			return err
		}
	}
	return nil
}

func validDomain(s string) bool {
	s = strings.Trim(s, ".")
	if s == "" || len(s) > 253 || strings.ContainsAny(s, "/:@*\\ \t\r\n") {
		return false
	}
	for _, r := range s {
		if r < 'a' || r > 'z' {
			if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
				continue
			}
			return false
		}
	}
	return true
}

func Save(path string, c Config) error {
	if err := Validate(c); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return WritePrivate(path, append(data, '\n'))
}

// WritePrivate atomically replaces a file without ever exposing a partial write.
func WritePrivate(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".rillway-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }() // temporary file only
	if err = f.Chmod(0o600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}
