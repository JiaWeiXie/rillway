package control

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"rillway/internal/config"
	"strings"
	"testing"
)

func importedProfile() string {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	return "[Interface]\nPrivateKey = " + key + "\nAddress = 10.77.0.2/24\n[Peer]\nPublicKey = " + key + "\nEndpoint = 192.0.2.1:51820\nAllowedIPs = 0.0.0.0/0\n"
}

func outboundBody(t *testing.T, revision uint64, o config.Outbound, profile, key string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"revision": revision, "outbound": o, "wireguard_config": profile, "tailscale_auth_key": key})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestImportedOutboundCredentialsArePrivateAndWriteOnly(t *testing.T) {
	for _, kind := range []string{"wireguard", "tailscale"} {
		t.Run(kind, func(t *testing.T) {
			state := t.TempDir()
			b := &fakeBackend{cfg: config.Default(state)}
			o := config.Outbound{ID: "vpn", Type: kind, Enabled: false}
			profile, key := "", ""
			if kind == "wireguard" {
				profile = importedProfile()
			} else {
				key = "tskey-auth-" + strings.Repeat("x", 30)
				o.StateDir = "/untrusted/client/path"
			}
			h := New(b, "correct")
			w := request(h, "PUT", "/api/v1/outbounds", outboundBody(t, 1, o, profile, key), "correct", "")
			if w.Code != 200 {
				t.Fatalf("save: %d %s", w.Code, w.Body.String())
			}
			saved := b.Config().Outbounds[2]
			filename := saved.ConfigFile
			if kind == "tailscale" {
				filename = saved.AuthKeyFile
				if !strings.HasPrefix(saved.StateDir, state+string(os.PathSeparator)) {
					t.Fatal("client selected the state directory")
				}
			}
			secret, err := os.ReadFile(filename)
			if err != nil || string(secret) != profile+key {
				t.Fatal("credential file was not saved")
			}
			for path, want := range map[string]os.FileMode{filename: 0o600, filepath.Dir(filename): 0o700} {
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != want {
					t.Fatalf("unsafe permissions on %s", path)
				}
			}
			for _, response := range []string{w.Body.String(), request(h, "GET", "/api/v1/config", "", "correct", "").Body.String()} {
				if strings.Contains(response, "PrivateKey") || (key != "" && strings.Contains(response, key)) {
					t.Fatal("API returned credentials")
				}
			}
			// Blank secret fields preserve the file instead of replacing it.
			w = request(h, "PUT", "/api/v1/outbounds", outboundBody(t, 2, saved, "", ""), "correct", "")
			if w.Code != 200 || b.Config().Outbounds[2].ConfigFile != saved.ConfigFile || b.Config().Outbounds[2].AuthKeyFile != saved.AuthKeyFile {
				t.Fatal("blank edit discarded existing credentials")
			}
		})
	}
}

func TestOutboundImportFailureCannotChangeProfileOrLeaveSecrets(t *testing.T) {
	state := t.TempDir()
	b := &fakeBackend{cfg: config.Default(state), failApply: errors.New("private upstream diagnostic")}
	h := New(b, "correct")
	w := request(h, "PUT", "/api/v1/outbounds", outboundBody(t, 1, config.Outbound{ID: "vpn", Type: "wireguard"}, importedProfile(), ""), "correct", "")
	if w.Code != 422 || strings.Contains(w.Body.String(), "private upstream") || b.Config().Revision != 1 || len(b.Config().Outbounds) != 2 {
		t.Fatalf("failure changed state or leaked an error: %s", w.Body.String())
	}
	entries, err := os.ReadDir(state)
	if err != nil || len(entries) != 0 {
		t.Fatal("failed import left credential files")
	}
}

func TestCreateDoesNotOverwriteExistingOutbound(t *testing.T) {
	state := t.TempDir()
	b := &fakeBackend{cfg: config.Default(state)}
	body := outboundBody(t, 1, config.Outbound{ID: "warp", Type: "wireguard"}, importedProfile(), "")
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatal(err)
	}
	payload["create"] = true
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	w := request(New(b, "correct"), "PUT", "/api/v1/outbounds", string(data), "correct", "")
	entries, err := os.ReadDir(state)
	if w.Code != 422 || b.Config().Outbounds[1].Type != "warp" || err != nil || len(entries) != 0 {
		t.Fatal("create replaced existing settings or wrote secrets")
	}
}

func TestOutboundImportRejectsBeforeWriting(t *testing.T) {
	for _, tc := range []struct {
		name, token, origin, profile, key string
		revision                          uint64
		outbound                          config.Outbound
		status                            int
	}{
		{name: "authentication", revision: 1, outbound: config.Outbound{ID: "vpn", Type: "wireguard"}, profile: importedProfile(), status: 401},
		{name: "origin", revision: 1, token: "correct", origin: "https://evil.test", outbound: config.Outbound{ID: "vpn", Type: "wireguard"}, profile: importedProfile(), status: 403},
		{name: "stale", revision: 2, token: "correct", outbound: config.Outbound{ID: "vpn", Type: "wireguard"}, profile: importedProfile(), status: 409},
		{name: "traversal", revision: 1, token: "correct", outbound: config.Outbound{ID: "../vpn", Type: "wireguard"}, profile: importedProfile(), status: 422},
		{name: "shell", revision: 1, token: "correct", outbound: config.Outbound{ID: "vpn", Type: "wireguard"}, profile: importedProfile() + "PostUp = private-input\n", status: 422},
		{name: "oversized", revision: 1, token: "correct", outbound: config.Outbound{ID: "vpn", Type: "wireguard"}, profile: strings.Repeat("x", (1<<20)+1), status: 422},
		{name: "wrong-type", revision: 1, token: "correct", outbound: config.Outbound{ID: "vpn", Type: "warp"}, profile: importedProfile(), status: 422},
		{name: "bad-key", revision: 1, token: "correct", outbound: config.Outbound{ID: "vpn", Type: "tailscale"}, key: "tskey-auth-private-input\nextra", status: 422},
		{name: "direct", revision: 1, token: "correct", outbound: config.Outbound{ID: "direct", Type: "wireguard"}, profile: importedProfile(), status: 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := t.TempDir()
			b := &fakeBackend{cfg: config.Default(state)}
			w := request(New(b, "correct"), "PUT", "/api/v1/outbounds", outboundBody(t, tc.revision, tc.outbound, tc.profile, tc.key), tc.token, tc.origin)
			entries, err := os.ReadDir(state)
			if w.Code != tc.status || err != nil || len(entries) != 0 || b.Config().Revision != 1 || strings.Contains(w.Body.String(), "private-input") {
				t.Fatalf("rejection: %d %s, files=%d", w.Code, w.Body.String(), len(entries))
			}
		})
	}
}
