package control

import (
	"context"
	"errors"
	"testing"
)

type restartBackend struct {
	*fakeBackend
	restarted bool
	err       error
}

func (b *restartBackend) PrepareRestart(context.Context) (func(), error) {
	return func() { b.restarted = true }, b.err
}

func TestRestartRequiresAuthenticationOriginAndSuccessfulPreparation(t *testing.T) {
	b := &restartBackend{fakeBackend: newBackend()}
	h := New(b, "correct")
	for _, tc := range []struct {
		token, origin string
		status        int
	}{
		{status: 401},
		{token: "wrong", status: 401},
		{token: "correct", origin: "https://evil.test", status: 403},
	} {
		w := request(h, "POST", "/api/v1/service/restart", "{}", tc.token, tc.origin)
		if w.Code != tc.status || b.restarted {
			t.Fatalf("unauthorized restart: %d", w.Code)
		}
	}
	b.err = errors.New("private diagnostic")
	w := request(h, "POST", "/api/v1/service/restart", "{}", "correct", "")
	if w.Code != 422 || b.restarted {
		t.Fatal("failed preparation restarted the service")
	}
	b.err = nil
	w = request(h, "POST", "/api/v1/service/restart", "{}", "correct", "")
	if w.Code != 202 || !b.restarted {
		t.Fatal("authenticated restart not acknowledged")
	}
}
