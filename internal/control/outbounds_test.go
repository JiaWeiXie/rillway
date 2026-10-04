package control

import (
	"encoding/json"
	"errors"
	"rillway/internal/config"
	"strings"
	"sync"
	"testing"
)

func TestDeleteOutboundRequiresAuthorizationRevisionAndReplacement(t *testing.T) {
	for _, tc := range []struct {
		id, body, token, origin string
		code                    int
	}{
		{"warp", `{"revision":1,"replacement":"direct"}`, "", "", 401},
		{"warp", `{"revision":1,"replacement":"direct"}`, "token", "http://evil.example", 403},
		{"warp", `{"revision":0,"replacement":"direct"}`, "token", "", 409},
		{"direct", `{"revision":1,"replacement":"warp"}`, "token", "", 422},
		{"missing", `{"revision":1,"replacement":"direct"}`, "token", "", 404},
		{"warp", `{"revision":1}`, "token", "", 422},
		{"warp", `{"revision":1,"replacement":"warp"}`, "token", "", 422},
		{"warp", `{"revision":1,"replacement":"direct","secret":"bad"}`, "token", "", 400},
		{"warp", `{"revision":1,"replacement":"direct"}`, "token", "http://rillway.test", 200},
	} {
		b := newBackend()
		b.cfg = config.Default("state")
		before, _ := json.Marshal(b.Config())
		w := request(New(b, "token"), "DELETE", "/api/v1/outbounds/"+tc.id, tc.body, tc.token, tc.origin)
		if w.Code != tc.code {
			t.Fatalf("%s %s: %d %s", tc.id, tc.body, w.Code, w.Body.String())
		}
		if tc.code != 200 {
			after, _ := json.Marshal(b.Config())
			if string(before) != string(after) {
				t.Fatal("rejected deletion changed configuration")
			}
		} else if b.Config().Revision != 2 || config.Validate(b.Config()) != nil || b.Config().Rules[1].Outbound != "direct" {
			t.Fatal(b.Config())
		}
		if b.lastAction != "" {
			t.Fatal("deletion invoked VPN registration or disconnect")
		}
	}
}

func TestDeleteOutboundApplyFailureKeepsConfiguration(t *testing.T) {
	for _, failure := range []error{errors.New("private credentials"), config.ErrConflict} {
		b := newBackend()
		b.cfg = config.Default("state")
		b.failApply = failure
		w := request(New(b, "token"), "DELETE", "/api/v1/outbounds/warp", `{"revision":1,"replacement":"direct"}`, "token", "")
		if w.Code < 400 || strings.Contains(w.Body.String(), "private credentials") || b.Config().Revision != 1 || len(b.Config().Outbounds) != 2 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestConcurrentDeletesOnlyOneWins(t *testing.T) {
	b := newBackend()
	b.cfg = config.Default("state")
	h := New(b, "token")
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Go(func() {
			codes <- request(h, "DELETE", "/api/v1/outbounds/warp", `{"revision":1,"replacement":"direct"}`, "token", "").Code
		})
	}
	wg.Wait()
	close(codes)
	wins, conflicts := 0, 0
	for code := range codes {
		switch code {
		case 200:
			wins++
		case 409:
			conflicts++
		default:
			t.Fatal(code)
		}
	}
	if wins != 1 || conflicts != 1 || b.Config().Revision != 2 {
		t.Fatal(wins, conflicts)
	}
}
