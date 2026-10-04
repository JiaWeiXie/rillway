package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type fakeRunner struct {
	diff, untracked string
	fail            string
	output          string
	commands        [][]string
	onCheck         func([]string)
}

func (f *fakeRunner) Run(_ context.Context, _ string, _ int, args ...string) ([]byte, error) {
	f.commands = append(f.commands, append([]string(nil), args...))
	if args[0] == "git" {
		if args[1] == "diff" {
			return []byte(f.diff), nil
		}
		return []byte(f.untracked), nil
	}
	if f.onCheck != nil {
		f.onCheck(args)
	}
	if args[0] == f.fail {
		return []byte(f.output), errors.New("check failed")
	}
	return nil, nil
}

func (f *fakeRunner) checks() [][]string {
	var result [][]string
	for _, args := range f.commands {
		if args[0] != "git" {
			result = append(result, args)
		}
	}
	return result
}

func fixture(t *testing.T) (checker, *fakeRunner) {
	t.Helper()
	f := &fakeRunner{}
	c := checker{root: t.TempDir(), run: f, now: time.Now}
	putFile(t, c.root, "go.mod", "module example\n")
	return c, f
}

func putFile(t *testing.T, root, path, content string) {
	t.Helper()
	full := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestEventSchemaAndIgnoredEvents(t *testing.T) {
	for _, input := range []string{
		`{"hook_event_name":"PreToolUse"}`, `{}`, `null`,
		`{"hook_event_name":"Stop","tool_input":{"command":"never execute"}}`,
	} {
		c, f := fixture(t)
		var out bytes.Buffer
		handle(strings.NewReader(input), &out, c)
		if out.Len() != 0 || len(f.commands) != 0 {
			t.Fatalf("non-PostToolUse event handled: %s", input)
		}
	}
	for _, input := range []string{`{`, `[]`, `{"hook_event_name":22}`, strings.Repeat("x", maxInput+1)} {
		c, f := fixture(t)
		var out bytes.Buffer
		handle(strings.NewReader(input), &out, c)
		if !json.Valid(out.Bytes()) || len(f.commands) != 0 {
			t.Fatal("invalid input must produce bounded advisory without commands")
		}
	}
}

func TestHookInputCannotInjectCommands(t *testing.T) {
	c, f := fixture(t)
	putFile(t, c.root, "internal/space name/source.go", "package sample\n")
	f.untracked = "internal/space name/source.go\x00"
	input := `{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"touch /tmp/not-authorized; $(curl bad.invalid)"},"transcript_path":"/private/secret"}`
	var out bytes.Buffer
	handle(strings.NewReader(input), &out, c)
	var result struct {
		Output struct {
			Event   string `json:"hookEventName"`
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Output.Event != "PostToolUse" || !strings.Contains(result.Output.Context, "通過") {
		t.Fatalf("invalid hook response: %s", out.String())
	}
	want := [][]string{
		{"golangci-lint", "run", "--allow-serial-runners", "./internal/space name"},
		{"go", "test", "-timeout=45s", "./internal/space name"},
	}
	if !reflect.DeepEqual(f.checks(), want) {
		t.Fatalf("unsafe argument construction: %#v", f.commands)
	}
	for _, args := range f.commands {
		if strings.Contains(strings.Join(args, " "), "not-authorized") {
			t.Fatal("hook command was executed")
		}
	}
}

func TestDocsAndPrivateFilesDoNotTriggerChecks(t *testing.T) {
	c, f := fixture(t)
	f.diff = "README.md\x00docs/guide.md\x00.env\x00.local/profile.go\x00secrets/key.go\x00state/session.go\x00.cache/module.go\x00"
	if result := c.check(context.Background()); result != "" {
		t.Fatal(result)
	}
	if len(f.checks()) != 0 {
		t.Fatal("docs or private files triggered checks")
	}
	if _, err := os.Stat(filepath.Join(c.root, cacheDir)); !os.IsNotExist(err) {
		t.Fatal("docs-only hook wrote cache")
	}
}

func TestSuccessAndFailureCacheEntireSourceTree(t *testing.T) {
	for _, fail := range []string{"", "golangci-lint"} {
		t.Run("failure="+fail, func(t *testing.T) {
			c, f := fixture(t)
			putFile(t, c.root, "internal/a/a.go", "package a\n")
			putFile(t, c.root, "internal/b/b.go", "package b\n")
			f.diff, f.fail, f.output = "internal/a/a.go\x00", fail, "a.go:3: concrete lint problem"
			first := c.check(context.Background())
			if fail != "" && !strings.Contains(first, f.output) {
				t.Fatal("check error missing")
			}
			_ = c.check(context.Background())
			if len(f.checks()) != 2 {
				t.Fatal("identical source reran checks")
			}
			putFile(t, c.root, "internal/b/b.go", "package b\n// dependency changed\n")
			_ = c.check(context.Background())
			if len(f.checks()) != 4 {
				t.Fatal("hash did not include other Go source")
			}
			putFile(t, c.root, "go.mod", "module changed\n")
			_ = c.check(context.Background())
			if len(f.checks()) != 6 {
				t.Fatal("hash did not include configuration")
			}
			data, err := os.ReadFile(filepath.Join(c.root, cacheDir, "checked.json"))
			if err != nil {
				t.Fatal(err)
			}
			var stored map[string]string
			if err := json.Unmarshal(data, &stored); err != nil || len(stored) != 2 || len(stored["hash"]) != 64 {
				t.Fatalf("cache should contain only hash/status: %s", data)
			}
			if strings.Contains(string(data), f.output) && f.output != "" {
				t.Fatal("cache retained command output")
			}
		})
	}
}

func TestSourceChangedDuringChecksIsNotCached(t *testing.T) {
	c, f := fixture(t)
	putFile(t, c.root, "a.go", "package sample\n")
	f.diff = "a.go\x00"
	f.onCheck = func(args []string) {
		if args[0] == "golangci-lint" {
			putFile(t, c.root, "a.go", "package sample\n// changed concurrently\n")
		}
	}
	message := c.check(context.Background())
	if !strings.Contains(message, "本次結果未快取") || strings.Contains(message, "檢查通過") {
		t.Fatalf("concurrent change was reported as verified: %s", message)
	}
	if _, ok := c.cached(); ok {
		t.Fatal("saved a result for a tree that changed during checks")
	}
	f.onCheck = nil
	_ = c.check(context.Background())
	if len(f.checks()) != 4 {
		t.Fatal("current tree was not checked again")
	}
	if cached, ok := c.cached(); !ok || cached.Status != "passed" {
		t.Fatal("stable tree result was not saved")
	}
}

func TestFinalHashReadFailureIsNotCached(t *testing.T) {
	c, f := fixture(t)
	putFile(t, c.root, "a.go", "package sample\n")
	f.diff = "a.go\x00"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.onCheck = func(args []string) {
		if args[0] == "go" {
			cancel()
		}
	}
	if message := c.check(ctx); !strings.Contains(message, "本次結果未快取") {
		t.Fatalf("missing recheck advisory: %s", message)
	}
	if _, ok := c.cached(); ok {
		t.Fatal("cached result after final tree verification failed")
	}
}

func TestDeletedPackagesConfigurationAndEmbeddedAssets(t *testing.T) {
	for _, tc := range []struct {
		name, changed, remaining, want string
	}{
		{"deleted package", "internal/gone/a.go", "", "./..."},
		{"deleted file", "internal/keep/a.go", "internal/keep/b.go", "./internal/keep"},
		{"module", "go.mod", "", "./..."},
		{"module checksum", "go.sum", "", "./..."},
		{"workspace", "go.work", "", "./..."},
		{"workspace checksum", "go.work.sum", "", "./..."},
		{"lint config", ".golangci.yml", "", "./..."},
		{"lint yaml config", ".golangci.yaml", "", "./..."},
		{"lint toml config", ".golangci.toml", "", "./..."},
		{"lint json config", ".golangci.json", "", "./..."},
		{"mise", "mise.toml", "", "./..."},
		{"hidden mise", ".mise.toml", "", "./..."},
		{"mise lock", "mise.lock", "", "./..."},
		{"embedded web", "internal/control/web/app.js", "", "./internal/control"},
		{"hook shell", "scripts/hooks/post-edit.sh", "", "./scripts/hooks"},
		{"nested hook shell", "scripts/hooks/lib/check.sh", "", "./scripts/hooks"},
		{"pre-commit hook", ".githooks/pre-commit", "", "./scripts/hooks"},
		{"commit-msg hook", ".githooks/commit-msg", "", "./scripts/hooks"},
		{"commit cliff config", "scripts/hooks/commit-cliff.toml", "", "./scripts/hooks"},
		{"changelog cliff config", "cliff.toml", "", "./scripts/changelog"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, f := fixture(t)
			if tc.remaining != "" {
				putFile(t, c.root, tc.remaining, "package keep\n")
			}
			f.diff = tc.changed + "\x00"
			_ = c.check(context.Background())
			checks := f.checks()
			if len(checks) != 2 || checks[0][len(checks[0])-1] != tc.want || checks[1][len(checks[1])-1] != tc.want {
				t.Fatalf("wrong scope: %#v", checks)
			}
		})
	}
}

func TestHookAndChangelogInputsInvalidatePreviousCache(t *testing.T) {
	for _, path := range []string{"scripts/hooks/post-edit.sh", ".githooks/pre-commit", ".githooks/commit-msg", "scripts/hooks/commit-cliff.toml", "cliff.toml"} {
		t.Run(path, func(t *testing.T) {
			c, f := fixture(t)
			putFile(t, c.root, "scripts/hooks/hooks_test.go", "package hooks\n")
			putFile(t, c.root, path, "#!/bin/sh\nexit 0\n")
			f.diff = "scripts/hooks/hooks_test.go\x00"
			_ = c.check(context.Background())
			_ = c.check(context.Background())
			if len(f.checks()) != 2 {
				t.Fatal("identical hook tree reran checks")
			}
			putFile(t, c.root, path, "#!/bin/sh\nexit 1\n")
			_ = c.check(context.Background())
			if len(f.checks()) != 4 {
				t.Fatal("hook or changelog content was not included in tree hash")
			}
		})
	}
}

func TestNewCommitAndChangelogInputsSelectTheirTestPackage(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{".githooks/commit-msg", "./scripts/hooks"},
		{"scripts/hooks/commit-cliff.toml", "./scripts/hooks"},
		{"cliff.toml", "./scripts/changelog"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			c, f := fixture(t)
			putFile(t, c.root, tc.path, "# newly added input\n")
			f.untracked = tc.path + "\x00"
			_ = c.check(context.Background())
			checks := f.checks()
			if len(checks) != 2 || checks[0][len(checks[0])-1] != tc.want || checks[1][len(checks[1])-1] != tc.want {
				t.Fatalf("wrong scope for new input: %#v", checks)
			}
		})
	}
}

