// Package dockerproxy generates Docker's separate daemon and container proxy settings.
package dockerproxy

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"rillway/internal/config"
	"strconv"
	"strings"
)

var hostname = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9.-]*[a-zA-Z0-9])?\.?$`)

const (
	InvalidURL    = "Docker proxy URL must be http://host:port without credentials, a path, query or fragment."
	InvalidBypass = "Docker bypass entries must be comma-separated domains, IPs or CIDRs without spaces or control characters."
	InvalidInput  = "Existing Docker settings must be a JSON object no larger than 1 MiB."
	InvalidTarget = "Docker export target must be daemon, client, env or compose."
	HTTPRequired  = "Enable the HTTP Proxy listener before exporting Docker settings."
	MergeJSONOnly = "Existing Docker settings can only be merged for daemon or client exports."
)

type Settings struct {
	ProxyURL string `json:"proxy_url"`
	NoProxy  string `json:"no_proxy"`
	Loopback bool   `json:"loopback"`
}

type Bundle struct {
	Settings
	AuthRequired bool              `json:"auth_required"`
	Exports      map[string]string `json:"exports"`
}

func New(proxyURL, noProxy string) (Settings, error) {
	u, err := url.Parse(proxyURL)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(proxyURL, "#") {
		return Settings{}, config.PublicError{Message: InvalidURL}
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return Settings{}, config.PublicError{Message: InvalidURL}
	}
	host := u.Hostname()
	ip, ipErr := netip.ParseAddr(host)
	if ipErr == nil {
		if ip.Unmap().IsUnspecified() || ip.Zone() != "" {
			return Settings{}, config.PublicError{Message: InvalidURL}
		}
	} else if len(host) > 253 || !hostname.MatchString(host) || strings.Contains(host, "..") {
		return Settings{}, config.PublicError{Message: InvalidURL}
	}
	if len(noProxy) > 8192 || strings.ContainsAny(noProxy, "\r\n\x00") {
		return Settings{}, config.PublicError{Message: InvalidBypass}
	}
	var entries []string
	seen := map[string]bool{}
	for _, entry := range strings.Split(noProxy, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		for _, c := range entry {
			if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_.*:-/[]", c) {
				return Settings{}, config.PublicError{Message: InvalidBypass}
			}
		}
		if strings.Contains(entry, "/") {
			if _, err := netip.ParsePrefix(entry); err != nil {
				return Settings{}, config.PublicError{Message: InvalidBypass}
			}
		}
		if !seen[entry] {
			entries = append(entries, entry)
			seen[entry] = true
		}
	}
	return Settings{ProxyURL: proxyURL, NoProxy: strings.Join(entries, ","), Loopback: ipErr == nil && ip.Unmap().IsLoopback() || strings.EqualFold(strings.TrimSuffix(host, "."), "localhost")}, nil
}

func DefaultBypass(pac config.PAC) string {
	entries := []string{"localhost"}
	entries = append(entries, pac.EnabledDomains()...)
	entries = append(entries, pac.EnabledCIDRs()...)
	return strings.Join(entries, ",")
}

func FromConfig(c config.Config) (Settings, error) {
	if c.Listeners.HTTP == "" {
		return Settings{}, config.PublicError{Message: HTTPRequired}
	}
	return New("http://"+c.PAC.ProxyAddress, DefaultBypass(c.PAC))
}

func object(data []byte) (map[string]json.RawMessage, error) {
	if len(data) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	var result map[string]json.RawMessage
	if len(data) > 1<<20 || json.Unmarshal(data, &result) != nil || result == nil {
		return nil, config.PublicError{Message: InvalidInput}
	}
	return result, nil
}

func (s Settings) Export(target string, existing []byte) ([]byte, error) {
	validated, err := New(s.ProxyURL, s.NoProxy)
	if err != nil {
		return nil, err
	}
	s = validated
	if target == "env" || target == "compose" {
		if len(existing) != 0 {
			return nil, config.PublicError{Message: MergeJSONOnly}
		}
		values := [][2]string{{"HTTP_PROXY", s.ProxyURL}, {"HTTPS_PROXY", s.ProxyURL}, {"NO_PROXY", s.NoProxy}, {"http_proxy", s.ProxyURL}, {"https_proxy", s.ProxyURL}, {"no_proxy", s.NoProxy}}
		var out strings.Builder
		if target == "compose" {
			out.WriteString("services:\n  app:\n    image: your-image:tag\n    environment:\n")
		}
		for _, item := range values {
			if target == "env" {
				fmt.Fprintf(&out, "%s=%s\n", item[0], item[1])
			} else {
				fmt.Fprintf(&out, "      %s: %s\n", item[0], strconv.Quote(item[1]))
			}
		}
		return []byte(out.String()), nil
	}
	if target != "daemon" && target != "client" {
		return nil, config.PublicError{Message: InvalidTarget}
	}
	root, err := object(existing)
	if err != nil {
		return nil, err
	}
	proxies, err := object(root["proxies"])
	if err != nil {
		return nil, err
	}
	if target == "daemon" {
		for key, value := range map[string]string{"http-proxy": s.ProxyURL, "https-proxy": s.ProxyURL, "no-proxy": s.NoProxy} {
			proxies[key], _ = json.Marshal(value)
		}
	} else {
		defaults, err := object(proxies["default"])
		if err != nil {
			return nil, err
		}
		for key, value := range map[string]string{"httpProxy": s.ProxyURL, "httpsProxy": s.ProxyURL, "noProxy": s.NoProxy} {
			defaults[key], _ = json.Marshal(value)
		}
		proxies["default"], _ = json.Marshal(defaults)
	}
	root["proxies"], _ = json.Marshal(proxies)
	data, err := json.MarshalIndent(root, "", "  ")
	return append(data, '\n'), err
}

func (s Settings) Bundle(authRequired bool) (Bundle, error) {
	b := Bundle{Settings: s, AuthRequired: authRequired, Exports: map[string]string{}}
	for _, target := range []string{"daemon", "client", "env", "compose"} {
		data, err := s.Export(target, nil)
		if err != nil {
			return Bundle{}, err
		}
		b.Exports[target] = string(data)
	}
	return b, nil
}
