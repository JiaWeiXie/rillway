#!/usr/bin/env bash
# Validate exactly one candidate using git-cliff's official parser. A temporary
# one-commit repository also works when the real repository has no HEAD yet.
set -euo pipefail

run() {
  if command -v rtk >/dev/null 2>&1; then rtk proxy "$@"; else "$@"; fi
}
fail() { printf 'Rillway commit-msg: %s\n' "$*" >&2; exit 1; }

[[ $# == 1 ]] || fail '請提供提交訊息檔案路徑；使用 - 從 stdin 讀取。'
command -v git >/dev/null 2>&1 || fail '找不到 Git，無法驗證提交訊息。'
command -v mise >/dev/null 2>&1 || fail '找不到 mise。請先安裝 mise，再於專案根目錄執行 mise install。'
repository=$(run git rev-parse --show-toplevel) || fail '目前目錄不在 Git repository 中。'
readonly repository
scratch=$(run mktemp -d "${TMPDIR:-/tmp}/rillway-commit-msg.XXXXXXXX") || fail '無法建立驗證用暫存目錄。'
readonly scratch
completed=0
cleanup() {
  result=$?
  trap - EXIT
  if [[ $result == 0 && $completed != 1 ]]; then result=1; fi
  if ! run rm -rf -- "$scratch"; then result=1; fi
  exit "$result"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

# Copy bytes, including trailing newlines, without command substitution, eval,
# whitespace cleanup or changing the original message file. cat accepts stdin -.
run cat -- "$1" > "$scratch/message"
cd "$repository"
if run mise exec -- bash -c '
  set -euo pipefail
  scratch=$1
  config=$2
  command -v git-cliff >/dev/null 2>&1 || {
    printf "%s\n" "Rillway commit-msg: mise 環境找不到 git-cliff；請在專案根目錄執行 mise install。" >&2
    exit 127
  }
  run() {
    if command -v rtk >/dev/null 2>&1; then rtk proxy "$@"; else "$@"; fi
  }
  # Git hooks inherit the real index/repository. None of that state, signing
  # configuration, or git-cliff environment overrides belongs in this sandbox.
  for name in ${!GIT_@}; do unset "$name"; done
  export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_SYSTEM=/dev/null GIT_CONFIG_GLOBAL=/dev/null
  export GIT_CONFIG_COUNT=0
  export GIT_AUTHOR_NAME="Rillway validation" GIT_COMMITTER_NAME="Rillway validation"
  export GIT_AUTHOR_EMAIL=hook@localhost GIT_COMMITTER_EMAIL=hook@localhost
  export GIT_AUTHOR_DATE=2000-01-01T00:00:00+0000 GIT_COMMITTER_DATE=2000-01-01T00:00:00+0000
  isolated_git() {
    run git -c core.hooksPath=/dev/null -c commit.gpgsign=false "$@"
  }
  temporary_repository="$scratch/repository"
  isolated_git init --quiet --initial-branch=main --template= "$temporary_repository"
  tree=$(isolated_git -C "$temporary_repository" hash-object -w -t tree --stdin </dev/null)
  commit=$(isolated_git -C "$temporary_repository" commit-tree "$tree" -F "$scratch/message")
  isolated_git -C "$temporary_repository" update-ref refs/heads/main "$commit"
  # The sole commit is this candidate, so no user history or existing tags are
  # consulted. commit-tree invokes neither git commit hooks nor signing tools.
  run git-cliff --repository "$temporary_repository" --config "$config" --offline --no-exec >/dev/null
' rillway-commit-msg "$scratch" "$repository/scripts/hooks/commit-cliff.toml"; then
  completed=1
else
  result=$?
  printf '%s\n' 'Rillway commit-msg: git-cliff 驗證未通過，提交已停止。請依上方錯誤修正；訊息格式例如 feat(proxy): add routing rule。' >&2
  exit "$result"
fi
