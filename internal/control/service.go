package control

import (
	"context"
	"net/http"
)

func (h *handler) restartService(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if !decodeBody(w, r, &body, 1024) {
		return
	}
	backend, ok := h.backend.(interface {
		PrepareRestart(context.Context) (func(), error)
	})
	if !ok {
		writeError(w, r, 422, "Service restart is not available in this session.")
		return
	}
	h.applyMu.Lock()
	defer h.applyMu.Unlock()
	restart, err := backend.PrepareRestart(r.Context())
	if err != nil {
		writeError(w, r, 422, publicMessage(err, "Could not restart the service."))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	restart()
}
