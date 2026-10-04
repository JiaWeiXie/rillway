#!/bin/sh
# Both Codex and Claude send a JSON event on stdin. Never evaluate its contents.
set -eu

bootstrap_failed() {
  printf '%s\n' '{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"Rillway edit checks could not start. Run mise exec -- go test ./tools/agentcheck from the project root to inspect the toolchain or compilation error. This advisory does not block the edit."}}'
}

if ! task_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P) 2>/dev/null; then
  bootstrap_failed
  exit 0
fi
if ! cd "$task_root" 2>/dev/null; then
  bootstrap_failed
  exit 0
fi
if ! command -v mise >/dev/null 2>&1; then
  printf '%s\n' '{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"Rillway edit checks could not run: mise is missing from the Agent PATH. Install mise and run mise install in the project."}}'
  exit 0
fi

# Sanitize before compiling the helper: its own environment guard runs too late
# to stop inherited Go flags, a host workspace, or automatic toolchain changes.
export GOFLAGS=-mod=readonly GOWORK=off GOENV=off GOTOOLCHAIN=local
unset RILLWAY_LIVE_CONFIG RILLWAY_LIVE_OUTBOUND RILLWAY_LIVE_TARGET

run() {
  "$@"
}

# Preserve stdin for the helper. Capture stdout so a failed bootstrap cannot
# mix diagnostics with the JSON advisory; never evaluate event or output text.
if result=$(run mise exec -- go run ./tools/agentcheck post-edit 2>/dev/null); then
  if [ -n "$result" ]; then
    printf '%s\n' "$result"
  fi
else
  bootstrap_failed
fi
exit 0