func TestUnrelatedScriptsDoNotMapToGoPackages(t *testing.T) {
	c, f := fixture(t)
	f.diff = "scripts/deploy.sh\x00scripts/check.sh\x00.githooks/other-hook\x00scripts/hooks/README.md\x00"
	if result := c.check(context.Background()); result != "" || len(f.checks()) != 0 {
		t.Fatal("unrelated script mapped to a Go package")
	}
}

func TestSymlinksAndTraversalAreNotFollowed(t *testing.T) {
	for _, kind := range []string{"file", "directory", "cache", "traversal", "absolute"} {
		t.Run(kind, func(t *testing.T) {
			c, f := fixture(t)
			outside := t.TempDir()
			putFile(t, outside, "private.go", "DO NOT READ\n")
			putFile(t, c.root, "a.go", "package sample\n")
			f.diff = "a.go\x00"
			var link, target string
			switch kind {
			case "file":
				f.diff, link, target = "linked.go\x00", "linked.go", filepath.Join(outside, "private.go")
			case "directory":
				f.diff, link, target = "linked/private.go\x00", "linked", outside
			case "cache":
				link, target = ".cache", outside
			case "traversal":
				f.diff = "../private.go\x00"
			case "absolute":
				f.diff = filepath.Join(outside, "private.go") + "\x00"
			}
			if link != "" {
				if err := os.Symlink(target, filepath.Join(c.root, link)); err != nil {
					t.Fatal(err)
				}
			}
			message := c.check(context.Background())
			if len(f.checks()) != 0 || strings.Contains(message, "DO NOT READ") {
				t.Fatal("unsafe path was used")
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 1 {
				t.Fatal("wrote outside repository")
			}
		})
	}
}

func TestUnrelatedSymlinkAndSecretDoNotAffectHash(t *testing.T) {
	c, f := fixture(t)
	putFile(t, c.root, "a.go", "package sample\n")
	outside := t.TempDir()
	putFile(t, outside, "private.go", "first secret")
	if err := os.Symlink(filepath.Join(outside, "private.go"), filepath.Join(c.root, "link.go")); err != nil {
		t.Fatal(err)
	}
	f.diff = "a.go\x00"
	_ = c.check(context.Background())
	putFile(t, outside, "private.go", "changed secret")
	putFile(t, c.root, "secrets/private.go", "secret")
	_ = c.check(context.Background())
	if len(f.checks()) != 2 {
		t.Fatal("hash read unrelated symlink or secret")
	}
}

func TestBusyAndStaleLocks(t *testing.T) {
	c, f := fixture(t)
	putFile(t, c.root, "a.go", "package sample\n")
	f.diff = "a.go\x00"
	lock := filepath.Join(c.root, cacheDir, "lock")
	if err := os.MkdirAll(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = c.check(context.Background())
	if len(f.checks()) != 0 {
		t.Fatal("busy lock did not prevent concurrent checks")
	}
	old := time.Now().Add(-4 * time.Minute)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	_ = c.check(context.Background())
	if len(f.checks()) != 2 {
		t.Fatal("stale lock was not recovered")
	}
}

func TestOutputAndInputAreBounded(t *testing.T) {
	b := limitedBuffer{limit: 8}
	for _, text := range []string{"12345", "67890", "abcdef"} {
		if n, err := b.Write([]byte(text)); err != nil || n != len(text) {
			t.Fatal("must consume output without blocking child process")
		}
	}
	if string(b.data) != "12345678" || !b.truncated {
		t.Fatal("output exceeded cap")
	}
	c, f := fixture(t)
	putFile(t, c.root, "a.go", "package sample\n")
	f.diff, f.fail, f.output = "a.go\x00", "go", strings.Repeat("錯誤", maxOutput)
	result := c.check(context.Background())
	if len(result) > maxOutput || !utf8.ValidString(result) {
		t.Fatal("advisory is unbounded or invalid UTF-8")
	}
}

func TestInheritedLiveAndRaceOptionsAreRemoved(t *testing.T) {
	t.Setenv("GOFLAGS", "-race -tags=live")
	t.Setenv("RILLWAY_LIVE_CONFIG", "/private/secrets/config.json")
	t.Setenv("GIT_WORK_TREE", "/outside")
	root := t.TempDir()
	env := strings.Join(checkEnvironment(root), "\n")
	if strings.Contains(env, "-race") || strings.Contains(env, "RILLWAY_LIVE_CONFIG=") || strings.Contains(env, "GIT_WORK_TREE=") {
		t.Fatal("inherited options can enable live checks")
	}
	if !strings.Contains(env, "GOENV=off") || !strings.Contains(env, "GOCACHE="+filepath.Join(root, ".cache/go-build")) {
		t.Fatal("Go check environment is not confined")
	}
	if !strings.Contains(env, "GOFLAGS=-mod=readonly\n") || !strings.Contains(env, "GOPATH="+filepath.Join(root, ".cache/go")+"\n") {
		t.Fatal("module writes must be disabled and GOPATH must match mise")
	}
}
