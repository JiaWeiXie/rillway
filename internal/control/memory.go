package control

import (
	"context"
	"errors"
	"net/http"
	"rillway/internal/memorylimit"
)

type memoryBackend interface {
	MemoryStatus(context.Context) (memorylimit.Status, error)
	ApplyMemory(context.Context, memorylimit.Request) (memorylimit.Status, error)
}

func (h *handler) getMemory(w http.ResponseWriter, r *http.Request) {
	b, ok := h.backend.(memoryBackend)
	if !ok {
		writeJSON(w, 200, memorylimit.Status{Reason: "Memory control is unavailable. Upgrade the installed service to enable it."})
		return
	}
	s, err := b.MemoryStatus(r.Context())
	if err != nil {
		writeError(w, r, 503, publicMessage(err, "Could not read service memory limits."))
		return
	}
	writeJSON(w, 200, s)
}

func (h *handler) putMemory(w http.ResponseWriter, r *http.Request) {
	var req memorylimit.Request
	if !decodeBody(w, r, &req, 2048) {
		return
	}
	b, ok := h.backend.(memoryBackend)
	if !ok {
		writeError(w, r, 501, "Memory control is unavailable. Upgrade the installed service to enable it.")
		return
	}
	h.applyMu.Lock()
	defer h.applyMu.Unlock()
	s, err := b.ApplyMemory(r.Context(), req)
	if err != nil {
		status := http.StatusUnprocessableEntity
		if errors.Is(err, memorylimit.ErrConflict) {
			status = http.StatusConflict
		}
		writeError(w, r, status, publicMessage(err, "Could not apply service memory limits. Refresh limits before retrying."))
		return
	}
	if !s.Supported {
		writeError(w, r, 501, "Service memory limits require a Linux systemd installation.")
		return
	}
	writeJSON(w, 200, s)
}
