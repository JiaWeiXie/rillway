package scripts

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillPackageIsDeterministicAndRejectsUnsafeFiles(t *testing.T) {
	root := t.TempDir()
	script, err := filepath.Abs("package-skill.go")
	if err != nil {
		t.Fatal(err)
	}
	members := []string{"SKILL.md", "agents/openai.yaml", "references/agent-cli.md", "references/installation.md", "references/configuration.md", "references/troubleshooting.md"}
	for _, member := range members {
		path := filepath.Join(root, "skills/rillway-ops", member)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("synthetic public skill\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "LICENSE"), []byte("synthetic license\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(root, "skills/rillway-ops/private-state.json")
	if err := os.WriteFile(extra, []byte("secret-never-ship"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func() []byte {
		t.Helper()
		command := exec.Command("go", "run", script)
		command.Dir = root
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("package: %v %s", err, out)
		}
		data, err := os.ReadFile(filepath.Join(root, "dist/rillway-ops.zip"))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	first := run()
	if !bytes.Equal(first, run()) {
		t.Fatal("skill archive contains changing timestamps or metadata")
	}
	reader, err := zip.NewReader(bytes.NewReader(first), int64(len(first)))
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.File) != 7 {
		t.Fatal("archive did not use the exact file allowlist")
	}
	for _, f := range reader.File {
		if strings.Contains(f.Name, "private") || !strings.HasPrefix(f.Name, "rillway-ops/") {
			t.Fatal("unexpected archive member", f.Name)
		}
		body, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(body)
		_ = body.Close()
		if err != nil || bytes.Contains(data, []byte("secret-never-ship")) {
			t.Fatal("private workspace data leaked")
		}
	}
	path := filepath.Join(root, "skills/rillway-ops/references/configuration.md")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(extra, path); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "run", script)
	command.Dir = root
	if err := command.Run(); err == nil {
		t.Fatal("a symbolic link to private state was packaged")
	}
}
