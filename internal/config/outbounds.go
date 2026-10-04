package config

import "slices"

const (
	DirectRequired      = "The built-in direct outbound must remain enabled with type direct and Internet access."
	DirectProtected     = "The built-in direct outbound cannot be deleted."
	OutboundMissing     = "Outbound not found."
	ReplacementRequired = "Choose an enabled replacement for the outbound's rules, default route and adaptive candidates."
	ReplacementPublic   = "The replacement must have Internet access and cannot be Tailscale when replacing adaptive candidates."
)

// RemoveOutbound preserves rules and requires an explicit replacement for
// references. It never turns removal of a fixed VPN route into implicit direct.
// The caller owns revision checking, persistence and runtime publication.
func RemoveOutbound(c Config, id, replacement string) (Config, error) {
	if id == "direct" {
		return Config{}, PublicError{Message: DirectProtected}
	}
	index := -1
	var substitute Outbound
	for i, o := range c.Outbounds {
		if o.ID == id {
			index = i
		}
		if o.ID == replacement {
			substitute = o
		}
	}
	if index < 0 {
		return Config{}, PublicError{Message: OutboundMissing}
	}
	adaptive := slices.Contains(c.Adaptive.Candidates, id)
	referenced := c.DefaultOutbound == id || adaptive
	for _, rule := range c.Rules {
		referenced = referenced || rule.Outbound == id || slices.Contains(rule.Candidates, id)
		adaptive = adaptive || slices.Contains(rule.Candidates, id)
	}
	if referenced || replacement != "" {
		if substitute.ID == "" || substitute.ID == id || !substitute.Enabled {
			return Config{}, PublicError{Message: ReplacementRequired}
		}
		if adaptive && (!substitute.PublicInternet || substitute.Type == "tailscale") {
			return Config{}, PublicError{Message: ReplacementPublic}
		}
	}
	replaceCandidates := func(ids []string) []string {
		if !slices.Contains(ids, id) {
			return slices.Clone(ids)
		}
		result := make([]string, 0, len(ids))
		for _, candidate := range ids {
			if candidate == id {
				candidate = replacement
			}
			if !slices.Contains(result, candidate) {
				result = append(result, candidate)
			}
		}
		return result
	}
	c.Outbounds = slices.Delete(slices.Clone(c.Outbounds), index, index+1)
	c.Rules = slices.Clone(c.Rules)
	for i := range c.Rules {
		if c.Rules[i].Outbound == id {
			c.Rules[i].Outbound = replacement
		}
		c.Rules[i].Candidates = replaceCandidates(c.Rules[i].Candidates)
	}
	if c.DefaultOutbound == id {
		c.DefaultOutbound = replacement
	}
	c.Adaptive.Candidates = replaceCandidates(c.Adaptive.Candidates)
	if err := Validate(c); err != nil {
		return Config{}, PublicError{Message: "Configuration validation failed: " + err.Error(), Err: err}
	}
	return c, nil
}
