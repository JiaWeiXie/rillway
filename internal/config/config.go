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
	"rillway/internal/access"
	"strings"
)

var (
	ErrConflict = errors.New("configuration revision conflict")
	validID     = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
)

const (
	PACDomainLocalhost        = "domain.localhost"
	PACDomainMDNS             = "domain.mdns"
	PACDomainHome             = "domain.home"
	PACDomainTailscaleDNS     = "domain.tailscale-dns"
	PACDomainTailscaleControl = "domain.tailscale-control"
	PACDomainDockerHost       = "domain.docker-host"
	PACDomainDockerGateway    = "domain.docker-gateway"
	PACDomainDockerVM         = "domain.docker-vm"
	PACDomainKubernetes       = "domain.kubernetes"
	PACCIDRLoopback4          = "cidr.loopback-v4"
	PACCIDRPrivate10          = "cidr.private-10"
	PACCIDRPrivate172         = "cidr.private-172"
	PACCIDRPrivate192         = "cidr.private-192"
	PACCIDRLinkLocal4         = "cidr.link-local-v4"
	PACCIDRTailscale          = "cidr.tailscale"
	PACCIDRTailscale6         = "cidr.tailscale-v6"
	PACCIDRLoopback6          = "cidr.loopback-v6"
	PACCIDRULA6               = "cidr.ula-v6"
	PACCIDRLinkLocal6         = "cidr.link-local-v6"
)

var pacPresets = map[string]struct{}{
	PACDomainLocalhost: {}, PACDomainMDNS: {}, PACDomainHome: {},
	PACDomainTailscaleDNS: {}, PACDomainTailscaleControl: {},
	PACDomainDockerHost: {}, PACDomainDockerGateway: {}, PACDomainDockerVM: {},
	PACDomainKubernetes: {}, PACCIDRLoopback4: {}, PACCIDRPrivate10: {},
	PACCIDRPrivate172: {}, PACCIDRPrivate192: {}, PACCIDRLinkLocal4: {},
	PACCIDRTailscale: {}, PACCIDRTailscale6: {}, PACCIDRLoopback6: {}, PACCIDRULA6: {}, PACCIDRLinkLocal6: {},
}

var legacyPACPresets = map[string]string{
	"localhost":               PACDomainLocalhost,
	"local":                   PACDomainMDNS,
	"home.arpa":               PACDomainHome,
	"ts.net":                  PACDomainTailscaleDNS,
	"tailscale.com":           PACDomainTailscaleControl,
	"host.docker.internal":    PACDomainDockerHost,
	"gateway.docker.internal": PACDomainDockerGateway,
	"vm.docker.internal":      PACDomainDockerVM,
	"cluster.local":           PACDomainKubernetes,
	"127.0.0.0/8":             PACCIDRLoopback4,
	"10.0.0.0/8":              PACCIDRPrivate10,
	"172.16.0.0/12":           PACCIDRPrivate172,
	"192.168.0.0/16":          PACCIDRPrivate192,
	"169.254.0.0/16":          PACCIDRLinkLocal4,
	"100.64.0.0/10":           PACCIDRTailscale,
	"fd7a:115c:a1e0::/48":     PACCIDRTailscale6,
	"::1/128":                 PACCIDRLoopback6,
	"fc00::/7":                PACCIDRULA6,
	"fe80::/10":               PACCIDRLinkLocal6,
}

type rawPAC struct {
	ProxyAddress  string          `json:"proxy_address"`
	BypassDomains json.RawMessage `json:"bypass_domains"`
	BypassCIDRs   json.RawMessage `json:"bypass_cidrs"`
}

// UnmarshalJSON accepts both the current row format and the original string
// arrays so upgrades never strand an existing daemon configuration.
func (p *PAC) UnmarshalJSON(data []byte) error {
	var raw rawPAC
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	domains, legacyDomains, err := decodePACBypasses(raw.BypassDomains)
	if err != nil {
		return fmt.Errorf("bypass_domains: %w", err)
	}
	cidrs, legacyCIDRs, err := decodePACBypasses(raw.BypassCIDRs)
	if err != nil {
		return fmt.Errorf("bypass_cidrs: %w", err)
	}
	if legacyDomains {
		domains = mergePACDefaults(domains, defaultPACDomains())
	}
	if legacyCIDRs {
		cidrs = mergePACDefaults(cidrs, defaultPACCIDRs())
	}
	*p = PAC{ProxyAddress: raw.ProxyAddress, BypassDomains: domains, BypassCIDRs: cidrs}
	return nil
}

