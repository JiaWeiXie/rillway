package control

import (
	"context"
	"errors"
	"net/http/httptest"
	"rillway/internal/config"
	"rillway/internal/resourcecontrol"
	"strings"
	"testing"
)

type resourceBackend struct {
	*fakeBackend
	state   resourcecontrol.MemoryStatus
	failure error
	writes  int
}

func (b *resourceBackend) MemoryStatus(context.Context) (resourcecontrol.MemoryStatus, error) {
	return b.state, b.failure
}

func (b *resourceBackend) ApplyMemory(_ context.Context, r resourcecontrol.MemoryRequest) (resourcecontrol.MemoryStatus, error) {
	if b.failure != nil {
		return b.state, b.failure
	}
	if r.Revision != b.state.Revision {
		return b.state, config.PublicError{Message: resourcecontrol.ErrMemoryConflict.Error(), Err: resourcecontrol.ErrMemoryConflict}
	}
	n, err := resourcecontrol.CalculateMemory(r, b.state)
	if err != nil {
		return b.state, config.PublicError{Message: err.Error()}
	}
	b.state.LimitBytes = n
	b.writes++
	return b.state, nil
}

func TestMemoryAPIAuthValidationAndConflicts(t *testing.T) {
	b := &resourceBackend{fakeBackend: newBackend(), state: resourcecontrol.MemoryStatus{Supported: true, HostBytes: 4 * resourcecontrol.GiB, MinimumBytes: 256 * resourcecontrol.MiB, MaximumBytes: resourcecontrol.MemoryMaximum(4 * resourcecontrol.GiB), Revision: "one"}}
	h := New(b, "token")
	for _, method := range []string{"GET", "PUT"} {
		if w := request(h, method, "/api/v1/service/memory", `{"mode":"MiB","value":"512","revision":"one"}`, "", ""); w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	for _, tc := range []struct {
		body string
		code int
	}{{`{"mode":"MiB","value":"512","revision":"one","command":"bad"}`, 400}, {`{"mode":"MiB","value":"512","revision":"old"}`, 409}, {`{"mode":"MiB","value":"128","revision":"one"}`, 422}} {
		if w := request(h, "PUT", "/api/v1/service/memory", tc.body, "token", ""); w.Code != tc.code || b.writes != 0 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	server := httptest.NewServer(h)
	defer server.Close()
	client, err := NewClient(server.URL, "token", "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := client.Memory(context.Background())
	if err != nil || !s.Supported {
		t.Fatal(s, err)
	}
	s, err = client.ApplyMemory(context.Background(), resourcecontrol.MemoryRequest{Mode: "GiB", Value: "0.5", Revision: "one"})
	if err != nil || s.LimitBytes != 512*resourcecontrol.MiB || b.writes != 1 {
		t.Fatal(s, err)
	}
	b.failure = errors.New("private key and command output")
	if w := request(h, "GET", "/api/v1/service/memory", "", "token", ""); w.Code != 503 || strings.Contains(w.Body.String(), "private") {
		t.Fatal(w.Body.String())
	}
	if w := request(New(newBackend(), "token"), "PUT", "/api/v1/service/memory", `{"mode":"MiB","value":"512"}`, "token", ""); w.Code != 501 {
		t.Fatal(w.Code)
	}
}
