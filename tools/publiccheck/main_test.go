package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublishablePathsAndData(t *testing.T) {
	for _, path := range []string{".local/backup.json", ".env", "nested/admin.token", "state/admin.key", "state/admin.crt", "state/private.pem", "mise.local.toml", "dist/rillway-linux-amd64"} {
		if !privatePath(path) {
			t.Fatal("private path allowed", path)
		}
	}
	for _, path := range []string{"examples/config.json", "examples/wireguard.conf", ".env.example", ".github/workflows/ci.yml"} {
		if privatePath(path) {
			t.Fatal("public example blocked", path)
		}
	}
	personal := []byte("/" + "Users" + "/" + "sample-operator/config.json")
	if !contentPrivate(personal, nil) {
		t.Fatal("home path allowed")
	}
	if !contentPrivate([]byte("server-a.internal"), []string{"SERVER-A.INTERNAL"}) {
		t.Fatal("private denylist ignored")
	}
	if contentPrivate([]byte("192.0.2.20 proxy-vm.example.invalid"), nil) {
		t.Fatal("documentation example blocked")
	}
}

func TestAuditFindsDeletedHistoricalPrivateFile(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.name", "Test Contributor"}, {"config", "user.email", "test@example.com"}} {
		if _, err := git(args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile("example.txt", []byte("synthetic public example"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := git("add", "example.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := git("commit", "-qm", "test: public example"); err != nil {
		t.Fatal(err)
	}
	if err := audit(); err != nil {
		t.Fatal("clean source rejected", err)
	}
	if err := os.WriteFile("admin.token", []byte("synthetic fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := git("add", "admin.token"); err != nil {
		t.Fatal(err)
	}
	if _, err := git("commit", "-qm", "test: private fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := git("rm", "admin.token"); err != nil {
		t.Fatal(err)
	}
	if _, err := git("commit", "-qm", "test: delete fixture"); err != nil {
		t.Fatal(err)
	}
	if err := audit(); err == nil {
		t.Fatal("deleted historical private file was missed")
	}
}

func TestAuditChecksLocalDenylistAndTagMetadata(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.name", "Test Contributor"}, {"config", "user.email", "test@example.com"}} {
		if _, err := git(args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile("example.txt", []byte("private-service.test"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := git("add", "example.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := git("commit", "-qm", "test: synthetic host"); err != nil {
		t.Fatal(err)
	}
	deny := filepath.Join(t.TempDir(), "denylist")
	if err := os.WriteFile(deny, []byte("PRIVATE-SERVICE.TEST\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RILLWAY_PRIVACY_PATTERNS", deny)
	if err := audit(); err == nil {
		t.Fatal("operator-specific private data allowed")
	}
	t.Setenv("RILLWAY_PRIVACY_PATTERNS", "")
	if _, err := git("-c", "user.email=private@operator.test", "tag", "-a", "v0.1.0", "-m", "synthetic tag"); err != nil {
		t.Fatal(err)
	}
	if err := audit(); err == nil {
		t.Fatal("private tagger email allowed")
	}
}
