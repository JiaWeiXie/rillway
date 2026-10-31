package engine

import (
	"context"
	"net"
	"net/netip"
	"rillway/internal/hostlocal"
	"rillway/internal/outbound"
	"rillway/internal/routeinfo"
)

// The separate listener callback still protects provider transport peers. This
// state evaluates only requested destinations and direct numeric candidates.
func (e *Engine) SetHostLocalPolicy(state *hostlocal.State) { e.hostLocal.Store(state) }

func refuseHost(ctx context.Context, dst netip.AddrPort, d hostlocal.Decision) error {
	hostlocal.EventFrom(ctx).Finalize(dst, d)
	return &hostlocal.Denial{Reason: d.Reason}
}

func (e *Engine) checkHostRequest(ctx context.Context, address string) error {
	if state := e.hostLocal.Load(); state != nil {
		d := state.Load().CheckAddress(address, routeinfo.From(ctx).SourceIP)
		if d.Denied {
			dst, _ := netip.ParseAddrPort(address)
			return refuseHost(ctx, dst, d)
		}
	}
	return nil
}

func (e *Engine) hostDialContext(ctx context.Context, address, providerType string, countListener bool) (context.Context, error) {
	state := e.hostLocal.Load()
	if state == nil {
		return ctx, nil
	}
	source := routeinfo.From(ctx).SourceIP
	if providerType != "direct" {
		d := state.Load().CheckRemoteName(address, source)
		if d.Denied {
			dst, _ := netip.ParseAddrPort(address)
			return ctx, refuseHost(ctx, dst, d)
		}
		return ctx, nil
	}
	return outbound.WithResolvedGuard(ctx, func(_ string, dst netip.AddrPort) error {
		// Never apply host-local policy to WARP's own transport connection.
		e.mu.Lock()
		listeners := e.destinationPolicy
		e.mu.Unlock()
		if listeners != nil {
			if reason := listeners(dst.String()); reason != "" {
				d := hostlocal.Decision{Denied: true, HypotheticalDenied: true, HostLocal: true, Reason: reason}
				hostlocal.EventFrom(ctx).ObserveCandidate(dst, d)
				if countListener {
					e.countManagementRefusal(reason)
				}
				return &hostlocal.Denial{Reason: reason}
			}
		}
		d := state.Load().CheckResolved(address, dst, source)
		hostlocal.EventFrom(ctx).ObserveCandidate(dst, d)
		if d.Denied {
			return &hostlocal.Denial{Reason: d.Reason}
		}
		return nil
	}), nil
}

func (e *Engine) dialProbe(ctx context.Context, job probeJob) (net.Conn, error) {
	e.mu.Lock()
	// Update replaces the provider map and advances generation together. Do not
	// compare Provider interface values: valid providers may contain functions.
	current := e.generation == job.generation && e.providers[job.id] != nil
	listeners := e.destinationPolicy
	e.mu.Unlock()
	if !current {
		return nil, context.Canceled
	}
	if listeners != nil {
		if reason := listeners(job.address); reason != "" {
			return nil, &hostlocal.Denial{Reason: reason}
		}
	}
	ctx = routeinfo.With(ctx, routeinfo.Info{SourceIP: job.source, Inbound: job.inbound})
	if err := e.checkHostRequest(ctx, job.address); err != nil {
		return nil, err
	}
	ctx, err := e.hostDialContext(ctx, job.address, job.providerType, false)
	if err != nil {
		return nil, err
	}
	return job.provider.DialContext(ctx, job.network, job.address)
}

// hostResult uses only a snapshotted direct provider's verified DestinationIP.
// An upstream transport RemoteAddr is never substituted for a missing identity.
func (e *Engine) hostResult(ctx context.Context, address, providerType string, conn net.Conn) (netip.AddrPort, error) {
	state := e.hostLocal.Load()
	if state == nil {
		return netip.AddrPort{}, nil
	}
	var dst netip.AddrPort
	if providerType == "direct" {
		if identity, ok := conn.(interface{ DestinationIP() string }); ok {
			_, port, _ := net.SplitHostPort(address)
			dst, _ = netip.ParseAddrPort(net.JoinHostPort(identity.DestinationIP(), port))
		}
	}
	source := routeinfo.From(ctx).SourceIP
	d := state.Load().CheckRemoteName(address, source)
	if dst.IsValid() {
		d = state.Load().CheckResolved(address, dst, source)
	}
	if d.Denied {
		return dst, refuseHost(ctx, dst, d)
	}
	hostlocal.EventFrom(ctx).Finalize(dst, d)
	return dst, nil
}
