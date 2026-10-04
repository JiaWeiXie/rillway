package platform

import (
	"fmt"
	"net"
	"rillway/internal/config"
	"rillway/internal/i18n"
	"strings"
	"testing"
)

func TestListenerCheckRefusesOccupiedPortAndReleasesEarlierSockets(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = occupied.Close() }()
	first, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := first.Addr().String()
	_ = first.Close()
	c := config.Default(t.TempDir())
	c.Listeners.HTTP = address
	c.Listeners.SOCKS5 = occupied.Addr().String()
	if err = CheckListeners(t.Context(), c); err == nil {
		t.Fatal("accepted occupied port")
	}
	again, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(fmt.Errorf("leaked preflight listener: %w", err))
	}
	_ = again.Close()
}

func TestListenerErrorLocalizesWithoutChangingAddress(t *testing.T) {
	ctx := i18n.WithLocale(t.Context(), i18n.TraditionalChinese)
	c := config.Default(t.TempDir())
	c.Listeners.HTTP = "127.0.0.1:80"
	err := CheckListeners(ctx, c)
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1:80") || !strings.Contains(err.Error(), "服務監聽位址") {
		t.Fatal(err)
	}
}

func TestListenerCheckAllowsOptionalListenersToBeDisabled(t *testing.T) {
	c := config.Default(t.TempDir())
	c.Listeners.HTTP = ""
	c.Listeners.SOCKS5 = ""
	c.Listeners.PAC = ""
	if err := config.Validate(c); err != nil {
		t.Fatal(err)
	}
	if err := CheckListeners(t.Context(), c); err != nil {
		t.Fatal(err)
	}
}
