package app

import (
	"path/filepath"
	"rillway/internal/config"
	"testing"
)

func TestHostLocalApplyPublishesOnlyAfterSave(t *testing.T) {
	c := config.Default(t.TempDir())
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	r, err := New(t.Context(), path, c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	original := r.hostLocal.Load()
	next := r.Config()
	next.HostLocal = &config.HostLocal{Mode: "strict"}
	r.path = t.TempDir() // rename onto an existing directory must fail
	if err := r.Apply(t.Context(), next); err == nil {
		t.Fatal("failed-save fixture did not fail")
	}
	if r.hostLocal.Load() != original || r.Config().Revision != c.Revision || r.Config().HostLocalMode() != "local-clients-only" {
		t.Fatal("failed save published new policy")
	}
	r.path = path
	if err := r.Apply(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	if r.hostLocal.Load() == original || r.hostLocal.Load().Mode() != "strict" || r.Config().Revision != c.Revision+1 {
		t.Fatal("successful save did not publish")
	}
	saved, err := config.Load(path)
	if err != nil || saved.HostLocalMode() != "strict" {
		t.Fatal("policy publication not backed by saved config", err)
	}
}
