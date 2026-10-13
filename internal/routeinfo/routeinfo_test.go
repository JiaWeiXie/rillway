package routeinfo

import (
	"context"
	"net/netip"
	"testing"
)

func TestFromUnsetContext(t *testing.T) {
	if got := From(context.Background()); got != (Info{}) {
		t.Fatalf("unset metadata = %+v, want zero Info", got)
	}
}

func TestWithNormalizesSourceAndPreservesParent(t *testing.T) {
	type contextKey struct{}
	parent, cancel := context.WithCancel(context.WithValue(t.Context(), contextKey{}, "parent"))
	defer cancel()
	for _, tt := range []struct {
		name        string
		input, want netip.Addr
	}{
		{"IPv4", netip.MustParseAddr("203.0.113.7"), netip.MustParseAddr("203.0.113.7")},
		{"mapped IPv4", netip.MustParseAddr("::ffff:203.0.113.7"), netip.MustParseAddr("203.0.113.7")},
		{"IPv6", netip.MustParseAddr("2001:db8::7"), netip.MustParseAddr("2001:db8::7")},
		{"unknown", netip.Addr{}, netip.Addr{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := Info{SourceIP: tt.input, Inbound: "socks5"}
			ctx := With(parent, input)
			if got := From(ctx); got != (Info{SourceIP: tt.want, Inbound: "socks5"}) {
				t.Fatalf("metadata = %+v, want source %v and socks5", got, tt.want)
			}
			if input.SourceIP != tt.input || From(parent) != (Info{}) || ctx.Value(contextKey{}) != "parent" {
				t.Fatal("With changed input, parent metadata, or parent values")
			}
			child := With(ctx, Info{Inbound: "http"})
			if From(child) != (Info{Inbound: "http"}) || From(ctx).Inbound != "socks5" {
				t.Fatal("nested metadata modified its parent")
			}
		})
	}
	ctx := With(parent, Info{Inbound: "http"})
	cancel()
	if ctx.Err() != context.Canceled {
		t.Fatalf("parent cancellation was lost: %v", ctx.Err())
	}
}
