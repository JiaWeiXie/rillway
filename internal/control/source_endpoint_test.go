package control

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSourceEndpointsAuthValidationAndClientRoundtrip(t *testing.T) {
	b := newBackend()
	h := New(b, "token")
	s := httptest.NewServer(h)
	defer s.Close()

	// Unauthenticated GET/POST rejected with 401
	w := request(h, "GET", "/api/v1/source-clients", "", "", "")
	if w.Code != 401 {
		t.Fatalf("unauth GET status = %d", w.Code)
	}
	w = request(h, "POST", "/api/v1/source-clients/block", `{"revision":7,"address":"192.0.2.1"}`, "", "")
	if w.Code != 401 {
		t.Fatalf("unauth POST status = %d", w.Code)
	}

	// Cross-origin write rejected with 403
	w = request(h, "POST", "/api/v1/source-clients/block", `{"revision":7,"address":"192.0.2.1"}`, "token", "http://attacker.invalid")
	if w.Code != 403 {
		t.Fatalf("cross-origin status = %d", w.Code)
	}

	// Bounded body limit: payload > 8 KiB rejected
	bigPayload := `{"revision":7,"address":"` + strings.Repeat("a", 9000) + `"}`
	w = request(h, "POST", "/api/v1/source-clients/block", bigPayload, "token", "")
	if w.Code != 413 && w.Code != 400 {
		t.Fatalf("large body status = %d", w.Code)
	}

	// Conflict on stale revision -> 409
	w = request(h, "POST", "/api/v1/source-clients/block", `{"revision":6,"address":"192.0.2.1"}`, "token", "")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "Configuration changed elsewhere") {
		t.Fatalf("stale revision status = %d body = %s", w.Code, w.Body.String())
	}

	// Invalid input -> 422 with public message
	w = request(h, "POST", "/api/v1/source-clients/block", `{"revision":7,"address":"invalid"}`, "token", "")
	if w.Code != 422 || !strings.Contains(w.Body.String(), "Invalid client IP address") {
		t.Fatalf("invalid input status = %d body = %s", w.Code, w.Body.String())
	}

	// Client roundtrip GET & POST
	client := &Client{
		base:  s.URL,
		token: "token",
		http:  &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}},
	}

	snapshot, err := client.SourceClients(context.Background())
	if err != nil || len(snapshot.Clients) != 1 || snapshot.Clients[0].Address != "192.0.2.1" {
		t.Fatalf("client.SourceClients failed: %v %+v", err, snapshot)
	}

	blockResult, err := client.BlockSource(context.Background(), 7, "192.0.2.1")
	if err != nil || blockResult.Disconnected != 1 || blockResult.Config.Revision != 8 {
		t.Fatalf("client.BlockSource failed: %v %+v", err, blockResult)
	}
}
