// Package memorylimit validates service memory budgets independently of the UI.
package memorylimit

import (
	"math/big"
	"regexp"
	"rillway/internal/config"
)

const (
	MiB            = uint64(1 << 20)
	GiB            = uint64(1 << 30)
	SocketPath     = "/run/rillway-memory.sock"
	StateDirectory = "/var/lib/rillway-resource-control"
)

// These are complete user-facing messages rather than diagnostic error strings.
type messageError string

func (e messageError) Error() string { return string(e) }

type Request struct {
	Mode     string `json:"mode"`
	Value    string `json:"value"`
	Revision string `json:"revision"`
}

type Status struct {
	Supported     bool   `json:"supported"`
	Reason        string `json:"reason,omitempty"`
	HostBytes     uint64 `json:"host_bytes"`
	MaximumBytes  uint64 `json:"maximum_bytes"`
	MinimumBytes  uint64 `json:"minimum_bytes"`
	CurrentBytes  uint64 `json:"current_bytes"`
	LimitBytes    uint64 `json:"limit_bytes"`
	HighBytes     uint64 `json:"high_bytes"`
	GoLimitBytes  uint64 `json:"go_limit_bytes"`
	Mode          string `json:"mode"`
	Value         string `json:"value"`
	Revision      string `json:"revision"`
	MinimumReason string `json:"minimum_reason"`
}

// SafeMinimum is a conservative policy floor, not a benchmark of the exact
// minimum RSS. Embedded VPNs need more headroom than direct/official WARP.
func SafeMinimum(cfg config.Config, current uint64) (uint64, string) {
	floor, reason := 256*MiB, "Direct and WARP safety floor"
	for _, o := range cfg.Outbounds {
		if o.Enabled && (o.Type == "tailscale" || o.Type == "wireguard") {
			floor, reason = GiB, "Embedded VPN safety floor"
		}
	}
	// A reduction must leave 25% headroom over the current cgroup usage.
	usageFloor := current + current/4
	if usageFloor > floor {
		floor, reason = usageFloor, "Current service usage plus headroom"
	}
	return ((floor + MiB - 1) / MiB) * MiB, reason
}

// Maximum leaves ten percent of detected host/ancestor capacity for other
// services. The service's own existing limit is deliberately not the host cap.
func Maximum(host uint64) uint64 { return ((host - host/10) / MiB) * MiB }

var decimal = regexp.MustCompile(`^(0|[1-9][0-9]{0,7})(\.[0-9]{1,3})?$`)

func Calculate(req Request, status Status) (uint64, error) {
	if !decimal.MatchString(req.Value) {
		return 0, messageError("Enter a positive memory value with at most three decimal places.")
	}
	value, ok := new(big.Rat).SetString(req.Value)
	if !ok || value.Sign() <= 0 {
		return 0, messageError("Enter a positive memory value with at most three decimal places.")
	}
	switch req.Mode {
	case "percent":
		if value.Cmp(big.NewRat(90, 1)) > 0 {
			return 0, messageError("Memory percentage must be greater than zero and at most 90.")
		}
		value.Mul(value, new(big.Rat).SetFrac(new(big.Int).SetUint64(status.HostBytes), big.NewInt(100)))
	case "MiB":
		value.Mul(value, new(big.Rat).SetInt(new(big.Int).SetUint64(MiB)))
	case "GiB":
		value.Mul(value, new(big.Rat).SetInt(new(big.Int).SetUint64(GiB)))
	default:
		return 0, messageError("Choose percent, MiB or GiB for the memory limit.")
	}
	bytes := new(big.Int).Quo(value.Num(), value.Denom())
	if !bytes.IsUint64() {
		return 0, messageError("Memory limit exceeds the detected host capacity.")
	}
	limit := bytes.Uint64()
	if req.Mode == "percent" {
		limit = limit / MiB * MiB
	}
	if limit < status.MinimumBytes {
		return 0, messageError("Memory limit is below the safe minimum. Refresh limits and choose a larger value.")
	}
	if limit > status.MaximumBytes {
		return 0, messageError("Memory limit exceeds the detected host capacity.")
	}
	return limit, nil
}

func GoBudget(limit uint64) uint64 {
	high := limit * 3 / 4
	if high > 64*MiB {
		return high - 64*MiB
	}
	return high
}