func decodePACBypasses(data []byte) ([]PACBypass, bool, error) {
	if len(data) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil, false, nil
	}
	var legacy []string
	if err := json.Unmarshal(data, &legacy); err == nil {
		result := make([]PACBypass, 0, len(legacy))
		for _, value := range legacy {
			result = append(result, PACBypass{Value: value, Enabled: true, Preset: legacyPACPresets[strings.ToLower(value)]})
		}
		return result, len(legacy) > 0, nil
	}
	var current []PACBypass
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&current); err != nil {
		return nil, false, err
	}
	return current, false, nil
}

func mergePACDefaults(entries, defaults []PACBypass) []PACBypass {
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		seen[strings.ToLower(entry.Value)] = true
	}
	for _, entry := range defaults {
		if !seen[strings.ToLower(entry.Value)] {
			entries = append(entries, entry)
		}
	}
	return entries
}

func defaultPACDomains() []PACBypass {
	return []PACBypass{
		{Value: "localhost", Enabled: true, Preset: PACDomainLocalhost},
		{Value: "local", Enabled: true, Preset: PACDomainMDNS},
		{Value: "home.arpa", Enabled: true, Preset: PACDomainHome},
		{Value: "ts.net", Enabled: true, Preset: PACDomainTailscaleDNS},
		{Value: "tailscale.com", Enabled: true, Preset: PACDomainTailscaleControl},
		{Value: "host.docker.internal", Enabled: true, Preset: PACDomainDockerHost},
		{Value: "gateway.docker.internal", Enabled: true, Preset: PACDomainDockerGateway},
		{Value: "vm.docker.internal", Enabled: true, Preset: PACDomainDockerVM},
		{Value: "cluster.local", Enabled: true, Preset: PACDomainKubernetes},
	}
}

func defaultPACCIDRs() []PACBypass {
	return []PACBypass{
		{Value: "127.0.0.0/8", Enabled: true, Preset: PACCIDRLoopback4},
		{Value: "10.0.0.0/8", Enabled: true, Preset: PACCIDRPrivate10},
		{Value: "172.16.0.0/12", Enabled: true, Preset: PACCIDRPrivate172},
		{Value: "192.168.0.0/16", Enabled: true, Preset: PACCIDRPrivate192},
		{Value: "169.254.0.0/16", Enabled: true, Preset: PACCIDRLinkLocal4},
		{Value: "100.64.0.0/10", Enabled: true, Preset: PACCIDRTailscale},
		{Value: "fd7a:115c:a1e0::/48", Enabled: true, Preset: PACCIDRTailscale6},
		{Value: "::1/128", Enabled: true, Preset: PACCIDRLoopback6},
		{Value: "fc00::/7", Enabled: true, Preset: PACCIDRULA6},
		{Value: "fe80::/10", Enabled: true, Preset: PACCIDRLinkLocal6},
	}
}

func enabledPACValues(entries []PACBypass) []string {
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Enabled {
			result = append(result, entry.Value)
		}
	}
	return result
}

func (p PAC) EnabledDomains() []string { return enabledPACValues(p.BypassDomains) }

func (p PAC) EnabledCIDRs() []string { return enabledPACValues(p.BypassCIDRs) }

func Default(stateDir string) Config {
	return Config{
		Version: 2, Revision: 1,
		Listeners: Listeners{HTTP: "127.0.0.1:17890", SOCKS5: "127.0.0.1:17891", Admin: "127.0.0.1:17892", PAC: "127.0.0.1:17893"},
		Security:  Security{AllowedClients: []string{"127.0.0.0/8", "::1/128"}, AdminTokenFile: filepath.Join(stateDir, "admin.token"), TLSCertFile: filepath.Join(stateDir, "admin.crt"), TLSKeyFile: filepath.Join(stateDir, "admin.key")},
		SourceAccess: SourceAccess{
			Rules: []SourceAccessRule{
				{
					ID:      "local-clients",
					Name:    "Local clients",
					Action:  "allow",
					CIDRs:   []string{"127.0.0.0/8", "::1/128"},
					Enabled: true,
				},
			},
		},
		Outbounds: []Outbound{{ID: "direct", Type: "direct", Enabled: true, PublicInternet: true}, {ID: "warp", Type: "warp", Enabled: false, PublicInternet: true, ProxyAddress: "127.0.0.1:40000", WARPBinary: "warp-cli"}},
		Rules: []Rule{
			{ID: "github-origin", Domains: []string{"github.com", "api.github.com", "codeload.github.com"}, Outbound: "direct", Family: "auto"},
			{ID: "github-cdn", Domains: []string{"raw.githubusercontent.com", "avatars.githubusercontent.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com"}, Suffixes: []string{"githubassets.com"}, Outbound: "warp", Family: "auto"},
		}, DefaultOutbound: "direct",
		Adaptive: Adaptive{Candidates: []string{"direct", "warp"}, WindowSeconds: 600, MinSamples: 3, ImprovementPercent: 25, ImprovementMillis: 50, CooldownSeconds: 600, ProbesPerMinute: 12, ProbeConcurrency: 2, ProbeTimeoutSeconds: 4},
		PAC:      PAC{ProxyAddress: "127.0.0.1:17890", BypassDomains: defaultPACDomains(), BypassCIDRs: defaultPACCIDRs()},
	}
}

