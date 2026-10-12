package app

import (
	"context"
	"errors"
	"rillway/internal/config"
	"rillway/internal/resourcecontrol"
)

func (r *Runtime) WarpLogStatus(ctx context.Context) (resourcecontrol.WarpLogStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, err := resourcecontrol.WarpLogControl(ctx, nil)
	return s, warpLogPublicError(err)
}

func (r *Runtime) ApplyWarpLog(ctx context.Context, req resourcecontrol.WarpLogRequest) (resourcecontrol.WarpLogStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, err := resourcecontrol.WarpLogControl(ctx, &req)
	return s, warpLogPublicError(err)
}

func warpLogPublicError(err error) error {
	if err == nil {
		return nil
	}
	// Control errors contain only our fixed protocol and policy messages, never
	// command output, configuration contents or credentials.
	if errors.Is(err, resourcecontrol.ErrWarpLogConflict) {
		return config.PublicError{Message: resourcecontrol.ErrWarpLogConflict.Error(), Err: err}
	}
	return config.PublicError{Message: err.Error(), Err: err}
}
