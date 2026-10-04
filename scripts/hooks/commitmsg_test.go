package hooks

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func (f *hookFixture) commitMessage(t *testing.T, message []byte, stdin bool) (string, error) {
	t.Helper()
	argument := "candidate message.txt"
	if stdin {
		argument = "-"
	} else {
		f.write(t, argument, string(message))
	}
	cmd := exec.Command(filepath.Join(f.root, ".githooks", "commit-msg"), argument)
	cmd.Dir = f.root
	cmd.Env = f.env
	if stdin {
		cmd.Stdin = bytes.NewReader(message)
	}
	output, err := cmd.CombinedOutput()
	if !stdin {
		after, readErr := os.ReadFile(filepath.Join(f.root, argument))
		if readErr != nil || !bytes.Equal(after, message) {
			t.Fatalf("commit hook changed candidate bytes: %v", readErr)
		}
	}
	entries, readErr := os.ReadDir(f.scratch)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("commit hook failed to clean temporary repository: %v %v", entries, readErr)
	}
	return string(output), err
}

func TestCommitMessagePreservesFileAndStdinBytes(t *testing.T) {
	for _, stdin := range []bool{false, true} {
		name := "file"
		if stdin {
			name = "stdin"
		}
		t.Run(name, func(t *testing.T) {
			f := newHookFixture(t)
			f.writeTool(t, "git-cliff", fakeCliff)
			capture := filepath.Join(f.root, "candidate-object")
			f.env = append(f.env, "HOOK_TEST_CAPTURE="+capture, "GIT_DIR="+filepath.Join(f.root, ".git"), "GIT_WORK_TREE="+f.root, "GIT_INDEX_FILE="+filepath.Join(f.root, "unused-index"), "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=commit.gpgsign", "GIT_CONFIG_VALUE_0=true")
			message := []byte("feat(proxy)!: retain literal $(printf injected > \"$HOOK_TEST_REPO/injected\") `printf injected > \"$HOOK_TEST_REPO/injected\"`\n\nBody has tabs\tand unicode 臺灣.\n\nBREAKING CHANGE: old configuration removed\n\n")
			before := f.localConfig(t)
			if output, err := f.commitMessage(t, message, stdin); err != nil {
				t.Fatalf("commit message hook: %v\n%s", err, output)
			}
			object, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			_, body, ok := bytes.Cut(object, []byte("\n\n"))
			if !ok || !bytes.Equal(body, message) {
				t.Fatalf("candidate commit bytes changed:\n%q", body)
			}
			if _, err = os.Stat(filepath.Join(f.root, "injected")); !os.IsNotExist(err) {
				t.Fatal("commit message was executed")
			}
			if f.localConfig(t) != before {
				t.Fatal("real repository config changed")
			}
			if _, err = os.Stat(filepath.Join(f.root, ".git", "refs", "heads", "main")); !os.IsNotExist(err) {
				t.Fatal("hook created a real repository commit")
			}
		})
	}
}

func TestCommitMessageReportsMissingTools(t *testing.T) {
	for _, tool := range []string{"mise", "git-cliff"} {
		t.Run(tool, func(t *testing.T) {
			f := newHookFixture(t)
			if tool == "mise" {
				if err := os.Remove(filepath.Join(f.bin, "mise")); err != nil {
					t.Fatal(err)
				}
			}
			output, err := f.commitMessage(t, []byte("feat: example\n"), false)
			if err == nil || !strings.Contains(output, "找不到 "+tool) || !strings.Contains(output, "mise install") {
				t.Fatalf("missing tool error: %v\n%s", err, output)
			}
		})
	}
}

func TestCommitMessageUsesOfficialParser(t *testing.T) {
	parser, err := exec.LookPath("git-cliff")
	if err != nil {
		t.Skip("git-cliff not installed; run mise install for official-parser integration tests")
	}
	for _, tt := range []struct {
		name, message string
		valid         bool
	}{
		{"feat", "feat(proxy): add routing rule\n", true},
		{"breaking", "feat!: replace configuration\n\nBREAKING CHANGE: old settings are removed\n\n", true},
		{"body", "fix: relay EOF\n\nPreserve literal $(false) and `false`.\n", true},
		{"invalid", "plain message\n", false},
		{"empty", "", false},
		{"option-like", "--help\n", false},
		{"merge", "Merge branch main\n", false},
		{"fixup", "fixup! feat: add feature\n", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newHookFixture(t)
			if err := os.Symlink(parser, filepath.Join(f.bin, "git-cliff")); err != nil {
				t.Fatal(err)
			}
			output, err := f.commitMessage(t, []byte(tt.message), false)
			if (err == nil) != tt.valid {
				t.Fatalf("official parser result valid=%v: %v\n%s", tt.valid, err, output)
			}
			if strings.Contains(output, "panicked") {
				t.Fatalf("synthetic message triggered parser panic: %s", output)
			}
		})
	}
}

const fakeCliff = `#!/bin/sh
repository=
config=
offline=0
noexec=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --repository) repository=$2; shift 2 ;;
    --config) config=$2; shift 2 ;;
    --offline) offline=1; shift ;;
    --no-exec) noexec=1; shift ;;
    *) exit 90 ;;
  esac
done
[ "$offline" = 1 ] && [ "$noexec" = 1 ] || exit 91
[ "$config" = "$HOOK_TEST_REPO/scripts/hooks/commit-cliff.toml" ] || exit 92
[ "$GIT_CONFIG_NOSYSTEM" = 1 ] && [ "$GIT_CONFIG_SYSTEM" = /dev/null ] && [ "$GIT_CONFIG_GLOBAL" = /dev/null ] && [ "$GIT_CONFIG_COUNT" = 0 ] || exit 93
[ -z "${GIT_DIR:-}${GIT_WORK_TREE:-}${GIT_INDEX_FILE:-}${GIT_CONFIG_KEY_0:-}${GIT_CONFIG_VALUE_0:-}" ] || exit 94
[ "$(git -C "$repository" rev-list --all --count)" = 1 ] || exit 95
git -C "$repository" cat-file commit HEAD > "$HOOK_TEST_CAPTURE"
`
