// Package routeinfo carries optional proxy ingress metadata for routing.
package routeinfo

import (
	"context"
	"net/netip"
)

// Info is optional connection metadata supplied by a proxy listener.
// A zero Info means the source address and inbound are unknown; conditions
// that require them then never match.
type Info struct {
	SourceIP netip.Addr // Unmapped; invalid when unknown.
	Inbound  string     // "http" or "socks5"; empty when unknown.
}

type contextKey struct{}

// With attaches ingress metadata without changing the context's lifetime.
func With(ctx context.Context, info Info) context.Context {
	info.SourceIP = info.SourceIP.WithZone("").Unmap()
	return context.WithValue(ctx, contextKey{}, info)
}

// From returns the ingress metadata, or zero Info when none was supplied.
func From(ctx context.Context) Info {
	info, _ := ctx.Value(contextKey{}).(Info)
	return info
}
