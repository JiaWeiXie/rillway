package config

// Config is the versioned, portable configuration. Secrets are file references.
type Config struct {
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

type Listeners struct {
	HTTP   string `json:"http"`
	SOCKS5 string `json:"socks5"`
	Admin  string `json:"admin"`
	PAC    string `json:"pac"`
}

type Security struct {
	AllowedClients    []string `json:"allowed_clients"`
	AdminTokenFile    string   `json:"admin_token_file"`
	TLSCertFile       string   `json:"tls_cert_file"`
	TLSKeyFile        string   `json:"tls_key_file"`
	ProxyUsername     string   `json:"proxy_username,omitempty"`
	ProxyPasswordFile string   `json:"proxy_password_file,omitempty"`
}

type Outbound struct {
	ID             string   `json:"id"`
	Type           string   `json:"type"`
	Enabled        bool     `json:"enabled"`
	PublicInternet bool     `json:"public_internet"`
	ProxyAddress   string   `json:"proxy_address,omitempty"`
	StateDir       string   `json:"state_dir,omitempty"`
	ConfigFile     string   `json:"config_file,omitempty"`
	Hostname       string   `json:"hostname,omitempty"`
	AuthKeyFile    string   `json:"auth_key_file,omitempty"`
	DNS            []string `json:"dns,omitempty"`
	WARPBinary     string   `json:"warp_binary,omitempty"`
}

type Rule struct {
	ID         string   `json:"id"`
	Domains    []string `json:"domains,omitempty"`
	Suffixes   []string `json:"suffixes,omitempty"`
	CIDRs      []string `json:"cidrs,omitempty"`
	Outbound   string   `json:"outbound,omitempty"`
	Adaptive   bool     `json:"adaptive,omitempty"`
	Candidates []string `json:"candidates,omitempty"`
	Family     string   `json:"family,omitempty"`
}

type Adaptive struct {
	Enabled             bool     `json:"enabled"`
	Candidates          []string `json:"candidates"`
	WindowSeconds       int      `json:"window_seconds"`
	MinSamples          int      `json:"min_samples"`
	ImprovementPercent  int      `json:"improvement_percent"`
	ImprovementMillis   int      `json:"improvement_millis"`
	CooldownSeconds     int      `json:"cooldown_seconds"`
	ProbesPerMinute     int      `json:"probes_per_minute"`
	ProbeConcurrency    int      `json:"probe_concurrency"`
	ProbeTimeoutSeconds int      `json:"probe_timeout_seconds"`
}

type PAC struct {
	ProxyAddress  string      `json:"proxy_address"`
	BypassDomains []PACBypass `json:"bypass_domains"`
	BypassCIDRs   []PACBypass `json:"bypass_cidrs"`
}

// PACBypass is one client-side direct-connection exception. Preset identifies
// Rillway-owned explanatory text; Note is user data and is never translated.
type PACBypass struct {
	Value   string `json:"value"`
	Enabled bool   `json:"enabled"`
	Note    string `json:"note,omitempty"`
	Preset  string `json:"preset,omitempty"`
}
