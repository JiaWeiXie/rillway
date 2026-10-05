package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"rillway/internal/config"
	"rillway/internal/i18n"
	"strings"
	"sync/atomic"
	"testing"
)

func invokeAgent(t *testing.T, args []string, input string) (agentResult, error) {
	t.Helper()
	var out bytes.Buffer
	err := agentCommand(t.Context(), args, strings.NewReader(input), &out)
	var result agentResult
	d := json.NewDecoder(&out)
	if decodeErr := d.Decode(&result); decodeErr != nil {
		t.Fatalf("not JSON: %v %s", decodeErr, out.String())
	}
	if out.Len() != 0 || result.SchemaVersion != 1 || result.OK != (err == nil) {
		t.Fatalf("invalid result envelope: %+v %v", result, err)
	}
	return result, err
}

func TestAgentOfflineAndPrivateErrors(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		input string
		code  string
	}{
		{[]string{"schema"}, "", ""},
		{[]string{"validate", "--input", "-"}, `{"secret-never-print": "private-test-value"}`, "invalid_configuration"},
		{[]string{"validate", "--input", "-"}, strings.Repeat(" ", (256<<10)+1), "invalid_input"},
		{[]string{"validate", "--input", "-"}, "{} {}", "invalid_configuration"},
		{[]string{"validate"}, "", "invalid_input"},
		{[]string{"status", "--config", filepath.Join(t.TempDir(), "private-test-value")}, "", "local_configuration_unavailable"},
		{[]string{"status", "--secret-never-print", "private-test-value"}, "", "invalid_arguments"},
		{[]string{"status", "--timeout", "0"}, "", "invalid_arguments"},
		{[]string{"status", "extra"}, "", "invalid_arguments"},
		{[]string{"restart"}, "", "confirmation_required"},
		{[]string{"apply", "--input", "-"}, "", "confirmation_required"},
		{[]string{"outbound", "--yes", "--id", "warp", "--action", "license"}, "", "invalid_arguments"},
		{[]string{"outbound", "--yes", "--id", "../warp", "--action", "connect"}, "", "invalid_arguments"},
	} {
		result, err := invokeAgent(t, tc.args, tc.input)
		if tc.code == "" {
			if err != nil {
				t.Fatal(err)
			}
		} else if result.Error == nil || result.Error.Code != tc.code || strings.Contains(result.Error.Message, "private-test-value") {
			t.Fatalf("args %v: %+v", tc.args, result)
		}
	}
	c := config.Default("/var/lib/rillway")
	body, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := invokeAgent(t, []string{"validate", "--input", "-"}, string(body)); err != nil || !result.OK {
		t.Fatal(result, err)
	}
}

