package main

import (
	"bytes"
	"path/filepath"
	"rillway/internal/config"
	"testing"
)

func TestInitDoesNotOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	var out bytes.Buffer
	if err := run(t.Context(), []string{"init", "--config", path}, &out); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Outbounds[1].Enabled {
		t.Fatal("WARP must require explicit enable")
	}
	if err = run(t.Context(), []string{"init", "--config", path}, &out); err == nil {
		t.Fatal("init overwrote existing config")
	}
	if err = run(t.Context(), []string{"pac", "--config", path}, &out); err != nil {
		t.Fatal(err)
	}
}
