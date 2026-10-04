#!/bin/sh
set -eu
run() {
  "$@"
}
task_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
cd "$task_root"
task_existing=$(run git config --get core.hooksPath || true)
if [ -n "$task_existing" ] && [ "$task_existing" != '.githooks' ]; then
  printf 'Existing core.hooksPath=%s; keeping it. Integrate the Rillway pre-commit and commit-msg hooks with that hook manager.\n' "$task_existing" >&2
  exit 1
fi
task_default_directory=$(run git rev-parse --git-path hooks)
if [ -z "$task_existing" ]; then
  for task_default_hook in "$task_default_directory"/*; do
    case "$task_default_hook" in *.sample) continue ;; esac
    if [ -f "$task_default_hook" ] && [ -x "$task_default_hook" ]; then
      task_hook_name=${task_default_hook##*/}
      printf 'An existing %s hook is present at %s; keeping it. Integrate the Rillway hooks there.\n' "$task_hook_name" "$task_default_hook" >&2
      exit 1
    fi
  done
fi
for task_hook in .githooks/pre-commit scripts/hooks/pre-commit.sh .githooks/commit-msg scripts/hooks/commit-msg.sh; do
  if [ ! -x "$task_hook" ]; then
    printf 'Missing executable %s; check out the complete repository first.\n' "$task_hook" >&2
    exit 1
  fi
done
if [ ! -f scripts/hooks/commit-cliff.toml ] || [ ! -r scripts/hooks/commit-cliff.toml ]; then
  printf '%s\n' 'Missing readable scripts/hooks/commit-cliff.toml; check out the complete repository first.' >&2
  exit 1
fi
run git config --local core.hooksPath .githooks
printf '%s\n' 'Rillway Git pre-commit and commit-msg hooks enabled for this repository.'
printf '%s\n' 'Codex: open /hooks to review and trust the project PostToolUse hook.'
