package control

import (
	"net/http"
	"rillway/internal/config"
)

func (h *handler) explainRoute(w http.ResponseWriter, r *http.Request) {
	var req config.ExplainRequest
	if !decodeBody(w, r, &req, 8<<10) {
		return
	}
	result, err := h.backend.Explain(req)
	if err != nil {
		writeError(w, r, 422, "Could not explain the route. Check the host, port, source and inbound values.")
		return
	}
	writeJSON(w, 200, result)
}
