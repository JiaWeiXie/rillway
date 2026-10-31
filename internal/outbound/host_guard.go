package outbound

import (
	"context"
	"errors"
	"net/netip"
	"syscall"
)

// ResolvedGuard checks a direct provider's numeric candidate before connect.
// Other providers own their transport sockets and must not consume this hook.
type ResolvedGuard func(network string, destination netip.AddrPort) error

type resolvedGuardKey struct{}

func WithResolvedGuard(ctx context.Context, guard ResolvedGuard) context.Context {
	return context.WithValue(ctx, resolvedGuardKey{}, guard)
}

func directControl(ctx context.Context) func(context.Context, string, string, syscall.RawConn) error {
	guard, _ := ctx.Value(resolvedGuardKey{}).(ResolvedGuard)
	if guard == nil {
		return nil
	}
	return func(ctx context.Context, network, address string, _ syscall.RawConn) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		dst, err := netip.ParseAddrPort(address)
		if err != nil {
			return errors.New("direct candidate has no numeric destination")
		}
		dst = netip.AddrPortFrom(dst.Addr().WithZone("").Unmap(), dst.Port())
		return guard(network, dst)
	}
}
