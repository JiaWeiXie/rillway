package app

import (
	"context"
	"errors"
	"rillway/internal/config"
	"rillway/internal/resourcecontrol"
)

func (r *Runtime) MemoryStatus(ctx context.Context) (resourcecontrol.MemoryStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, err := resourcecontrol.MemoryControl(ctx, nil)
	if err == nil {
		s = r.memoryMinimum(s)
	}
	return s, memoryPublicError(err)
}

func (r *Runtime) ApplyMemory(ctx context.Context, req resourcecontrol.MemoryRequest) (resourcecontrol.MemoryStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	before, err := resourcecontrol.MemoryControl(ctx, nil)
	if err != nil {
		return before, memoryPublicError(err)
	}
	before = r.memoryMinimum(before)
	if !before.Supported {
		return before, config.PublicError{Message: before.Reason}
	}
	if _, err = resourcecontrol.CalculateMemory(req, before); err != nil {
		return before, memoryPublicError(err)
	}
	s, err := resourcecontrol.MemoryControl(ctx, &req)
	if err == nil {
		s = r.memoryMinimum(s)
	}
	return s, memoryPublicError(err)
}

// Keep the active configuration floor, including Tailscale nodes retired while
// existing connections still own their state, until those connections close.
func (r *Runtime) memoryMinimum(s resourcecontrol.MemoryStatus) resourcecontrol.MemoryStatus {
	if !s.Supported {
		return s
	}
	floor, reason := resourcecontrol.MemorySafeMinimum(r.cfg, s.CurrentBytes)
	for _, owner := range r.tailscaleOwners {
		if !owner.isClosed() && floor < resourcecontrol.GiB {
			floor, reason = resourcecontrol.GiB, "Embedded VPN safety floor"
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
	if errors.Is(err, resourcecontrol.ErrMemoryConflict) {
		return config.PublicError{Message: resourcecontrol.ErrMemoryConflict.Error(), Err: err}
	}
	return config.PublicError{Message: err.Error(), Err: err}
}
