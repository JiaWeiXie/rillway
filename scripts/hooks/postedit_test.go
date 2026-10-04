package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// No real mise, Go compiler, or helper runs here: these fixtures exercise the
// wrapper without recursively starting the tests from inside a post-edit hook.
type postEditFixture struct {
	root, bin string
	env       []string
}

func newPostEditFixture(t *testing.T) *postEditFixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &postEditFixture{root: filepath.Join(base, "project with spaces"), bin: filepath.Join(base, "fake-bin")}
	for _, path := range []string{f.bin, filepath.Join(f.root, "scripts", "hooks")} {
		if err = os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, tool := range []string{"sh", "dirname", "cat"} {
		path, lookupErr := exec.LookPath(tool)
		if lookupErr != nil {
			t.Fatalf("post-edit test requires %s: %v", tool, lookupErr)
		}
		if err = os.Symlink(path, filepath.Join(f.bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	source, err := os.ReadFile("post-edit.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(f.root, "scripts", "hooks", "post-edit.sh"), source, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "PATH" || strings.HasPrefix(key, "GO") || strings.HasPrefix(key, "RILLWAY_LIVE_") || strings.HasPrefix(key, "POSTEDIT_TEST_") {
			continue
		}
		f.env = append(f.env, entry)
	}
	f.env = append(f.env, "PATH="+f.bin, "POSTEDIT_TEST_ROOT="+f.root,
		"GOFLAGS=-rillway-invalid", "GOWORK=/not/a/real/go.work", "GOENV=/not/a/real/go.env", "GOTOOLCHAIN=not-a-toolchain",
		"RILLWAY_LIVE_CONFIG=/not/a/real/config.json", "RILLWAY_LIVE_OUTBOUND=private", "RILLWAY_LIVE_TARGET=private.invalid:443")
	f.tool(t, "mise", postEditFakeMise)
	f.tool(t, "go", postEditFakeGo)
	return f
}

func (f *postEditFixture) tool(t *testing.T, name, source string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.bin, name), []byte(source), 0o700); err != nil {
		t.Fatal(err)
	}
}

func (f *postEditFixture) run(t *testing.T, input string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(f.root, "scripts", "hooks", "post-edit.sh"))
	cmd.Dir = f.bin // The wrapper must resolve its own project, not the caller cwd.
	cmd.Env = f.env
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("post-edit wrapper must be advisory: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("wrapper exposed bootstrap stderr: %s", stderr.String())
	}
	return stdout.String()
}

func postEditMessage(t *testing.T, output string) string {
	t.Helper()
	if strings.Count(output, "\n") != 1 {
		t.Fatalf("expected one JSON line, got %q", output)
	}
	var response struct {
		Output struct {
			Event   string `json:"hookEventName"`
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(output), &response); err != nil || response.Output.Event != "PostToolUse" || response.Output.Context == "" {
		t.Fatalf("invalid advisory JSON: %q: %v", output, err)
	}
	return response.Output.Context
}

func TestPostEditPreservesInputAndSanitizesBootstrap(t *testing.T) {
	const input = "{\"hook_event_name\":\"PostToolUse\",\"tool_input\":{\"command\":\"$(printf compromised > payload-executed); exit 97\"}}\n\n"
	const output = `{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"fixture check passed"}}`
	for _, withRTK := range []bool{false, true} {
		name := "without RTK"
		if withRTK {
			name = "with RTK"
		}
		t.Run(name, func(t *testing.T) {
			f := newPostEditFixture(t)
			f.env = append(f.env, "POSTEDIT_TEST_OUTPUT="+output)
			if withRTK {
				f.tool(t, "rtk", postEditFakeRTK)
			}
			actual := f.run(t, input)
			if actual != output+"\n" {
				t.Fatalf("helper output changed: %q", actual)
			}
			_ = postEditMessage(t, actual)
			captured, err := os.ReadFile(filepath.Join(f.root, "captured-input"))
			if err != nil || string(captured) != input {
				t.Fatalf("stdin was consumed or changed: %q: %v", captured, err)
			}
			for _, path := range []string{filepath.Join(f.root, "payload-executed"), filepath.Join(f.bin, "payload-executed")} {
				if _, err = os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("event text was executed: %s: %v", path, err)
				}
			}
			trace, err := os.ReadFile(filepath.Join(f.root, "rtk-used"))
			if withRTK && (err != nil || string(trace) != "proxy mise exec -- go run ./tools/agentcheck post-edit\n") {
				t.Fatalf("RTK was not used: %q: %v", trace, err)
			}
			if !withRTK && !os.IsNotExist(err) {
				t.Fatalf("unexpected RTK invocation: %q: %v", trace, err)
			}
		})
	}
}

