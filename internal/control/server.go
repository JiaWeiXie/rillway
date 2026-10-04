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
	"rillway/internal/config"
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
	mux.HandleFunc("PUT /api/v1/config", h.protect(h.putConfig))
	mux.HandleFunc("GET /api/v1/stats", h.protect(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, h.backend.Snapshot()) }))
	mux.HandleFunc("GET /api/v1/outbounds", h.protect(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, h.backend.Statuses(r.Context())) }))
	mux.HandleFunc("POST /api/v1/outbounds/{id}/{action}", h.protect(h.action))
	mux.HandleFunc("/api/", h.protect(func(w http.ResponseWriter, _ *http.Request) { writeError(w, 404, "找不到這個管理 API") }))
	web, _ := fs.Sub(assets, "web")
	files := http.FileServer(http.FS(web))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/" && r.URL.Path != "/app.js" && r.URL.Path != "/app.css" {
			http.NotFound(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		mux.ServeHTTP(w, r)
	})
}

func (h *handler) protect(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.backend == nil || h.token == "" {
			writeError(w, 503, "管理服務尚未設定存取金鑰")
			return
		}
		provided, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(provided), []byte(h.token)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, 401, "管理金鑰不正確或尚未登入")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(r, origin) {
				writeError(w, 403, "不接受其他網站發起的設定變更")
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

func (h *handler) getConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, h.backend.Config())
}

func (h *handler) putConfig(w http.ResponseWriter, r *http.Request) {
	var next config.Config
	if !decodeBody(w, r, &next, 256<<10) {
		return
	}
	h.applyMu.Lock()
	defer h.applyMu.Unlock()
	if next.Revision != h.backend.Config().Revision {
		writeError(w, 409, "設定已在其他地方變更，請重新載入後再儲存")
		return
	}
	if err := h.backend.Apply(r.Context(), next); err != nil {
		if errors.Is(err, config.ErrConflict) {
			writeError(w, 409, "設定已在其他地方變更，請重新載入後再儲存")
		} else {
			writeError(w, 422, publicMessage(err, "設定無法套用，請確認出口、規則與必要欄位；目前設定保持不變"))
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
		writeError(w, 404, "找不到這個出口")
		return
	}
	switch action {
	case "connect", "disconnect", "register", "verify", "license", "login", "logout":
	default:
		writeError(w, 400, "不支援這個出口操作")
		return
	}
	if action == "license" && strings.TrimSpace(body.Value) == "" {
		writeError(w, 400, "請輸入 WARP+ 授權碼")
		return
	}
	if err := h.backend.Action(r.Context(), id, action, body.Value); err != nil {
		writeError(w, 422, publicMessage(err, "出口操作未完成，請檢查出口狀態與必要設定"))
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		writeError(w, 415, "請使用 application/json")
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
		writeError(w, 413, "設定內容超過大小限制")
	} else {
		writeError(w, 400, "JSON 格式或欄位不正確")
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
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
