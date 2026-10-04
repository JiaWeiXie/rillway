#!/bin/sh
set -eu
run() {
  if command -v rtk >/dev/null 2>&1; then rtk proxy "$@"; else "$@"; fi
}
task_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
cd "$task_root"
task_existing=$(run git config --get core.hooksPath || true)
if [ -n "$task_existing" ] && [ "$task_existing" != '.githooks' ]; then
  printf 'Existing core.hooksPath=%s; keeping it. Integrate .githooks/pre-commit with that hook manager.\n' "$task_existing" >&2
  exit 1
fi
task_default_hook=$(run git rev-parse --git-path hooks/pre-commit)
if [ -z "$task_existing" ] && [ -f "$task_default_hook" ]; then
  printf 'An existing pre-commit hook is present at %s; keeping it. Integrate the Rillway hook there.\n' "$task_default_hook" >&2
  exit 1
fi
if [ ! -x .githooks/pre-commit ]; then
  printf '%s\n' 'Missing executable .githooks/pre-commit; check out the complete repository first.' >&2
  exit 1
fi
if [ ! -x scripts/hooks/pre-commit.sh ]; then
  printf '%s\n' 'Missing executable scripts/hooks/pre-commit.sh; check out the complete repository first.' >&2
  exit 1
fi
run git config --local core.hooksPath .githooks
printf '%s\n' 'Rillway Git pre-commit hook enabled for this repository.'
printf '%s\n' 'Codex: open /hooks to review and trust the project PostToolUse hook.'
