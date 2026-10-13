package engine

import (
	"rillway/internal/config"
	"testing"
)

func TestExplainBlocksDisabledFixedOutbound(t *testing.T) {
	e, _ := testEngine()
	c := cloneConfig(e.cfg)
	c.Rules = []config.Rule{{ID: "fixed", Domains: []string{"example.com"}, Outbound: "warp"}}
	for i := range c.Outbounds {
		if c.Outbounds[i].ID == "warp" {
			c.Outbounds[i].Enabled = false
		}
	}
	e.Update(c, e.providers)
	got, err := e.Explain(config.ExplainRequest{Host: "example.com", Port: "443"})
	if err != nil || !got.Blocked || got.Reason != "target_unavailable" || got.TargetID != "warp" {
		t.Fatalf("disabled fixed outbound = %+v, %v", got, err)
	}
}
