package proxy

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"rillway/internal/hostlocal"
	"rillway/internal/i18n"
	"rillway/internal/routeinfo"
	"strconv"
)

func outboundFailureMessage(ctxLocale i18n.Locale, err error) string {
	var denial *hostlocal.Denial
	if errors.As(err, &denial) {
		return i18n.Message(ctxLocale, denial.Error())
	}
	return "outbound connection failed"
}

func (s *Server) SetHostLocalPolicy(state *hostlocal.State) { s.hostLocal.Store(state) }

func (s *Server) beginHostEvent(ctx context.Context, address string) (context.Context, *hostlocal.Event) {
	if state := s.hostLocal.Load(); state != nil {
		_, port, _ := net.SplitHostPort(address)
		n, _ := strconv.ParseUint(port, 10, 16)
		event := state.Begin(routeinfo.From(ctx).SourceIP, uint16(n))
		return hostlocal.WithEvent(ctx, event), event
	}
	return ctx, nil
}

func (s *Server) checkHost(ctx context.Context, address string, numeric netip.AddrPort) error {
	state := s.hostLocal.Load()
	if state == nil {
		return nil
	}
	p, source := state.Load(), routeinfo.From(ctx).SourceIP
	d := p.CheckAddress(address, source)
	if numeric.IsValid() {
		d = p.CheckResolved(address, numeric, source)
	}
	if d.Denied {
		if !numeric.IsValid() {
			numeric, _ = netip.ParseAddrPort(address)
		}
		hostlocal.EventFrom(ctx).Finalize(numeric, d)
		return &hostlocal.Denial{Reason: d.Reason}
	}
	return nil
}

func (s *Server) recordSelfRefusal(ctx context.Context, address string) {
	dst, _ := netip.ParseAddrPort(address)
	reason := s.DestinationPolicy(address)
	if reason == "" {
		reason = "proxy_listener"
	}
	hostlocal.EventFrom(ctx).Finalize(dst, hostlocal.Decision{Denied: true, HypotheticalDenied: true, HostLocal: true, Reason: reason})
}
