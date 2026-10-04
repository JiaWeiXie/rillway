package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"rillway/internal/config"
	"strings"
	"time"
)

type ProxySnapshot struct {
	Service      string `json:"service"`
	PACURL       string `json:"pac_url"`
	PACEnabled   bool   `json:"pac_enabled"`
	HTTPEnabled  bool   `json:"http_enabled"`
	HTTPSEnabled bool   `json:"https_enabled"`
	SOCKSEnabled bool   `json:"socks_enabled"`
}

func field(s, key string) string {
	for _, l := range strings.Split(s, "\n") {
		if v, ok := strings.CutPrefix(l, key+": "); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func CaptureProxy(ctx context.Context, run Runner, service string) (ProxySnapshot, error) {
	s := ProxySnapshot{Service: service}
	if service == "" || strings.ContainsAny(service, "\x00\n\r") {
		return s, errors.New("invalid network service")
	}
	b, err := run(ctx, "/usr/sbin/networksetup", "-getautoproxyurl", service)
	if err != nil {
		return s, err
	}
	s.PACURL = field(string(b), "URL")
	s.PACEnabled = field(string(b), "Enabled") == "Yes"
	for _, entry := range []struct {
		cmd string
		dst *bool
	}{{"-getwebproxy", &s.HTTPEnabled}, {"-getsecurewebproxy", &s.HTTPSEnabled}, {"-getsocksfirewallproxy", &s.SOCKSEnabled}} {
		b, err = run(ctx, "/usr/sbin/networksetup", entry.cmd, service)
		if err != nil {
			return s, err
		}
		*entry.dst = field(string(b), "Enabled") == "Yes"
	}
	return s, nil
}

func ApplyPAC(ctx context.Context, run Runner, service, pacURL, backup string) error {
	u, err := url.Parse(pacURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return errors.New("PAC URL must be an HTTP(S) URL without credentials")
	}
	if _, err = os.Stat(backup); err == nil {
		return errors.New("backup already exists; restore it before applying another PAC")
	}
	snapshot, err := CaptureProxy(ctx, run, service)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	if err = config.WritePrivate(backup, data); err != nil {
		return err
	}
	cmds := [][]string{{"-setautoproxyurl", service, pacURL}, {"-setwebproxystate", service, "off"}, {"-setsecurewebproxystate", service, "off"}, {"-setsocksfirewallproxystate", service, "off"}, {"-setautoproxystate", service, "on"}}
	for _, args := range cmds {
		if _, err = run(ctx, "/usr/sbin/networksetup", args...); err != nil {
			rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			rollbackErr := RestoreProxy(rollbackCtx, run, backup)
			cancel()
			return fmt.Errorf("apply PAC: %w; rollback: %v", err, rollbackErr)
		}
	}
	return nil
}

func RestoreProxy(ctx context.Context, run Runner, backup string) error {
	data, err := os.ReadFile(backup)
	if err != nil {
		return err
	}
	var s ProxySnapshot
	if err = json.Unmarshal(data, &s); err != nil {
		return err
	}
	if s.Service == "" {
		return errors.New("invalid proxy backup")
	}
	state := func(on bool) string {
		if on {
			return "on"
		}
		return "off"
	}
	var cmds [][]string
	if s.PACURL != "" && s.PACURL != "(null)" {
		cmds = append(cmds, []string{"-setautoproxyurl", s.Service, s.PACURL})
	}
	cmds = append(cmds, []string{"-setautoproxystate", s.Service, state(s.PACEnabled)}, []string{"-setwebproxystate", s.Service, state(s.HTTPEnabled)}, []string{"-setsecurewebproxystate", s.Service, state(s.HTTPSEnabled)}, []string{"-setsocksfirewallproxystate", s.Service, state(s.SOCKSEnabled)})
	for _, args := range cmds {
		if _, err = run(ctx, "/usr/sbin/networksetup", args...); err != nil {
			return err
		}
	}
	return os.Remove(backup)
}