func TestAgentPlansCASAndExplicitActions(t *testing.T) {
	current := config.Default("/var/lib/rillway")
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-agent-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"private-test-value"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/config":
			_ = json.NewEncoder(w).Encode(current)
		case r.Method == "GET" && r.URL.Path == "/api/v1/outbounds":
			_, _ = w.Write([]byte(`[{"id":"warp","state":"ready","detail":"private-test-value","auth_url":"https://example.invalid/private-test-value"}]`))
		case r.Method == "PUT":
			var next config.Config
			_ = json.NewDecoder(r.Body).Decode(&next)
			if next.Revision != current.Revision {
				w.WriteHeader(http.StatusConflict)
				return
			}
			next.Revision++
			current = next
			writes++
			_ = json.NewEncoder(w).Encode(next)
		case r.Method == "POST":
			writes++
			_, _ = w.Write([]byte(`{"accepted":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("synthetic-agent-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	connection := []string{"--url", server.URL, "--token-file", token}
	invoke := func(command string, candidate config.Config, extras ...string) (agentResult, error) {
		t.Helper()
		body, err := json.Marshal(candidate)
		if err != nil {
			t.Fatal(err)
		}
		args := append([]string{command}, connection...)
		args = append(args, extras...)
		return invokeAgent(t, args, string(body))
	}
	status, err := invoke("status", current)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(status)
	if strings.Contains(string(data), "private-test-value") || strings.Contains(string(data), "synthetic-agent-token") {
		t.Fatal("status leaked provider detail or login URL")
	}
	candidate := current
	candidate.Adaptive.Enabled = true
	plan, err := invoke("plan", candidate, "--input", "-")
	if err != nil || writes != 0 || !strings.Contains(string(mustAgentJSON(t, plan.Data)), "adaptive") {
		t.Fatal("plan wrote or did not show the change", plan, err, writes)
	}
	if _, err := invoke("apply", candidate, "--input", "-", "--yes"); err != nil || writes != 1 {
		t.Fatal("apply failed", err, writes)
	}
	if result, err := invoke("apply", candidate, "--input", "-", "--yes"); err == nil || result.Error.Code != "revision_conflict" || writes != 1 {
		t.Fatal("stale apply accepted", result, writes)
	}
	if _, err := invoke("apply", current, "--input", "-", "--yes"); err != nil || writes != 1 {
		t.Fatal("no-op wrote configuration", err, writes)
	}
	candidate = current
	candidate.Listeners.HTTP = "127.0.0.1:28000"
	if result, err := invoke("plan", candidate, "--input", "-"); err == nil || result.Error.Code != "server_settings_required" {
		t.Fatal("listener changes bypassed server restriction", result)
	}
	if _, err := invoke("outbound", current, "--id", "warp", "--action", "disconnect", "--yes"); err != nil || writes != 2 {
		t.Fatal("explicit outbound operation failed", err, writes)
	}
	if _, err := invoke("restart", current, "--yes"); err != nil || writes != 3 {
		t.Fatal("explicit restart failed", err, writes)
	}
	if err := os.WriteFile(token, []byte("wrong"), 0o600); err != nil {
		t.Fatal(err)
	}
	if result, err := invoke("status", current); err == nil || result.Error.Code != "access_denied" || strings.Contains(result.Error.Message, "private-test-value") {
		t.Fatal("authentication error leaked server response", result)
	}
}

func mustAgentJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAgentFailureCodesAndLanguage(t *testing.T) {
	var out bytes.Buffer
	err := run(i18n.WithLocale(t.Context(), i18n.English), []string{"--lang", "zh-Hant", "agent", "restart"}, &out)
	var failure *agentFailure
	if !errors.As(err, &failure) || failure.ExitCode != 2 || !strings.Contains(out.String(), "此操作需要") || !strings.Contains(out.String(), "confirmation_required") {
		t.Fatal("machine error lost classification or localization", err, out.String())
	}
	if _, err := agentClient("", "http://example.invalid", "-", ""); err == nil {
		t.Fatal("missing token/stdin was accepted")
	}
	out.Reset()
	err = run(t.Context(), []string{"agent", "status", "--lang", "private-test-value"}, &out)
	if !errors.As(err, &failure) || failure.Code != "invalid_language" || strings.Contains(out.String(), "private-test-value") {
		t.Fatal("invalid language broke the JSON/privacy contract", err, out.String())
	}
}

func TestAgentTLSAndAmbiguousWriteAreNotRetried(t *testing.T) {
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("synthetic-agent-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	result, err := invokeAgent(t, []string{"config", "--url", server.URL, "--token-file", token}, "")
	if err == nil || result.Error.Code != "connection_failed" {
		t.Fatal("untrusted TLS was accepted", result)
	}
	if _, err := agentClient("", "http://example.invalid", token, ""); err == nil {
		t.Fatal("non-loopback plain HTTP accepted")
	}
	var writes atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writes.Add(1)
		// Simulate a mutation accepted by the server, with a lost acknowledgement.
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("test server cannot hijack")
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	defer plain.Close()
	result, err = invokeAgent(t, []string{"restart", "--url", plain.URL, "--token-file", token, "--yes"}, "")
	if err == nil || result.Error.Code != "connection_failed" || writes.Load() != 1 {
		t.Fatal("ambiguous mutation was retried or hidden", result, writes.Load())
	}
}
