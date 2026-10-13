package config

// ExplainRequest describes a destination without establishing a connection.
type ExplainRequest struct {
	Host    string `json:"host"`
	Port    string `json:"port"`
	Source  string `json:"source,omitempty"`
	Inbound string `json:"inbound,omitempty"`
	Family  string `json:"family,omitempty"`
}

// ExplainResult reports the configured route and its initial attempt order.
// It does not verify connectivity or outbound availability.
type ExplainResult struct {
	RuleID     string   `json:"rule_id"`
	TargetKind string   `json:"target_kind"`
	TargetID   string   `json:"target_id"`
	ResolvedID string   `json:"resolved_id,omitempty"`
	Order      []string `json:"order,omitempty"`
	Private    bool     `json:"private"`
	Blocked    bool     `json:"blocked"`
	Family     string   `json:"family"`
	Reason     string   `json:"reason"`
}
