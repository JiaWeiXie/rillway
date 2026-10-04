package config

import (
	"fmt"
	"path/filepath"
)

// FormDefaults supplies editable suggestions, never credentials or host changes.
// Paths belong to the daemon, not to the machine displaying its UI.
type FormDefaults struct {
	Outbounds map[string]Outbound `json:"outbounds"`
	Rule      Rule                `json:"rule"`
}

func DefaultsForForms(c Config) FormDefaults {
	used := map[string]bool{}
	for _, o := range c.Outbounds {
		used[o.ID] = true
	}
	unique := func(base string) string {
		id := base
		for n := 2; used[id]; n++ {
			id = fmt.Sprintf("%s-%d", base, n)
		}
		return id
	}
	state := filepath.Dir(c.Security.AdminTokenFile)
	warp := Default(state).Outbounds[1]
	warp.ID, warp.Enabled = unique("warp"), true
	wgID, tailID := unique("wireguard"), unique("tailscale")
	for {
		occupied := false
		for _, o := range c.Outbounds {
			if o.StateDir != "" && filepath.Clean(o.StateDir) == filepath.Join(state, "tailscale", tailID) {
				occupied = true
				break
			}
		}
		if !occupied {
			break
		}
		used[tailID] = true
		tailID = unique("tailscale")
	}
	outs := map[string]Outbound{
		"warp":      warp,
		"direct":    {ID: unique("direct"), Type: "direct", Enabled: true, PublicInternet: true},
		"wireguard": {ID: wgID, Type: "wireguard", PublicInternet: true, ConfigFile: filepath.Join(state, "secrets", wgID+".conf")},
		"tailscale": {ID: tailID, Type: "tailscale", Hostname: "rillway-" + tailID, StateDir: filepath.Join(state, "tailscale", tailID)},
	}
	used = map[string]bool{}
	for _, r := range c.Rules {
		used[r.ID] = true
	}
	return FormDefaults{Outbounds: outs, Rule: Rule{ID: unique("rule"), Domains: []string{"github.com"}, Outbound: c.DefaultOutbound, Family: "auto"}}
}
