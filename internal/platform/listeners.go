package platform

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"rillway/internal/config"
	"rillway/internal/i18n"
)

// CheckListeners verifies bindability before a first installation creates files.
// It cannot reserve ports after returning; the daemon performs the final bind.
func CheckListeners(ctx context.Context, c config.Config) error {
	var listeners []net.Listener
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()
	for _, address := range []string{c.Listeners.HTTP, c.Listeners.SOCKS5, c.Listeners.Admin, c.Listeners.PAC} {
		if address == "" {
			continue
		}
		parsed, err := netip.ParseAddrPort(address)
		if err != nil || parsed.Port() < 1024 {
			return fmt.Errorf(i18n.Message(i18n.FromContext(ctx), "service listener %s must use a literal IP and a port between 1024 and 65535"), address)
		}
		listener, err := net.Listen("tcp", address)
		if err != nil {
			return fmt.Errorf(i18n.Message(i18n.FromContext(ctx), "listener %s is unavailable: %w"), address, err)
		}
		listeners = append(listeners, listener)
	}
	return nil
}
