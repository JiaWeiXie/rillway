package control

import (
	"context"
	"errors"
	"net/http/httptest"
	"rillway/internal/config"
	"rillway/internal/memorylimit"
	"strings"
	"testing"
)

type resourceBackend struct {
	*fakeBackend
	state   memorylimit.Status
	failure error
	writes  int
}

func (b *resourceBackend) MemoryStatus(context.Context) (memorylimit.Status, error) {
	return b.state, b.failure
}

func (b *resourceBackend) ApplyMemory(_ context.Context, r memorylimit.Request) (memorylimit.Status, error) {
	if b.failure != nil {
		return b.state, b.failure
	}
	if r.Revision != b.state.Revision {
		return b.state, config.PublicError{Message: memorylimit.ErrConflict.Error(), Err: memorylimit.ErrConflict}
	}
	n, err := memorylimit.Calculate(r, b.state)
	if err != nil {
		return b.state, config.PublicError{Message: err.Error()}
	}
	b.state.LimitBytes = n
	b.writes++
	return b.state, nil
}

func TestMemoryAPIAuthValidationAndConflicts(t *testing.T) {
	b := &resourceBackend{fakeBackend: newBackend(), state: memorylimit.Status{Supported: true, HostBytes: 4 * memorylimit.GiB, MinimumBytes: 256 * memorylimit.MiB, MaximumBytes: memorylimit.Maximum(4 * memorylimit.GiB), Revision: "one"}}
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
	s, err = client.ApplyMemory(context.Background(), memorylimit.Request{Mode: "GiB", Value: "0.5", Revision: "one"})
	if err != nil || s.LimitBytes != 512*memorylimit.MiB || b.writes != 1 {
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
