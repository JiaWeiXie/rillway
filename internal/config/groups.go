package config

import (
	"fmt"
	"net/netip"
	"slices"
	"unicode/utf8"
)

const (
	GroupMissing             = "Outbound group not found."
	GroupReplacementRequired = "Choose an enabled replacement for the group's rules and default route."
	DirectMember             = "Direct outbounds cannot be group members."
)

func validateOutboundGroups(list []OutboundGroup, outs map[string]Outbound, rules []Rule, defaultOutbound string) (map[string]OutboundGroup, error) {
	groups := make(map[string]OutboundGroup, len(list))
	for _, g := range list {
		if !validID.MatchString(g.ID) {
			return nil, fmt.Errorf("invalid outbound group ID %q", g.ID)
		}
		if _, ok := groups[g.ID]; ok {
			return nil, fmt.Errorf("duplicate outbound group %q", g.ID)
		}
		if _, ok := outs[g.ID]; ok {
			return nil, fmt.Errorf("outbound group ID %q collides with outbound", g.ID)
		}
		switch g.Type {
		case "select", "fallback", "load-balance":
		default:
			return nil, fmt.Errorf("unknown outbound group type %q", g.Type)
		}
		if len(g.Members) == 0 {
			return nil, fmt.Errorf("outbound group %q members must not be empty", g.ID)
		}
		seenMembers := make(map[string]bool, len(g.Members))
		for _, m := range g.Members {
			if seenMembers[m] {
				return nil, fmt.Errorf("duplicate member %q in group %q", m, g.ID)
			}
			seenMembers[m] = true
			o, ok := outs[m]
			if !ok {
				return nil, fmt.Errorf("group %q refers to unknown outbound %q", g.ID, m)
			}
			if !o.Enabled {
				return nil, fmt.Errorf("group %q refers to disabled outbound %q", g.ID, m)
			}
			if o.Type == "direct" {
				return nil, PublicError{Message: DirectMember}
			}
		}
		switch g.Type {
		case "select":
			if g.Selected == "" || !seenMembers[g.Selected] {
				return nil, fmt.Errorf("selected member %q of group %q must be in members", g.Selected, g.ID)
			}
			if g.Strategy != "" {
				return nil, fmt.Errorf("select group %q cannot configure strategy", g.ID)
			}
			if g.Key != "" {
				return nil, fmt.Errorf("select group %q cannot configure key", g.ID)
			}
		case "load-balance":
			if g.Strategy != "consistent-hashing" && g.Strategy != "round-robin" {
				return nil, fmt.Errorf("invalid strategy %q for load-balance group %q", g.Strategy, g.ID)
			}
			if g.Strategy == "consistent-hashing" {
				if g.Key != "destination" && g.Key != "source" {
					return nil, fmt.Errorf("invalid key %q for consistent-hashing group %q", g.Key, g.ID)
				}
			} else {
				if g.Key != "" {
					return nil, fmt.Errorf("round-robin group %q cannot configure key", g.ID)
				}
			}
			if g.Selected != "" {
				return nil, fmt.Errorf("load-balance group %q cannot configure selected", g.ID)
			}
		case "fallback":
			if g.Strategy != "" {
				return nil, fmt.Errorf("fallback group %q cannot configure strategy", g.ID)
			}
			if g.Key != "" {
				return nil, fmt.Errorf("fallback group %q cannot configure key", g.ID)
			}
			if g.Selected != "" {
				return nil, fmt.Errorf("fallback group %q cannot configure selected", g.ID)
			}
		}
		groups[g.ID] = g
	}
	return groups, nil
}

func validateRuleSets(list []RuleSet) (map[string]RuleSet, error) {
	sets := make(map[string]RuleSet, len(list))
	for _, s := range list {
		if !validID.MatchString(s.ID) {
			return nil, fmt.Errorf("invalid rule set ID %q", s.ID)
		}
		if _, ok := sets[s.ID]; ok {
			return nil, fmt.Errorf("duplicate rule set %q", s.ID)
		}
		nameRunes := utf8.RuneCountInString(s.Name)
		if nameRunes == 0 {
			return nil, fmt.Errorf("rule set %q: name is required", s.ID)
		}
		if nameRunes > 256 {
			return nil, fmt.Errorf("rule set %q: name exceeds 256 characters", s.ID)
		}
		if len(s.Domains)+len(s.Suffixes)+len(s.CIDRs)+len(s.Sources) == 0 {
			return nil, fmt.Errorf("rule set %q has no match", s.ID)
		}
		for _, domains := range [][]string{s.Domains, s.Suffixes} {
			for _, d := range domains {
				if !validDomain(d) {
					return nil, fmt.Errorf("invalid rule set domain %q", d)
				}
			}
		}
		for _, p := range s.CIDRs {
			if _, err := netip.ParsePrefix(p); err != nil {
				return nil, fmt.Errorf("rule set CIDR: %w", err)
			}
		}
		for _, p := range s.Sources {
			if _, err := netip.ParsePrefix(p); err != nil {
				return nil, fmt.Errorf("rule set source: %w", err)
			}
		}
		sets[s.ID] = s
	}
	return sets, nil
}

// RemoveGroup removes an outbound group and requires an explicit replacement
// if referenced by default_outbound or rules.
func RemoveGroup(c Config, id, replacement string) (Config, error) {
	index := -1
	for i, g := range c.OutboundGroups {
		if g.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return Config{}, PublicError{Message: GroupMissing}
	}
	referenced := c.DefaultOutbound == id
	for _, rule := range c.Rules {
		if rule.Outbound == id {
			referenced = true
			break
		}
	}
	if referenced || replacement != "" {
		if replacement == "" || replacement == id {
			return Config{}, PublicError{Message: GroupReplacementRequired}
		}
		validSub := false
		for _, o := range c.Outbounds {
			if o.ID == replacement && o.Enabled {
				validSub = true
				break
			}
		}
		if !validSub {
			for i, g := range c.OutboundGroups {
				if i != index && g.ID == replacement && g.Enabled {
					validSub = true
					break
				}
			}
		}
		if !validSub {
			return Config{}, PublicError{Message: GroupReplacementRequired}
		}
	}
	c.OutboundGroups = slices.Delete(slices.Clone(c.OutboundGroups), index, index+1)
	c.Rules = slices.Clone(c.Rules)
	for i := range c.Rules {
		if c.Rules[i].Outbound == id {
			c.Rules[i].Outbound = replacement
		}
	}
	if c.DefaultOutbound == id {
		c.DefaultOutbound = replacement
	}
	if err := Validate(c); err != nil {
		return Config{}, PublicError{Message: "Configuration validation failed: " + err.Error(), Err: err}
	}
	return c, nil
}
