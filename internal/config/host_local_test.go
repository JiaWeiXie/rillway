package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"rillway/internal/hostlocal"
	"testing"
)

func TestHostLocalEveryConfigVersionPreservesEffectiveMode(t *testing.T) {
	for _, version := range []int{1, 2, 3} {
		for _, mode := range []string{"absent", hostlocal.Open, hostlocal.Strict} {
			t.Run(fmt.Sprintf("v%d/%s", version, mode), func(t *testing.T) {
				c := Default(t.TempDir())
				c.HostLocal = nil
				if mode != "absent" {
					c.HostLocal = &HostLocal{Mode: mode}
				}
				body, err := json.Marshal(c)
				if err != nil {
					t.Fatal(err)
				}
				var raw map[string]json.RawMessage
				if err := json.Unmarshal(body, &raw); err != nil {
					t.Fatal(err)
				}
				raw["version"] = json.RawMessage(fmt.Sprint(version))
				if version < 3 {
					delete(raw, "outbound_groups")
					delete(raw, "rule_sets")
				}
				if version == 1 {
					delete(raw, "source_access")
				}
				body, err = json.Marshal(raw)
				if err != nil {
					t.Fatal(err)
				}
				decoded, err := Decode(body)
				if err != nil {
					t.Fatal(err)
				}
				want := mode
				if mode == "absent" {
					want = hostlocal.LocalClientsOnly
				}
				if decoded.HostLocalMode() != want {
					t.Fatalf("mode=%s want=%s", decoded.HostLocalMode(), want)
				}
				p, err := decoded.CompileHostLocal(nil)
				if err != nil {
					t.Fatal(err)
				}
				got := p.CheckIP(netip.MustParseAddrPort("127.0.0.1:3000"), netip.MustParseAddr("192.0.2.2"))
				if got.Denied != (mode != hostlocal.Open) {
					t.Fatal("upgrade lost protection or explicit rollback", got)
				}
			})
		}
	}
}

func TestHostLocalAbsentProtectsWithoutRewriting(t *testing.T) {
	c := Default(t.TempDir())
	if c.HostLocal == nil || c.HostLocal.Mode != hostlocal.LocalClientsOnly {
		t.Fatal("setup default not explicit")
	}
	c.HostLocal = nil
	c.Revision = 17
	body, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "legacy.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := loaded.CompileHostLocal(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !p.CheckIP(netip.MustParseAddrPort("127.0.0.1:3000"), netip.MustParseAddr("192.0.2.9")).Denied {
		t.Fatal("absent mode preserved old external access")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(body, after) || loaded.Revision != 17 || loaded.HostLocal != nil {
		t.Fatal("load rewrote legacy configuration", err)
	}
	loaded.HostLocal = &HostLocal{Mode: hostlocal.Open}
	if err := Save(path, loaded); err != nil {
		t.Fatal(err)
	}
	loaded, err = Load(path)
	if err != nil || loaded.HostLocalMode() != hostlocal.Open || loaded.Revision != 17 {
		t.Fatal("explicit open lost", err)
	}
	p, err = loaded.CompileHostLocal(nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.CheckIP(netip.MustParseAddrPort("127.0.0.1:3000"), netip.Addr{}).Denied {
		t.Fatal("open did not restore ordinary access")
	}
	if !p.CheckIP(netip.MustParseAddrPort("169.254.169.254:80"), netip.Addr{}).Denied {
		t.Fatal("open disabled baseline")
	}
}

func TestHostLocalValidation(t *testing.T) {
	for _, entry := range []HostLocalRule{
		{To: "localhost:3000"},
		{To: "*:3000"},
		{To: "127.0.0.1:0"},
		{To: "127.0.0.1:3001-3000"},
		{To: "127.0.0.1:65536"},
		{To: "127.0.0.1:3000", From: []string{}},
		{To: "127.0.0.1:3000", From: []string{"invalid"}},
		{To: "127.0.0.1:17892"},
		{To: "[::ffff:127.0.0.1]:17890-17893"},
	} {
		c := Default(t.TempDir())
		c.HostLocal = &HostLocal{Mode: hostlocal.Strict, Allow: []HostLocalRule{entry}}
		if Validate(c) == nil {
			t.Fatal("accepted invalid exception", entry)
		}
	}
	c := Default(t.TempDir())
	c.HostLocal = &HostLocal{Mode: "typo"}
	if Validate(c) == nil {
		t.Fatal("unknown mode accepted")
	}
	for _, listener := range []string{"0.0.0.0:17892", "[::]:17892", ":17892"} {
		c.Listeners.Admin = listener
		c.HostLocal = &HostLocal{Allow: []HostLocalRule{{To: "127.0.0.2:17892"}}}
		if Validate(c) == nil {
			t.Fatal("wildcard listener exception accepted", listener)
		}
		c.HostLocal.Allow[0].To = "192.0.2.10:17892"
		if _, err := c.CompileHostLocal([]netip.Addr{netip.MustParseAddr("192.0.2.10")}); err == nil {
			t.Fatal("own interface listener exception accepted")
		}
	}
	c = Default(t.TempDir())
	c.HostLocal = &HostLocal{Mode: hostlocal.Strict, Allow: []HostLocalRule{{To: "[fe80::1%eth0]:3000-3002", From: []string{"192.0.2.0/24"}}}}
	if err := Validate(c); err != nil {
		t.Fatal("valid range/source rejected", err)
	}
	if len(c.HostLocalWarnings()) != 2 {
		t.Fatal("expected metadata and range warnings, without a broad-source warning")
	}
	c.HostLocal.Allow[0].From = nil
	if len(c.HostLocalWarnings()) != 3 {
		t.Fatal("missing source warning")
	}
	c.HostLocal.Allow[0].From = []string{"::/0"}
	if len(c.HostLocalWarnings()) != 3 {
		t.Fatal("broad source warning missing")
	}
}