type rawConfigV1 struct {
	Version         int        `json:"version"`
	Revision        uint64     `json:"revision"`
	Listeners       Listeners  `json:"listeners"`
	Security        Security   `json:"security"`
	Outbounds       []Outbound `json:"outbounds"`
	Rules           []Rule     `json:"rules"`
	DefaultOutbound string     `json:"default_outbound"`
	Adaptive        Adaptive   `json:"adaptive"`
	PAC             PAC        `json:"pac"`
}

func Decode(data []byte) (Config, error) {
	if len(data) > 1<<20 {
		return Config{}, errors.New("configuration exceeds 1 MiB")
	}
	var probe struct {
		Version int `json:"version"`
	}
	_ = json.Unmarshal(data, &probe)
	if probe.Version == 1 {
		var v1 rawConfigV1
		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()
		if err := d.Decode(&v1); err != nil {
			return Config{}, err
		}
		if err := d.Decode(new(any)); err != io.EOF {
			return Config{}, errors.New("configuration must contain one JSON object")
		}
		c := Config{
			Version:         2,
			Revision:        v1.Revision,
			Listeners:       v1.Listeners,
			Security:        v1.Security,
			SourceAccess:    SourceAccess{Rules: []SourceAccessRule{}},
			Outbounds:       v1.Outbounds,
			Rules:           v1.Rules,
			DefaultOutbound: v1.DefaultOutbound,
			Adaptive:        v1.Adaptive,
			PAC:             v1.PAC,
		}
		if len(v1.Security.AllowedClients) > 0 {
			c.SourceAccess.Rules = []SourceAccessRule{
				{
					ID:      "local-clients",
					Name:    "Local clients",
					Action:  "allow",
					CIDRs:   append([]string(nil), v1.Security.AllowedClients...),
					Enabled: true,
				},
			}
		}
		return c, Validate(c)
	}

	var c Config
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return c, errors.New("configuration must contain one JSON object")
	}
	if c.SourceAccess.Rules == nil {
		c.SourceAccess.Rules = []SourceAccessRule{}
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

// ListenAddressesOverlap reports whether two host:port values bind or reach the
// same local TCP socket. Go binds "" and "::" dual-stack; names are treated as
// overlapping because they may resolve to either address.
func ListenAddressesOverlap(a, b string) bool {
	ah, ap, err := net.SplitHostPort(a)
	if err != nil {
		return false
	}
	bh, bp, err := net.SplitHostPort(b)
	if err != nil || ap != bp {
		return false
	}
	if ah == "" || bh == "" {
		return true
	}
	ai, ae := netip.ParseAddr(ah)
	bi, be := netip.ParseAddr(bh)
	if ae != nil || be != nil {
		return true
	}
	ai, bi = ai.Unmap(), bi.Unmap()
	if ai == bi || ai == netip.IPv6Unspecified() || bi == netip.IPv6Unspecified() {
		return true
	}
	if ai == netip.IPv4Unspecified() {
		return bi.Is4()
	}
	if bi == netip.IPv4Unspecified() {
		return ai.Is4()
	}
	return false
}

func Validate(c Config) error {
	_, err := ValidateAndCompileSourceAccess(c)
	return err
}

// ValidateAndCompileSourceAccess validates the full configuration and returns the
// immutable source policy for publication without compiling it a second time.
func ValidateAndCompileSourceAccess(c Config) (*access.Policy, error) {
	if err := validateNonSourceSettings(c); err != nil {
		return nil, err
	}
	return CompileSourceAccess(c.SourceAccess)
}

func validateNonSourceSettings(c Config) error {
	if c.Version != 2 {
		return fmt.Errorf("unsupported config version %d", c.Version)
	}
	if c.Revision == 0 {
		return errors.New("revision must be positive")
	}
	listeners := []string{c.Listeners.HTTP, c.Listeners.SOCKS5, c.Listeners.Admin, c.Listeners.PAC}
	for i, addr := range listeners {
		if addr == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return fmt.Errorf("listener %q: %w", addr, err)
		}
		for _, earlier := range listeners[:i] {
			if earlier != "" && ListenAddressesOverlap(earlier, addr) {
				return fmt.Errorf("listener %q overlaps listener %q", earlier, addr)
			}
		}
	}
	if c.Listeners.Admin == "" {
		return errors.New("admin listener is required")
	}
	if _, err := access.CompileAllowlist(c.Security.AllowedClients); err != nil {
		return fmt.Errorf("allowed_clients: %w", err)
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
	stateDirs := map[string]bool{}
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
			if o.Enabled {
				for _, addr := range listeners {
					if addr != "" && ListenAddressesOverlap(addr, o.ProxyAddress) {
						return errors.New("WARP proxy_address must not point to a Rillway listener")
					}
				}
			}
		case "wireguard":
			if o.ConfigFile == "" {
				return errors.New("WireGuard config_file is required")
			}
		case "tailscale":
			if o.StateDir == "" {
				return errors.New("tailscale state_dir is required")
			}
			if o.Enabled {
				key := filepath.Clean(o.StateDir)
				if stateDirs[key] {
					return errors.New("enabled Tailscale outbounds must use different state_dir values")
				}
				stateDirs[key] = true
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
	seenPAC := map[string]bool{}
	for _, entry := range c.PAC.BypassDomains {
		if !validDomain(entry.Value) {
			return fmt.Errorf("invalid bypass domain %q", entry.Value)
		}
		if err := validatePACBypass(entry, seenPAC, strings.ToLower(strings.Trim(entry.Value, "."))); err != nil {
			return err
		}
	}
	for _, entry := range c.PAC.BypassCIDRs {
		prefix, err := netip.ParsePrefix(entry.Value)
		if err != nil {
			return err
		}
		if err := validatePACBypass(entry, seenPAC, prefix.Masked().String()); err != nil {
			return err
		}
	}
	return nil
}

func CompileSourceAccess(sa SourceAccess) (*access.Policy, error) {
	if len(sa.Rules) > 256 {
		return nil, errors.New("source access exceeds maximum of 256 rules")
	}
	var totalCIDRs int
	seenIDs := make(map[string]bool, len(sa.Rules))
	specs := make([]access.RuleSpec, 0, len(sa.Rules))
	for _, r := range sa.Rules {
		if !validID.MatchString(r.ID) {
			return nil, fmt.Errorf("invalid source rule ID %q", r.ID)
		}
		if seenIDs[r.ID] {
			return nil, fmt.Errorf("duplicate source rule ID %q", r.ID)
		}
		seenIDs[r.ID] = true
		nameRunes := len([]rune(r.Name))
		if strings.TrimSpace(r.Name) == "" || nameRunes == 0 {
			return nil, fmt.Errorf("source rule %q: name must not be blank", r.ID)
		}
		if nameRunes > 256 {
			return nil, fmt.Errorf("source rule %q: name exceeds 256 characters", r.ID)
		}
		if r.Action != "allow" && r.Action != "deny" {
			return nil, fmt.Errorf("source rule %q: action must be allow or deny", r.ID)
		}
		if len(r.CIDRs) == 0 {
			return nil, fmt.Errorf("source rule %q: at least one CIDR is required", r.ID)
		}
		totalCIDRs += len(r.CIDRs)
		if totalCIDRs > 1024 {
			return nil, errors.New("source access exceeds maximum of 1,024 total CIDRs")
		}
		if len([]rune(r.Note)) > 1024 {
			return nil, fmt.Errorf("source rule %q: note exceeds 1024 characters", r.ID)
		}
		for _, cidr := range r.CIDRs {
			if _, err := access.ParsePrefix(cidr); err != nil {
				return nil, fmt.Errorf("source rule %q: CIDR %q: %w", r.ID, cidr, err)
			}
		}
		specs = append(specs, access.RuleSpec{
			ID:      r.ID,
			Action:  r.Action,
			CIDRs:   r.CIDRs,
			Enabled: r.Enabled,
		})
	}
	return access.Compile(specs)
}

func validatePACBypass(entry PACBypass, seen map[string]bool, key string) error {
	if seen[key] {
		return fmt.Errorf("duplicate PAC bypass %q", entry.Value)
	}
	seen[key] = true
	if len([]rune(entry.Note)) > 500 {
		return fmt.Errorf("PAC bypass note for %q exceeds 500 characters", entry.Value)
	}
	if entry.Preset != "" {
		if _, ok := pacPresets[entry.Preset]; !ok {
			return fmt.Errorf("unknown PAC bypass preset %q", entry.Preset)
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
