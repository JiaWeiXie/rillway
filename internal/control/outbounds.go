package control

import (
	"errors"
	"net/http"
	"rillway/internal/config"
)

func (h *handler) deleteOutbound(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Revision    uint64 `json:"revision"`
		Replacement string `json:"replacement"`
	}
	if !decodeBody(w, r, &body, 4096) {
		return
	}
	h.applyMu.Lock()
	defer h.applyMu.Unlock()
	c := h.backend.Config()
	if body.Revision != c.Revision {
		writeError(w, r, 409, "Configuration changed elsewhere. Reload it before saving.")
		return
	}
	id := r.PathValue("id")
	next, err := config.RemoveOutbound(c, id, body.Replacement)
	if err != nil {
		code := 422
		if err.Error() == config.OutboundMissing {
			code = 404
		}
		writeError(w, r, code, publicMessage(err, "Could not delete outbound. Your previous configuration is unchanged."))
		return
	}
	if err := h.backend.Apply(r.Context(), next); err != nil {
		if errors.Is(err, config.ErrConflict) {
			writeError(w, r, 409, "Configuration changed elsewhere. Reload it before saving.")
		} else {
			writeError(w, r, 422, publicMessage(err, "Could not delete outbound. Your previous configuration is unchanged."))
		}
		return
	}
	writeJSON(w, 200, h.backend.Config())
}
