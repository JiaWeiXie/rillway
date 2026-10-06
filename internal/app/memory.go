package app

import (
	"context"
	"errors"
	"rillway/internal/config"
	"rillway/internal/memorylimit"
)

func (r *Runtime) MemoryStatus(ctx context.Context) (memorylimit.Status, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, err := memorylimit.Control(ctx, nil)
	if err == nil {
		s = r.memoryMinimum(s)
	}
	return s, memoryPublicError(err)
}

func (r *Runtime) ApplyMemory(ctx context.Context, req memorylimit.Request) (memorylimit.Status, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	before, err := memorylimit.Control(ctx, nil)
	if err != nil {
		return before, memoryPublicError(err)
	}
	before = r.memoryMinimum(before)
	if !before.Supported {
		return before, config.PublicError{Message: before.Reason}
	}
	if _, err = memorylimit.Calculate(req, before); err != nil {
		return before, memoryPublicError(err)
	}
	s, err := memorylimit.Control(ctx, &req)
	if err == nil {
		s = r.memoryMinimum(s)
	}
	return s, memoryPublicError(err)
}

// Keep the active configuration floor, including Tailscale nodes retired while
// existing connections still own their state, until those connections close.
func (r *Runtime) memoryMinimum(s memorylimit.Status) memorylimit.Status {
	if !s.Supported {
		return s
	}
	floor, reason := memorylimit.SafeMinimum(r.cfg, s.CurrentBytes)
	for _, owner := range r.tailscaleOwners {
		if !owner.isClosed() && floor < memorylimit.GiB {
			floor, reason = memorylimit.GiB, "Embedded VPN safety floor"
		}
	}
	if floor > s.MinimumBytes {
		s.MinimumBytes, s.MinimumReason = floor, reason
	}
	if s.MinimumBytes > s.MaximumBytes {
		s.Supported, s.Reason = false, "Host capacity is below the service safety minimum."
	}
	return s
}

func memoryPublicError(err error) error {
	if err == nil {
		return nil
	}
	// Control errors contain only our fixed protocol and policy messages, never
	// command output, configuration contents or credentials.
	if errors.Is(err, memorylimit.ErrConflict) {
		return config.PublicError{Message: memorylimit.ErrConflict.Error(), Err: err}
	}
	return config.PublicError{Message: err.Error(), Err: err}
}
