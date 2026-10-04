package hooks

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests use isolated repositories and fake toolchains. Running go test
// ./... from the real pre-commit hook therefore cannot invoke itself recursively.
type hookFixture struct {
	root, bin, scratch, log string
	env                     []string
}

func newHookFixture(t *testing.T) *hookFixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "repository with spaces")
	f := &hookFixture{root: root, bin: filepath.Join(root, "fake-bin"), scratch: filepath.Join(root, "scratch"), log: filepath.Join(root, "commands.log")}
	for _, dir := range []string{f.bin, f.scratch, filepath.Join(root, ".githooks"), filepath.Join(root, "scripts", "hooks")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, tool := range []string{"git", "bash", "sh", "dirname", "mktemp", "mkdir", "rm"} {
		path, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("hook test requires %s: %v", tool, err)
		}
		if err = os.Symlink(path, filepath.Join(f.bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []struct{ source, destination string }{{filepath.Join(cwd, "pre-commit.sh"), filepath.Join(root, "scripts", "hooks", "pre-commit.sh")}, {filepath.Join(cwd, "install.sh"), filepath.Join(root, "scripts", "hooks", "install.sh")}, {filepath.Join(cwd, "..", "..", ".githooks", "pre-commit"), filepath.Join(root, ".githooks", "pre-commit")}} {
		content, err := os.ReadFile(file.source)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(file.destination, content, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if key == "PATH" || key == "TMPDIR" || strings.HasPrefix(key, "GIT_") || strings.HasPrefix(key, "HOOK_TEST_") {
			continue
		}
		f.env = append(f.env, item)
	}
	f.env = append(f.env, "PATH="+f.bin, "TMPDIR="+f.scratch, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "HOOK_TEST_REPO="+root, "HOOK_TEST_LOG="+f.log, "HOOK_TEST_EXPECTED=package staged", "HOOK_TEST_STAGED_PATH=main.go")
	f.writeTool(t, "mise", fakeMise)
	f.writeTool(t, "go", fakeCheckedTool)
	f.writeTool(t, "golangci-lint", fakeCheckedTool)
	f.git(t, "init", "--initial-branch=main")
	return f
}

func (f *hookFixture) writeTool(t *testing.T, name, source string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.bin, name), []byte(source), 0o700); err != nil {
		t.Fatal(err)
	}
}

func (f *hookFixture) write(t *testing.T, path, contents string) {
	t.Helper()
	name := filepath.Join(f.root, path)
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *hookFixture) git(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command(filepath.Join(f.bin, "git"), append([]string{"-C", f.root}, args...)...)
	cmd.Env = f.env
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func (f *hookFixture) run(t *testing.T) (string, error) {
	t.Helper()
	cmd := exec.Command(filepath.Join(f.root, ".githooks", "pre-commit"))
	cmd.Dir = f.root
	cmd.Env = f.env
	output, err := cmd.CombinedOutput()
	entries, readErr := os.ReadDir(f.scratch)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("hook left temporary files after exit: %v\n%s", entries, output)
	}
	return string(output), err
}

func (f *hookFixture) commands(t *testing.T) string {
	t.Helper()
	content, err := os.ReadFile(f.log)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestPreCommitUsesOnlyStagedSnapshot(t *testing.T) {
	f := newHookFixture(t)
	f.env = append(f.env, "GOFLAGS=-tags=live", "RILLWAY_LIVE_CONFIG=/not/a/real/live/config.json")
	f.write(t, "main.go", "package staged\n")
	f.git(t, "add", "--", "main.go")
	f.write(t, "main.go", "package unstaged\n")
	f.write(t, "untracked.go", "not staged\n")
	f.write(t, "secrets/token", "not a real token\n")
	f.write(t, ".local/runtime", "private local state\n")
	before := f.git(t, "ls-files", "--stage", "-z")
	output, err := f.run(t)
	if err != nil {
		t.Fatalf("hook failed: %v\n%s", err, output)
	}
	if after := f.git(t, "ls-files", "--stage", "-z"); before != after {
		t.Fatal("hook changed the index")
	}
	content, err := os.ReadFile(filepath.Join(f.root, "main.go"))
	if err != nil || string(content) != "package unstaged\n" {
		t.Fatalf("hook changed working tree: %q %v", content, err)
	}
	commands := f.commands(t)
	if !strings.Contains(commands, "mise|"+f.root+"|") || !strings.Contains(commands, "golangci-lint|") || !strings.Contains(commands, "|run --allow-serial-runners ./...") || !strings.Contains(commands, "go|") || !strings.Contains(commands, "|test ./...") {
		t.Fatalf("missing checks or wrong mise cwd:\n%s", commands)
	}
	if strings.Contains(commands, "-race") || strings.Contains(commands, "-tags=live") {
		t.Fatalf("unexpected expensive/live test:\n%s", commands)
	}
}

func TestPreCommitDocsOnlyDoesNotRequireMise(t *testing.T) {
	f := newHookFixture(t)
	if err := os.Remove(filepath.Join(f.bin, "mise")); err != nil {
		t.Fatal(err)
	}
	f.write(t, "docs/notes with spaces.md", "notes\n")
	f.git(t, "add", "--", "docs/notes with spaces.md")
	output, err := f.run(t)
	if err != nil || !strings.Contains(output, "略過 Go 檢查") {
		t.Fatalf("docs-only check: %v\n%s", err, output)
	}
	if commands := f.commands(t); commands != "" {
		t.Fatalf("docs-only ran tools: %s", commands)
	}
}

func TestPreCommitChecksEmbeddedAssetsAndFixtures(t *testing.T) {
	f := newHookFixture(t)
	path := "internal/control/web/help.md"
	f.env = append(f.env, "HOOK_TEST_STAGED_PATH="+path)
	f.write(t, path, "package staged\n")
	f.git(t, "add", "--", path)
	if output, err := f.run(t); err != nil {
		t.Fatalf("embedded asset checks: %v\n%s", err, output)
	}
	if commands := f.commands(t); !strings.Contains(commands, "golangci-lint|") || !strings.Contains(commands, "go|") {
		t.Fatalf("embedded/test asset was mistaken for standalone documentation: %s", commands)
	}
}

func TestPreCommitChecksHookScriptChanges(t *testing.T) {
	for _, path := range []string{"scripts/hooks/custom.sh", ".githooks/pre-commit"} {
		t.Run(path, func(t *testing.T) {
			f := newHookFixture(t)
			f.env = append(f.env, "HOOK_TEST_STAGED_PATH="+path)
			if path == ".githooks/pre-commit" {
				f.env = append(f.env, "HOOK_TEST_EXPECTED=#!/bin/sh")
			} else {
				f.write(t, path, "package staged\n")
			}
			f.git(t, "add", "--", path)
			if output, err := f.run(t); err != nil {
				t.Fatalf("hook script checks: %v\n%s", err, output)
			}
			if commands := f.commands(t); !strings.Contains(commands, "golangci-lint|") || !strings.Contains(commands, "go|") {
				t.Fatalf("hook script was not checked: %s", commands)
			}
		})
	}
}

func TestPreCommitReportsMissingMise(t *testing.T) {
	f := newHookFixture(t)
	if err := os.Remove(filepath.Join(f.bin, "mise")); err != nil {
		t.Fatal(err)
	}
	f.write(t, "main.go", "package staged\n")
	f.git(t, "add", "--", "main.go")
	output, err := f.run(t)
	if err == nil || !strings.Contains(output, "找不到 mise") || !strings.Contains(output, "mise install") {
		t.Fatalf("missing mise error: %v\n%s", err, output)
	}
}

func TestPreCommitReportsMissingGoTools(t *testing.T) {
	for _, tool := range []string{"go", "golangci-lint"} {
		t.Run(tool, func(t *testing.T) {
			f := newHookFixture(t)
			if err := os.Remove(filepath.Join(f.bin, tool)); err != nil {
				t.Fatal(err)
			}
			f.write(t, "main.go", "package staged\n")
			f.git(t, "add", "--", "main.go")
			output, err := f.run(t)
			if err == nil || !strings.Contains(output, "mise 環境找不到 "+tool) || !strings.Contains(output, "mise install") {
				t.Fatalf("missing tool error: %v\n%s", err, output)
			}
		})
	}
}

func TestPreCommitToolFailureStopsCommitAndCleansUp(t *testing.T) {
	for _, tool := range []string{"LINT", "GO"} {
		t.Run(tool, func(t *testing.T) {
			f := newHookFixture(t)
			f.env = append(f.env, "HOOK_TEST_"+tool+"_EXIT=23")
			f.write(t, "main.go", "package staged\n")
			f.git(t, "add", "--", "main.go")
			output, err := f.run(t)
			if err == nil || !strings.Contains(output, "檢查未通過") {
				t.Fatalf("tool failure was ignored: %v\n%s", err, output)
			}
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 23 {
				t.Fatalf("tool exit status was lost: %v\n%s", err, output)
			}
			if tool == "LINT" && strings.Contains(f.commands(t), "go|") {
				t.Fatal("tests ran after lint failed")
			}
		})
	}
}

func TestPreCommitHandlesUnusualGoFilename(t *testing.T) {
	f := newHookFixture(t)
	path := "source with\ttab\nand newline.go"
	f.env = append(f.env, "HOOK_TEST_STAGED_PATH="+path)
	f.write(t, path, "package staged\n")
	f.git(t, "add", "--", path)
	if output, err := f.run(t); err != nil {
		t.Fatalf("NUL-delimited filename handling: %v\n%s", err, output)
	}
}

func TestPreCommitUsesRTKWhenAvailable(t *testing.T) {
	f := newHookFixture(t)
	f.writeTool(t, "rtk", fakeRTK)
	f.write(t, "main.go", "package staged\n")
	f.git(t, "add", "--", "main.go")
	if output, err := f.run(t); err != nil {
		t.Fatalf("RTK hook: %v\n%s", err, output)
	}
	if commands := f.commands(t); !strings.Contains(commands, "rtk|proxy golangci-lint run --allow-serial-runners ./...") || !strings.Contains(commands, "rtk|proxy go test ./...") {
		t.Fatalf("RTK was not used:\n%s", commands)
	}
}

func TestPreCommitRejectsTrackedPrivatePaths(t *testing.T) {
	f := newHookFixture(t)
	f.write(t, "main.go", "package staged\n")
	f.write(t, "secrets/token", "test placeholder\n")
	f.git(t, "add", "--", "main.go", "secrets/token")
	output, err := f.run(t)
	if err == nil || !strings.Contains(output, "私人設定或產物") {
		t.Fatalf("private file was exported: %v\n%s", err, output)
	}
	if f.commands(t) != "" {
		t.Fatal("tools ran with a private file in the snapshot")
	}
}

func TestPreCommitRejectsPrivateFilesWithoutGoChanges(t *testing.T) {
	for _, tt := range []struct {
		name, path    string
		documentation bool
	}{{"env-only", ".env", false}, {"secret-only", "secrets/token", false}, {"readme-and-secret", "secrets/token", true}} {
		t.Run(tt.name, func(t *testing.T) {
			f := newHookFixture(t)
			if err := os.Remove(filepath.Join(f.bin, "mise")); err != nil {
				t.Fatal(err)
			}
			f.write(t, tt.path, "test placeholder\n")
			f.git(t, "add", "--force", "--", tt.path)
			if tt.documentation {
				f.write(t, "README.md", "documentation\n")
				f.git(t, "add", "--", "README.md")
			}
			output, err := f.run(t)
			if err == nil || !strings.Contains(output, "私人設定或產物") {
				t.Fatalf("private-only/documentation commit was allowed: %v\n%s", err, output)
			}
			if commands := f.commands(t); commands != "" {
				t.Fatalf("private index guard required Go tools: %s", commands)
			}
		})
	}
}

func TestPreCommitRejectsSymlinksOutsideSnapshot(t *testing.T) {
	f := newHookFixture(t)
	f.write(t, "main.go", "package staged\n")
	if err := os.Symlink("/not/a/real/secret", filepath.Join(f.root, "outside")); err != nil {
		t.Fatal(err)
	}
	f.git(t, "add", "--", "main.go", "outside")
	output, err := f.run(t)
	if err == nil || !strings.Contains(output, "symbolic link") {
		t.Fatalf("symlink was exported: %v\n%s", err, output)
	}
}

func TestPreCommitHonorsAlternateIndex(t *testing.T) {
	f := newHookFixture(t)
	f.write(t, "docs/notes.md", "main index\n")
	f.git(t, "add", "--", "docs/notes.md")
	mainIndex := f.git(t, "ls-files", "--stage", "-z")
	f.env = append(f.env, "GIT_INDEX_FILE=alternate-index")
	f.write(t, "main.go", "package staged\n")
	f.git(t, "add", "--", "main.go")
	f.write(t, "main.go", "package unstaged\n")
	if output, err := f.run(t); err != nil {
		t.Fatalf("alternate index ignored: %v\n%s", err, output)
	}
	f.env = f.env[:len(f.env)-1]
	if mainIndex != f.git(t, "ls-files", "--stage", "-z") {
		t.Fatal("main index was modified")
	}
}

const fakeMise = `#!/bin/sh
[ "$1" = exec ] && [ "$2" = -- ] || exit 90
printf 'mise|%s|%s\n' "$PWD" "$*" >> "$HOOK_TEST_LOG"
shift 2
exec "$@"
`

const fakeCheckedTool = `#!/bin/sh
tool=${0##*/}
printf '%s|%s|%s\n' "$tool" "$PWD" "$*" >> "$HOOK_TEST_LOG"
[ "$PWD" != "$HOOK_TEST_REPO" ] || exit 91
[ "$GOCACHE" = "$HOOK_TEST_REPO/.cache/go-build" ] || exit 92
[ "$GOMODCACHE" = "$HOOK_TEST_REPO/.cache/gomod" ] || exit 93
[ "$GOLANGCI_LINT_CACHE" = "$HOOK_TEST_REPO/.cache/golangci-lint" ] || exit 94
[ "$GOFLAGS" = -mod=readonly ] && [ "$GOWORK" = off ] || exit 95
[ -z "${GIT_INDEX_FILE:-}${GIT_DIR:-}${GIT_WORK_TREE:-}${RILLWAY_LIVE_CONFIG:-}" ] || exit 96
[ ! -e .git ] && [ ! -e untracked.go ] && [ ! -e secrets ] && [ ! -e .local ] || exit 97
IFS= read -r actual < "$HOOK_TEST_STAGED_PATH"
[ "$actual" = "$HOOK_TEST_EXPECTED" ] || exit 98
if [ "$tool" = golangci-lint ]; then
  [ "$*" = 'run --allow-serial-runners ./...' ] || exit 99
  exit "${HOOK_TEST_LINT_EXIT:-0}"
fi
[ "$*" = 'test ./...' ] || exit 100
exit "${HOOK_TEST_GO_EXIT:-0}"
`

const fakeRTK = `#!/bin/sh
printf 'rtk|%s\n' "$*" >> "$HOOK_TEST_LOG"
[ "$1" = proxy ] || exit 89
shift
exec "$@"
`
