package engine

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"rillway/internal/config"
	"rillway/internal/hostlocal"
	"rillway/internal/outbound"
	"rillway/internal/routeinfo"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestFinalPolicyAcceptanceControlsSuccessHealth(t *testing.T) {
	for _, tc := range []struct {
		name                                       string
		strict, listener, update, replace, failure bool
	}{
		{name: "strict after numeric connect", strict: true, update: true},
		{name: "own listener after numeric connect", listener: true},
		{name: "allowed success"},
		{name: "allowed success after provider reuse", update: true},
		{name: "old success cannot heal replacement", update: true, replace: true},
		{name: "genuine transport failure", failure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			address := listener.Addr().String()
			_, port, _ := net.SplitHostPort(address)
			if tc.failure {
				_ = listener.Close()
			}
			real, err := outbound.New(t.Context(), config.Outbound{ID: "direct", Type: "direct", Enabled: true, PublicInternet: true})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = real.Close() }()
			type result struct {
				conn net.Conn
				err  error
			}
			connected, returned := make(chan result, 1), make(chan result, 1)
			release := make(chan struct{})
			proceed := sync.OnceFunc(func() { close(release) })
			defer proceed()
			provider := &testProvider{id: "direct", dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				// Use the actual direct numeric guard and verified identity, without
				// DNS. Pause only after connect and before returning to Engine.
				conn, dialErr := real.DialContext(ctx, network, address)
				connected <- result{conn, dialErr}
				select {
				case <-release:
					return conn, dialErr
				case <-ctx.Done():
					if conn != nil {
						_ = conn.Close()
					}
					return nil, ctx.Err()
				}
			}}
			e, now := testEngine()
			cfg := cloneConfig(e.cfg)
			cfg.Outbounds = []config.Outbound{{ID: "direct", Type: "direct", Enabled: true, PublicInternet: true}}
			cfg.Adaptive.Candidates = []string{"direct"}
			cfg.HostLocal = &config.HostLocal{Mode: hostlocal.Open}
			providers := map[string]outbound.Provider{"direct": provider}
			e.Update(cfg, providers)
			policy, err := cfg.CompileHostLocal(nil)
			if err != nil {
				t.Fatal(err)
			}
			state := hostlocal.NewState(policy)
			e.SetHostLocalPolicy(state)
			var refuseListener atomic.Bool
			e.SetDestinationPolicy(func(target string) string {
				if target == address && refuseListener.Load() {
					return "management_endpoint"
				}
				return ""
			})
			seed := healthState{consecutiveFailures: 3, lastFailure: now.Add(-time.Minute), lastSuccess: now.Add(-time.Hour)}
			e.health["direct"] = &seed
			before := seed
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			ctx = routeinfo.With(ctx, routeinfo.Info{SourceIP: netip.MustParseAddr("127.0.0.1"), Inbound: "http"})
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := e.DialContext(ctx, "tcp4", "health.invalid:"+port)
				returned <- result{conn, err}
			}()
			defer func() { proceed(); <-done }()
			var dial result
			select {
			case dial = <-connected:
			case <-ctx.Done():
				t.Fatal("provider did not reach controlled return", ctx.Err())
			}
			if !tc.failure {
				if dial.err != nil || dial.conn == nil {
					t.Fatal("numeric connect under open failed", dial.err)
				}
				defer func() { _ = dial.conn.Close() }()
				peer, err := listener.Accept()
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = peer.Close() }()
			}
			if tc.strict {
				cfg.HostLocal.Mode = hostlocal.Strict
				policy, err = cfg.CompileHostLocal(nil)
				if err != nil {
					t.Fatal(err)
				}
				state.Publish(policy)
			}
			refuseListener.Store(tc.listener)
			if tc.replace {
				providers = map[string]outbound.Provider{"direct": &testProvider{id: "direct"}}
			}
			if tc.update {
				e.Update(cfg, providers)
				if tc.replace {
					e.health["direct"] = &healthState{consecutiveFailures: 5}
					before = *e.health["direct"]
				}
			}
			proceed()
			outcome := <-returned
			<-done
			refused := tc.strict || tc.listener
			if refused {
				if outcome.conn != nil || (tc.strict && !errors.Is(outcome.err, hostlocal.ErrDenied)) || (tc.listener && !errors.Is(outcome.err, errLocalPeer)) {
					t.Fatal("lost final policy refusal", outcome.err)
				}
				if _, err := dial.conn.Write([]byte("closed")); !errors.Is(err, net.ErrClosed) {
					t.Fatal("refused connection was not closed", err)
				}
			} else if tc.failure {
				if outcome.conn != nil || !errors.Is(outcome.err, syscall.ECONNREFUSED) {
					t.Fatal("lost genuine transport failure", outcome.err)
				}
			} else {
				if outcome.err != nil || outcome.conn == nil {
					t.Fatal("allowed success failed", outcome.err)
				}
				_ = outcome.conn.Close()
			}
			got := *e.health["direct"]
			switch {
			case refused || tc.replace:
				if got != before {
					t.Fatalf("policy refusal/old provider altered health: got%+v want%+v", got, before)
				}
			case tc.failure:
				if got.consecutiveFailures != 4 || !got.lastFailure.Equal(*now) || !got.lastSuccess.Equal(before.lastSuccess) {
					t.Fatalf("transport failure did not update health: %+v", got)
				}
			default:
				if got.consecutiveFailures != 0 || !got.lastSuccess.Equal(*now) {
					t.Fatalf("accepted success did not heal health: %+v", got)
				}
			}
			wantFlows, wantDestinations := 1, 1
			if refused || tc.failure {
				wantFlows = 0
			}
			if refused || tc.update {
				wantDestinations = 0
			}
			if len(e.flows) != wantFlows || len(e.destinations) != wantDestinations {
				t.Fatal("unexpected flow/adaptive observations", len(e.flows), len(e.destinations))
			}
			for _, destination := range e.destinations {
				samples := destination.samples["direct"]
				if len(samples) != 1 || samples[0].success == tc.failure {
					t.Fatal("accepted/transport controls lost their adaptive sample", samples)
				}
			}
		})
	}
}
