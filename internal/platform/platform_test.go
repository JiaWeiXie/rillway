package platform

import (
	"rillway/internal/config"
	"strings"
	"testing"
)

func TestPACAndACL(t *testing.T) {
	c := config.Default(t.TempDir())
	p, err := PAC(c.PAC)
	if err != nil || !strings.Contains(p, "PROXY 127.0.0.1:17890") {
		t.Fatal(err, p)
	}
	if strings.Contains(p, "17890; DIRECT") {
		t.Fatal("unsafe fallback")
	}
	for _, tc := range []struct {
		addr string
		ok   bool
	}{{"127.0.0.1:22", true}, {"[::1]:22", true}, {"192.168.1.2:22", false}, {"bad", false}} {
		if ClientAllowed(tc.addr, c.Security.AllowedClients) != tc.ok {
			t.Fatal(tc)
		}
	}
	c.PAC.BypassCIDRs = []string{"garbage"}
	if _, err := PAC(c.PAC); err == nil {
		t.Fatal("invalid CIDR accepted")
	}
}

func TestCredentialsStable(t *testing.T) {
	c := config.Default(t.TempDir())
	token, fp, err := EnsureCredentials(c)
	if err != nil {
		t.Fatal(err)
	}
	b, bfp, err := EnsureCredentials(c)
	if err != nil || token != b || fp != bfp {
		t.Fatal("credentials changed", err)
	}
	if len(token) != 64 || len(fp) != 64 {
		t.Fatal("bad credential lengths")
	}
}
