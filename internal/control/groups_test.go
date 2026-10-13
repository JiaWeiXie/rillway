package control

import (
	"encoding/json"
	"errors"
	"reflect"
	"rillway/internal/config"
	"strings"
	"sync"
	"testing"
)

func groupDeletionBackend(t *testing.T) *fakeBackend {
	t.Helper()
	b := newBackend()
	b.cfg = config.Default(t.TempDir())
	b.cfg.Outbounds[1].Enabled = true
	b.cfg.OutboundGroups = []config.OutboundGroup{
		{ID: "group", Type: "fallback", Enabled: true, Members: []string{"warp"}},
		{ID: "replacement", Type: "select", Enabled: true, Members: []string{"warp"}, Selected: "warp"},
		{ID: "disabled", Type: "fallback", Enabled: false, Members: []string{"warp"}},
	}
	b.cfg.DefaultOutbound = "group"
	b.cfg.Rules = []config.Rule{
		{ID: "referenced", Domains: []string{"example.com"}, Outbound: "group"},
		{ID: "unrelated", Domains: []string{"example.org"}, Outbound: "direct"},
	}
	if err := config.Validate(b.cfg); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDeleteGroupAuthorizationReplacementAndRevision(t *testing.T) {
	for _, tc := range []struct {
		name, id, body, token, origin string
		status                        int
	}{
		{"auth", "group", `{"revision":1,"replacement":"warp"}`, "", "", 401},
		{"cross origin", "group", `{"revision":1,"replacement":"warp"}`, "token", "http://evil.example", 403},
		{"stale", "group", `{"revision":0,"replacement":"warp"}`, "token", "", 409},
		{"missing", "missing", `{"revision":1}`, "token", "", 404},
		{"missing replacement", "group", `{"revision":1}`, "token", "", 422},
		{"self replacement", "group", `{"revision":1,"replacement":"group"}`, "token", "", 422},
		{"unknown replacement", "group", `{"revision":1,"replacement":"missing"}`, "token", "", 422},
		{"disabled replacement", "group", `{"revision":1,"replacement":"disabled"}`, "token", "", 422},
		{"unknown field", "group", `{"revision":1,"replacement":"warp","unknown":true}`, "token", "", 400},
		{"oversize", "group", `{"revision":1,"replacement":"` + strings.Repeat("x", 4096) + `"}`, "token", "", 413},
		{"outbound replacement", "group", `{"revision":1,"replacement":"warp"}`, "token", "", 200},
		{"group replacement", "group", `{"revision":1,"replacement":"replacement"}`, "token", "http://rillway.test", 200},
		{"unreferenced", "replacement", `{"revision":1}`, "token", "", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := groupDeletionBackend(t)
			before, err := json.Marshal(b.Config())
			if err != nil {
				t.Fatal(err)
			}
			w := request(New(b, "token"), "DELETE", "/api/v1/groups/"+tc.id, tc.body, tc.token, tc.origin)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
			if tc.status != 200 {
				after, err := json.Marshal(b.Config())
				if err != nil || string(after) != string(before) {
					t.Fatal("rejected deletion changed config", err)
				}
				var fields map[string]string
				if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil {
					t.Fatal(err)
				}
				if tc.name == "missing" && fields["error_source"] != config.GroupMissing {
					t.Fatalf("missing error = %v", fields)
				}
				if tc.name == "missing replacement" && fields["error_source"] != config.GroupReplacementRequired {
					t.Fatalf("replacement error = %v", fields)
				}
			} else {
				var returned config.Config
				if err := json.Unmarshal(w.Body.Bytes(), &returned); err != nil {
					t.Fatal(err)
				}
				current := b.Config()
				if current.Revision != 2 || len(current.OutboundGroups) != 2 || config.Validate(current) != nil || !reflect.DeepEqual(returned, current) {
					t.Fatalf("deletion result = %+v", current)
				}
				for _, group := range current.OutboundGroups {
					if group.ID == tc.id {
						t.Fatal("deleted group retained")
					}
				}
				if tc.id == "group" {
					var body struct{ Replacement string }
					if err := json.Unmarshal([]byte(tc.body), &body); err != nil {
						t.Fatal(err)
					}
					if current.DefaultOutbound != body.Replacement || current.Rules[0].Outbound != body.Replacement {
						t.Fatal("references were not replaced")
					}
				} else if current.DefaultOutbound != "group" || current.Rules[0].Outbound != "group" {
					t.Fatal("unreferenced deletion changed routes")
				}
				if current.Rules[1].ID != "unrelated" || current.Rules[1].Outbound != "direct" || len(current.Outbounds) != 2 {
					t.Fatal("deletion changed unrelated rules or outbounds")
				}
			}
			if b.lastAction != "" {
				t.Fatal("group deletion invoked provider action")
			}
		})
	}
}

func TestDeleteGroupApplyFailureIsAtomicAndSafe(t *testing.T) {
	for _, tc := range []struct {
		failure error
		status  int
	}{
		{errors.New("provider-private-output"), 422},
		{config.ErrConflict, 409},
	} {
		b := groupDeletionBackend(t)
		before, err := json.Marshal(b.Config())
		if err != nil {
			t.Fatal(err)
		}
		b.failApply = tc.failure
		w := request(New(b, "token"), "DELETE", "/api/v1/groups/group", `{"revision":1,"replacement":"warp"}`, "token", "")
		after, err := json.Marshal(b.Config())
		if err != nil || w.Code != tc.status || string(before) != string(after) || strings.Contains(w.Body.String(), "provider-private-output") {
			t.Fatalf("failed deletion = %d %s, error %v", w.Code, w.Body.String(), err)
		}
		if tc.status == 422 {
			var fields map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil {
				t.Fatal(err)
			}
			if fields["error_source"] != "Could not delete outbound group. Your previous configuration is unchanged." {
				t.Fatalf("unsafe deletion error = %v", fields)
			}
		}
	}
}

func TestConcurrentGroupDeletesOnlyOneRevisionWins(t *testing.T) {
	b := groupDeletionBackend(t)
	h := New(b, "token")
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			codes <- request(h, "DELETE", "/api/v1/groups/group", `{"revision":1,"replacement":"warp"}`, "token", "").Code
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
			t.Fatalf("unexpected concurrent deletion status %d", code)
		}
	}
	if wins != 1 || conflicts != 1 || b.Config().Revision != 2 {
		t.Fatalf("wins=%d conflicts=%d revision=%d", wins, conflicts, b.Config().Revision)
	}
}
