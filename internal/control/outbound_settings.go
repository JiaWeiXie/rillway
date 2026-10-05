package control

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"rillway/internal/config"
	"rillway/internal/outbound"
	"strings"
)

// Secrets are write-only inputs, separate from the portable configuration.
// Each save uses a new private file so failed updates cannot alter a running
// provider's profile or an operator-managed file.
func (h *handler) saveOutbound(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Revision        uint64          `json:"revision"`
		Create          bool            `json:"create,omitempty"`
		Outbound        config.Outbound `json:"outbound"`
		WireGuardConfig string          `json:"wireguard_config,omitempty"`
		TailscaleKey    string          `json:"tailscale_auth_key,omitempty"`
	}
	if !decodeBody(w, r, &body, 2<<20) {
		return
	}
	h.applyMu.Lock()
	defer h.applyMu.Unlock()
	c := h.backend.Config()
	c.Outbounds = append([]config.Outbound(nil), c.Outbounds...)
	if body.Revision != c.Revision {
		writeError(w, r, 409, "Configuration changed elsewhere. Reload it before saving.")
		return
	}
	o := body.Outbound
	if o.ID == "direct" {
		writeError(w, r, 422, config.DirectRequired)
		return
	}
	index := -1
	for i, existing := range c.Outbounds {
		if existing.ID == o.ID {
			index = i
			break
		}
	}
	if body.Create && index >= 0 {
		writeError(w, r, 422, "An outbound with this name already exists. Choose a different name.")
		return
	}
	if (body.WireGuardConfig != "" && o.Type != "wireguard") || (body.TailscaleKey != "" && o.Type != "tailscale") {
		writeError(w, r, 422, "The supplied credentials do not match the outbound type.")
		return
	}
	if body.WireGuardConfig != "" {
		if len(body.WireGuardConfig) > 1<<20 {
			writeError(w, r, 422, "WireGuard configuration must be at most 1 MiB.")
			return
		}
		if _, err := outbound.ParseWireGuard(body.WireGuardConfig); err != nil {
			// Parser errors can contain attacker-supplied field names or values.
			writeError(w, r, 422, "Invalid WireGuard configuration. Check keys, addresses, peers and DNS. Shell hooks are not supported.")
			return
		}
		o.ConfigFile = "pending-managed-profile"
	}
	key := strings.TrimSpace(body.TailscaleKey)
	if body.TailscaleKey != "" && (len(key) > 4096 || !strings.HasPrefix(key, "tskey-auth-") || strings.ContainsAny(key, "\r\n\t ")) {
		writeError(w, r, 422, "Enter a Tailscale auth key beginning with tskey-auth- (at most 4096 bytes).")
		return
	}
	if index < 0 && o.Type == "tailscale" {
		// Ignore client-supplied state paths. Choose a daemon-managed directory
		// only after the complete configuration has passed validation.
		o.StateDir = "pending-managed-state"
	}
	if index < 0 {
		c.Outbounds = append(c.Outbounds, o)
		index = len(c.Outbounds) - 1
	} else {
		c.Outbounds[index] = o
	}
	if err := config.Validate(c); err != nil {
		writeError(w, r, 422, "Invalid outbound settings. Check the name, type, addresses and routing references.")
		return
	}
	state := filepath.Dir(c.Security.AdminTokenFile)
	if o.Type == "tailscale" && o.StateDir == "pending-managed-state" {
		o.StateDir = config.DefaultsForForms(h.backend.Config()).Outbounds["tailscale"].StateDir
	}
	secretDir := ""
	if body.WireGuardConfig != "" || key != "" {
		var err error
		secretDir, err = os.MkdirTemp(state, "outbound-secret-")
		if err != nil {
			writeError(w, r, 422, "Could not save outbound credentials in the server state directory.")
			return
		}
		committed := false
		defer func() {
			if !committed {
				_ = os.RemoveAll(secretDir)
			}
		}()
		name, content := "wireguard.conf", body.WireGuardConfig
		if key != "" {
			name, content = "tailscale.key", key
		}
		filename := filepath.Join(secretDir, name)
		if err := config.WritePrivate(filename, []byte(content)); err != nil {
			writeError(w, r, 422, "Could not save outbound credentials in the server state directory.")
			return
		}
		if name == "wireguard.conf" {
			o.ConfigFile = filename
		} else {
			o.AuthKeyFile = filename
		}
		c.Outbounds[index] = o
		if !h.applyOutbound(w, r, c) {
			return
		}
		committed = true
		return
	}
	c.Outbounds[index] = o
	h.applyOutbound(w, r, c)
}

func (h *handler) applyOutbound(w http.ResponseWriter, r *http.Request, c config.Config) bool {
	if err := h.backend.Apply(r.Context(), c); err != nil {
		if errors.Is(err, config.ErrConflict) {
			writeError(w, r, 409, "Configuration changed elsewhere. Reload it before saving.")
		} else {
			writeError(w, r, 422, publicMessage(err, "Could not apply outbound settings. Your previous configuration is unchanged."))
		}
		return false
	}
	writeJSON(w, 200, h.backend.Config())
	return true
}
