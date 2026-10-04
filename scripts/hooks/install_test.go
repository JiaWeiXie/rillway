package hooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func (f *hookFixture) install(t *testing.T) (string, error) {
	t.Helper()
	cmd := exec.Command(filepath.Join(f.bin, "sh"), filepath.Join(f.root, "scripts", "hooks", "install.sh"))
	cmd.Dir = f.root
	cmd.Env = f.env
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func (f *hookFixture) localConfig(t *testing.T) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(f.root, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestHookInstallerUsesRepoLocalConfiguration(t *testing.T) {
	f := newHookFixture(t)
	output, err := f.install(t)
	if err != nil {
		t.Fatalf("install failed: %v\n%s", err, output)
	}
	if value := strings.TrimSpace(f.git(t, "config", "--local", "--get", "core.hooksPath")); value != ".githooks" {
		t.Fatalf("wrong repository hook path: %q", value)
	}
	origin := f.git(t, "config", "--show-origin", "--get", "core.hooksPath")
	if !strings.Contains(origin, ".git/config") || !strings.Contains(origin, ".githooks") {
		t.Fatalf("hook path was not repo-local: %s", origin)
	}
}

func TestHookInstallerIsIdempotent(t *testing.T) {
	f := newHookFixture(t)
	if output, err := f.install(t); err != nil {
		t.Fatalf("first install: %v\n%s", err, output)
	}
	before := f.localConfig(t)
	if output, err := f.install(t); err != nil {
		t.Fatalf("second install: %v\n%s", err, output)
	}
	if before != f.localConfig(t) {
		t.Fatal("repeated install changed local Git configuration")
	}
}

func TestHookInstallerPreservesExistingManager(t *testing.T) {
	f := newHookFixture(t)
	f.git(t, "config", "--local", "core.hooksPath", "/existing hook manager")
	before := f.localConfig(t)
	output, err := f.install(t)
	if err == nil || !strings.Contains(output, "Existing core.hooksPath") {
		t.Fatalf("existing manager was not rejected: %v\n%s", err, output)
	}
	if before != f.localConfig(t) {
		t.Fatal("installer replaced an existing hook manager")
	}
}

func TestHookInstallerPreservesDefaultActiveHook(t *testing.T) {
	f := newHookFixture(t)
	path := filepath.Join(f.root, ".git", "hooks", "pre-commit")
	want := "#!/bin/sh\nprintf original-hook\n"
	f.write(t, ".git/hooks/pre-commit", want)
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	before := f.localConfig(t)
	output, err := f.install(t)
	if err == nil || !strings.Contains(output, "existing pre-commit hook") {
		t.Fatalf("default active hook was not preserved: %v\n%s", err, output)
	}
	if before != f.localConfig(t) {
		t.Fatal("installer bypassed the existing default hook")
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != want {
		t.Fatalf("existing hook was modified: %q %v", content, err)
	}
	afterInfo, err := os.Stat(path)
	if err != nil || afterInfo.Mode() != beforeInfo.Mode() {
		t.Fatalf("existing hook permissions changed: %v", err)
	}
}

func TestHookInstallerRequiresExecutableEntry(t *testing.T) {
	f := newHookFixture(t)
	if err := os.Chmod(filepath.Join(f.root, ".githooks", "pre-commit"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := f.localConfig(t)
	output, err := f.install(t)
	if err == nil || !strings.Contains(output, "Missing executable .githooks/pre-commit") {
		t.Fatalf("non-executable hook was installed: %v\n%s", err, output)
	}
	if before != f.localConfig(t) {
		t.Fatal("failed install changed Git configuration")
	}
}

func TestHookInstallerRequiresExecutableRunner(t *testing.T) {
	for _, missing := range []bool{false, true} {
		name := "not-executable"
		if missing {
			name = "missing"
		}
		t.Run(name, func(t *testing.T) {
			f := newHookFixture(t)
			path := filepath.Join(f.root, "scripts", "hooks", "pre-commit.sh")
			if missing {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Chmod(path, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before := f.localConfig(t)
			output, err := f.install(t)
			if err == nil || !strings.Contains(output, "Missing executable scripts/hooks/pre-commit.sh") {
				t.Fatalf("unusable runner was installed: %v\n%s", err, output)
			}
			if before != f.localConfig(t) {
				t.Fatal("failed runner verification changed Git configuration")
			}
		})
	}
}

func TestHookInstallerPreservesOtherActiveHooks(t *testing.T) {
	for _, name := range []string{"commit-msg", "pre-push", "post-commit"} {
		t.Run(name, func(t *testing.T) {
			f := newHookFixture(t)
			path := filepath.Join(f.root, ".git", "hooks", name)
			wanted := []byte("#!/bin/sh\nprintf original\n")
			if err := os.WriteFile(path, wanted, 0o700); err != nil {
				t.Fatal(err)
			}
			before := f.localConfig(t)
			output, err := f.install(t)
			if err == nil || !strings.Contains(output, "existing "+name+" hook") {
				t.Fatalf("existing hook ignored: %v\n%s", err, output)
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil || string(after) != string(wanted) || f.localConfig(t) != before {
				t.Fatal("installer modified/bypassed another hook")
			}
		})
	}
}

func TestHookInstallerRequiresCommitMessageFiles(t *testing.T) {
	for _, path := range []string{".githooks/commit-msg", "scripts/hooks/commit-msg.sh", "scripts/hooks/commit-cliff.toml"} {
		t.Run(path, func(t *testing.T) {
			f := newHookFixture(t)
			if err := os.Remove(filepath.Join(f.root, path)); err != nil {
				t.Fatal(err)
			}
			before := f.localConfig(t)
			output, err := f.install(t)
			if err == nil || !strings.Contains(output, path) {
				t.Fatalf("incomplete commit-msg hook installed: %v\n%s", err, output)
			}
			if f.localConfig(t) != before {
				t.Fatal("failed install changed local configuration")
			}
		})
	}
}
