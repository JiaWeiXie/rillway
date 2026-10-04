// Package control serves the authenticated management API and embedded UI.
package control

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"rillway/internal/config"
	"rillway/internal/dockerproxy"
	"rillway/internal/i18n"
	"rillway/internal/outbound"
	"strings"
	"sync"
)

// Backend owns configuration persistence and the running proxy engine.
type Backend interface {
	Config() config.Config
	Apply(context.Context, config.Config) error
	Snapshot() any
	Statuses(context.Context) []outbound.Status
	Action(context.Context, string, string, string) error
}

//go:embed web/*
var assets embed.FS

type handler struct {
	backend Backend
	token   string
	applyMu sync.Mutex
}

// New returns an HTTP handler. Its listener and TLS belong to the caller.
// No configuration, statistics, or provider state is available without a token.
func New(backend Backend, adminToken string) http.Handler {
	h := &handler{backend: backend, token: adminToken}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/config", h.protect(h.getConfig))
	mux.HandleFunc("GET /api/v1/defaults", h.protect(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, config.DefaultsForForms(h.backend.Config()))
	}))
	mux.HandleFunc("PUT /api/v1/config", h.protect(h.putConfig))
	mux.HandleFunc("GET /api/v1/stats", h.protect(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, h.backend.Snapshot()) }))
	mux.HandleFunc("GET /api/v1/outbounds", h.protect(h.getOutbounds))
	mux.HandleFunc("DELETE /api/v1/outbounds/{id}", h.protect(h.deleteOutbound))
	mux.HandleFunc("GET /api/v1/integrations/docker", h.protect(h.dockerExport))
	mux.HandleFunc("POST /api/v1/integrations/docker", h.protect(h.dockerExport))
	mux.HandleFunc("POST /api/v1/outbounds/{id}/{action}", h.protect(h.action))
	mux.HandleFunc("/api/", h.protect(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, 404, "Management API endpoint not found.")
	}))
	web, _ := fs.Sub(assets, "web")
	files := http.FileServer(http.FS(web))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		brandType := brandContentType(name)
		if r.URL.Path != "/" && r.URL.Path != "/app.js" && r.URL.Path != "/app.css" && r.URL.Path != "/i18n.js" && r.URL.Path != "/locales.json" && brandType == "" {
			http.NotFound(w, r)
			return
		}
		if brandType != "" {
			info, err := fs.Stat(web, name)
			if err != nil || info.IsDir() {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", brandType)
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		files.ServeHTTP(w, r)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Add("Vary", "Accept-Language")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Content-Language", string(i18n.FromAcceptLanguage(r.Header.Get("Accept-Language"))))
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; font-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		mux.ServeHTTP(w, r)
	})
}

func brandContentType(name string) string {
	if !fs.ValidPath(name) {
		return ""
	}
	if strings.HasPrefix(name, "fonts/") && strings.EqualFold(path.Ext(name), ".woff2") {
		return "font/woff2"
	}
	if strings.HasPrefix(name, "fonts/") && strings.EqualFold(path.Ext(name), ".ttf") {
		return "font/ttf"
	}
	if !strings.HasPrefix(name, "brand/") {
		return ""
	}
	return map[string]string{
		".png": "image/png", ".webp": "image/webp", ".jpg": "image/jpeg",
		".jpeg": "image/jpeg", ".ico": "image/x-icon",
	}[strings.ToLower(path.Ext(name))]
}

func (h *handler) protect(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.backend == nil || h.token == "" {
			writeError(w, r, 503, "The management service has no configured token.")
			return
		}
		provided, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(provided), []byte(h.token)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, r, 401, "Enter a valid management token to sign in.")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(r, origin) {
				writeError(w, r, 403, "Configuration changes from another origin are not allowed.")
				return
			}
		}
		next(w, r)
	}
}

func sameOrigin(r *http.Request, origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return u.Scheme == scheme && strings.EqualFold(u.Host, r.Host)
}

