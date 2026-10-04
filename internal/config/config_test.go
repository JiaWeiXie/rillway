package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTripAndPermissions(t *testing.T) {
	c := Default(t.TempDir())
	p := filepath.Join(t.TempDir(), "config.json")
	if err := Save(p, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil || got.Revision != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatal(st.Mode())
	}
}

func TestInvalidConfig(t *testing.T) {
	for _, modify := range []func(*Config){func(c *Config) { c.Rules[0].Domains = []string{"github.com/evil"} }, func(c *Config) { c.Outbounds = append(c.Outbounds, c.Outbounds[0]) }, func(c *Config) { c.Adaptive.ProbesPerMinute = 13 }, func(c *Config) { c.Rules[0].Outbound = "missing" }, func(c *Config) { c.Listeners.Admin = c.Listeners.HTTP }} {
		c := Default(t.TempDir())
		modify(&c)
		if Validate(c) == nil {
			t.Fatal("accepted invalid config")
		}
	}
}

func TestDecodeRejectsUnknownAndTrailing(t *testing.T) {
	c := Default(t.TempDir())
	data, _ := json.Marshal(c)
	for _, b := range [][]byte{append(append([]byte{}, data...), []byte(" {}")...), append([]byte(`{"unexpected":1,`), data[1:]...)} {
		if _, err := Decode(b); err == nil {
			t.Fatal("accepted extra JSON")
		}
	}
}

func FuzzDecode(f *testing.F) {
	seed, _ := json.Marshal(Default("state"))
	f.Add(seed)
	f.Fuzz(func(t *testing.T, b []byte) {
		c, err := Decode(b)
		if err == nil && Validate(c) != nil {
			t.Fatal("decoded invalid config")
		}
	})
}
