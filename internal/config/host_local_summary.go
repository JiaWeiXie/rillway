package config

import "rillway/internal/i18n"

// HostLocalSummary is offline policy guidance, not live enforcement evidence.
type HostLocalSummary struct {
	EffectiveMode      string   `json:"effective_mode"`
	Baseline           []string `json:"baseline"`
	ListenerProtection string   `json:"listener_protection"`
	RollbackField      string   `json:"rollback_field"`
	RollbackValue      string   `json:"rollback_value"`
	Notice             string   `json:"notice"`
	Warnings           []string `json:"warnings"`
}

func (c Config) HostLocalSummary(locale i18n.Locale) HostLocalSummary {
	notice := "Only loopback peers can reach host-local services without an exception."
	switch c.HostLocalMode() {
	case "open":
		notice = "Host-local services are allowed, subject to the protected baseline and listener restrictions."
	case "strict":
		notice = "Every client needs an exception to reach host-local services."
	}
	result := HostLocalSummary{
		EffectiveMode:      c.HostLocalMode(),
		Baseline:           []string{"169.254.0.0/16", "fe80::/10", "224.0.0.0/24", "ff02::/16", "fd00:ec2::254", "192.0.0.192", "100.100.100.200"},
		ListenerProtection: i18n.Message(locale, "Proxy, management, PAC and UDP relay listeners cannot be allowed by an exception."),
		RollbackField:      "host_local.mode", RollbackValue: "open",
		Notice: i18n.Message(locale, notice) + " " + i18n.Message(locale, "To return to open, set host_local.mode to open and apply the configuration; the baseline remains protected."),
	}
	for _, warning := range c.HostLocalWarnings() {
		result.Warnings = append(result.Warnings, i18n.Message(locale, warning))
	}
	return result
}
