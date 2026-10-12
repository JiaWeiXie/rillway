package control

import (
	"context"
	"errors"
	"net/http"
	"rillway/internal/resourcecontrol"
)

type warpLogBackend interface {
	WarpLogStatus(context.Context) (resourcecontrol.WarpLogStatus, error)
	ApplyWarpLog(context.Context, resourcecontrol.WarpLogRequest) (resourcecontrol.WarpLogStatus, error)
}

func (h *handler) getWarpLog(w http.ResponseWriter, r *http.Request) {
	b, ok := h.backend.(warpLogBackend)
	if !ok {
		writeJSON(w, http.StatusOK, resourcecontrol.WarpLogStatus{
			Supported: false,
			Reason:    "WARP logging control is unavailable. Upgrade the installed service to enable it.",
		})
		return
	}
	s, err := b.WarpLogStatus(r.Context())
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, publicMessage(err, "Could not read WARP logging settings."))
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (h *handler) putWarpLog(w http.ResponseWriter, r *http.Request) {
	var req resourcecontrol.WarpLogRequest
	if !decodeBody(w, r, &req, 2048) {
		return
	}
	switch req.Level {
	case resourcecontrol.WarpLogLevelInherit,
		resourcecontrol.WarpLogLevelWarning,
		resourcecontrol.WarpLogLevelInfo,
		resourcecontrol.WarpLogLevelDebug:
	default:
		writeError(w, r, http.StatusUnprocessableEntity, "Invalid WARP log level. Allowed values: inherit, warning, info, debug.")
		return
	}
	b, ok := h.backend.(warpLogBackend)
	if !ok {
		writeError(w, r, http.StatusNotImplemented, "WARP logging control is unavailable. Upgrade the installed service to enable it.")
		return
	}
	h.applyMu.Lock()
	defer h.applyMu.Unlock()
	s, err := b.ApplyWarpLog(r.Context(), req)
	if !s.Supported {
		reason := s.Reason
		if reason == "" {
			reason = "WARP logging control requires a Linux systemd installation."
		}
		writeError(w, r, http.StatusNotImplemented, reason)
		return
	}
	if err != nil {
		status := http.StatusUnprocessableEntity
		if errors.Is(err, resourcecontrol.ErrWarpLogConflict) {
			status = http.StatusConflict
		}
		writeError(w, r, status, publicMessage(err, "Could not apply WARP logging settings. Refresh settings before retrying."))
		return
	}
	writeJSON(w, http.StatusOK, s)
}