func TestPostEditBootstrapFailureIsFixedAdvisory(t *testing.T) {
	var previous string
	for _, failure := range []string{"mise", "go", "missing-go"} {
		t.Run(failure, func(t *testing.T) {
			f := newPostEditFixture(t)
			f.env = append(f.env, "POSTEDIT_TEST_FAIL="+failure)
			if failure == "missing-go" {
				if err := os.Remove(filepath.Join(f.bin, "go")); err != nil {
					t.Fatal(err)
				}
			}
			output := f.run(t, "{\"hook_event_name\":\"PostToolUse\"}\n")
			message := postEditMessage(t, output)
			if !strings.Contains(message, "mise exec -- go test ./tools/agentcheck") || strings.Contains(output, "private-bootstrap-output") {
				t.Fatalf("missing safe bootstrap guidance: %q", output)
			}
			if previous != "" && output != previous {
				t.Fatalf("bootstrap failures must produce fixed output: %q != %q", output, previous)
			}
			previous = output
		})
	}
}

func TestPostEditMissingMiseIsAdvisory(t *testing.T) {
	f := newPostEditFixture(t)
	if err := os.Remove(filepath.Join(f.bin, "mise")); err != nil {
		t.Fatal(err)
	}
	message := postEditMessage(t, f.run(t, "{\"hook_event_name\":\"PostToolUse\"}\n"))
	if !strings.Contains(message, "mise is missing") || !strings.Contains(message, "mise install") {
		t.Fatalf("missing mise guidance: %q", message)
	}
	if _, err := os.Stat(filepath.Join(f.root, "captured-input")); !os.IsNotExist(err) {
		t.Fatalf("helper ran without mise: %v", err)
	}
}

func TestPostEditSilentHelperStaysSilent(t *testing.T) {
	f := newPostEditFixture(t)
	if output := f.run(t, "{\"hook_event_name\":\"PostToolUse\"}\n"); output != "" {
		t.Fatalf("silent helper produced output: %q", output)
	}
}

const postEditFakeMise = `#!/bin/sh
set -eu
[ "$GOFLAGS" = -mod=readonly ] && [ "$GOWORK" = off ] && [ "$GOENV" = off ] && [ "$GOTOOLCHAIN" = local ] || exit 91
[ -z "${RILLWAY_LIVE_CONFIG:-}${RILLWAY_LIVE_OUTBOUND:-}${RILLWAY_LIVE_TARGET:-}" ] || exit 92
[ "$PWD" = "$POSTEDIT_TEST_ROOT" ] && [ "$*" = 'exec -- go run ./tools/agentcheck post-edit' ] || exit 93
if [ "${POSTEDIT_TEST_FAIL:-}" = mise ]; then
  printf '%s\n' private-bootstrap-output
  printf '%s\n' private-bootstrap-output >&2
  exit 42
fi
shift 2
exec "$@"
`

const postEditFakeGo = `#!/bin/sh
set -eu
[ "$GOFLAGS" = -mod=readonly ] && [ "$GOWORK" = off ] && [ "$GOENV" = off ] && [ "$GOTOOLCHAIN" = local ] || exit 94
[ "$*" = 'run ./tools/agentcheck post-edit' ] || exit 95
cat > "$POSTEDIT_TEST_ROOT/captured-input"
if [ "${POSTEDIT_TEST_FAIL:-}" = go ]; then
  printf '%s\n' private-bootstrap-output
  printf '%s\n' private-bootstrap-output >&2
  exit 43
fi
printf '%s' "${POSTEDIT_TEST_OUTPUT:-}"
`

const postEditFakeRTK = `#!/bin/sh
set -eu
printf '%s\n' "$*" > "$POSTEDIT_TEST_ROOT/rtk-used"
[ "$1" = proxy ] || exit 96
shift
exec "$@"
`