func (h *handler) getOutbounds(w http.ResponseWriter, r *http.Request) {
	type localizedStatus struct {
		outbound.Status
		DetailSource string `json:"detail_source,omitempty"`
	}
	originals := h.backend.Statuses(r.Context())
	statuses := make([]localizedStatus, 0, len(originals))
	locale := i18n.FromAcceptLanguage(r.Header.Get("Accept-Language"))
	for _, original := range originals {
		localized := localizedStatus{Status: original, DetailSource: original.Detail}
		localized.Detail = i18n.Message(locale, original.Detail)
		statuses = append(statuses, localized)
	}
	writeJSON(w, 200, statuses)
}

func (h *handler) getConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, h.backend.Config())
}

func (h *handler) dockerExport(w http.ResponseWriter, r *http.Request) {
	c := h.backend.Config()
	s, err := dockerproxy.FromConfig(c)
	if err != nil {
		writeError(w, r, 422, publicMessage(err, "Could not export Docker settings."))
		return
	}
	if r.Method == http.MethodPost {
		var body struct {
			ProxyURL string `json:"proxy_url"`
			NoProxy  string `json:"no_proxy"`
		}
		if !decodeBody(w, r, &body, 16384) {
			return
		}
		s, err = dockerproxy.New(body.ProxyURL, body.NoProxy)
		if err != nil {
			writeError(w, r, 422, publicMessage(err, "Could not export Docker settings."))
			return
		}
	}
	bundle, err := s.Bundle(c.Security.ProxyUsername != "")
	if err != nil {
		writeError(w, r, 422, publicMessage(err, "Could not export Docker settings."))
		return
	}
	writeJSON(w, 200, bundle)
}

func (h *handler) putConfig(w http.ResponseWriter, r *http.Request) {
	var next config.Config
	if !decodeBody(w, r, &next, 256<<10) {
		return
	}
	h.applyMu.Lock()
	defer h.applyMu.Unlock()
	if next.Revision != h.backend.Config().Revision {
		writeError(w, r, 409, "Configuration changed elsewhere. Reload it before saving.")
		return
	}
	if err := h.backend.Apply(r.Context(), next); err != nil {
		if errors.Is(err, config.ErrConflict) {
			writeError(w, r, 409, "Configuration changed elsewhere. Reload it before saving.")
		} else {
			writeError(w, r, 422, publicMessage(err, "Could not apply configuration. Check outbounds, rules, and required fields. Your previous configuration is unchanged."))
		}
		return
	}
	writeJSON(w, 200, h.backend.Config())
}

func (h *handler) action(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Value string `json:"value"`
	}
	if !decodeBody(w, r, &body, 4096) {
		return
	}
	id, action := r.PathValue("id"), r.PathValue("action")
	found := false
	for _, o := range h.backend.Config().Outbounds {
		if o.ID == id {
			found = true
			break
		}
	}
	if !found {
		writeError(w, r, 404, "Outbound not found.")
		return
	}
	switch action {
	case "connect", "disconnect", "register", "verify", "license", "login", "logout":
	default:
		writeError(w, r, 400, "This outbound action is not supported.")
		return
	}
	if action == "license" && strings.TrimSpace(body.Value) == "" {
		writeError(w, r, 400, "Enter a WARP+ license key.")
		return
	}
	if err := h.backend.Action(r.Context(), id, action, body.Value); err != nil {
		writeError(w, r, 422, publicMessage(err, "Could not complete the action. Check the outbound status and required settings."))
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		writeError(w, r, 415, "Use application/json for the request body.")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	err := d.Decode(dst)
	if err == nil {
		var extra any
		err = d.Decode(&extra)
		if err == io.EOF {
			return true
		}
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, r, 413, "The request body exceeds the size limit.")
	} else {
		writeError(w, r, 400, "Invalid JSON or unsupported fields.")
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, r *http.Request, status int, message string) {
	writeJSON(w, status, map[string]string{"error": i18n.Message(i18n.FromAcceptLanguage(r.Header.Get("Accept-Language")), message), "error_source": message})
}

// Only explicitly public messages may cross the API boundary. An ordinary
// backend error may include credentials or raw output, even when wrapped.
func publicMessage(err error, fallback string) string {
	var public interface{ PublicMessage() string }
	if errors.As(err, &public) {
		if message := public.PublicMessage(); strings.TrimSpace(message) != "" {
			return message
		}
	}
	return fallback
}
