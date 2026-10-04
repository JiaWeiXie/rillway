#!/usr/bin/env bash
# Check the index, including partially staged files, without modifying it.
set -euo pipefail

run() {
  if command -v rtk >/dev/null 2>&1; then
    rtk proxy "$@"
  else
    "$@"
  fi
}

fail() {
  printf 'Rillway pre-commit: %s\n' "$*" >&2
  exit 1
}

command -v git >/dev/null 2>&1 || fail '找不到 Git，無法讀取已暫存的內容。'

# Git may supply a temporary index during a partial/path-limited commit. Keep
# that exact index even when this script was invoked from a subdirectory.
if [[ -n ${GIT_INDEX_FILE:-} && $GIT_INDEX_FILE != /* ]]; then
  export GIT_INDEX_FILE="$PWD/$GIT_INDEX_FILE"
fi
repository=$(run git rev-parse --show-toplevel) || fail '目前目錄不在 Git repository 中。'
readonly repository

scratch=$(run mktemp -d "${TMPDIR:-/tmp}/rillway-pre-commit.XXXXXXXX") || fail '無法建立提交檢查的暫存目錄。'
readonly scratch
completed=0
cleanup() {
  result=$?
  trap - EXIT
  # Older Bash versions can report status 0 to EXIT after an expansion error.
  # Only an explicit successful completion is allowed to unblock a commit.
  if [[ $result == 0 && $completed != 1 ]]; then
    result=1
  fi
  # scratch is readonly and comes only from a successful mktemp -d above.
  if ! run rm -rf -- "$scratch"; then
    printf 'Rillway pre-commit: 無法清理暫存目錄 %s\n' "$scratch" >&2
    result=1
  fi
  exit "$result"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

# Disable rename detection: renaming/removing a .go file must still trigger Go
# checks even when the destination has a different extension.
run git -C "$repository" diff --cached --name-only --no-renames -z > "$scratch/changed"
needs_go=0
while IFS= read -r -d '' path; do
  case "$path" in
    *.go|go.mod|go.sum|go.work|go.work.sum|mise.toml|mise.lock|.mise.toml|cliff.toml|.golangci.yml|.golangci.yaml|.golangci.toml|.golangci.json)
      needs_go=1
      ;;
    cmd/*|internal/*|tests/*|testdata/*|.githooks/*|scripts/hooks/*)
      # Embedded assets, fixtures and hook scripts have Go regression tests.
      needs_go=1
      ;;
    *.md|docs/*|LICENSE|NOTICE|CHANGELOG|CHANGELOG.*)
      ;;
  esac
done < "$scratch/changed"

# Only tracked index entries are exported. Refuse private/generated paths even
# if somebody forced them into the index. Symlinks and gitlinks could refer to
# content outside this snapshot, so they cannot provide an index-only check.
run git -C "$repository" ls-files --stage -z > "$scratch/index"
while IFS= read -r -d '' entry; do
  metadata=${entry%%$'\t'*}
  path=${entry#*$'\t'}
  mode=${metadata%% *}
  stage=${metadata##* }
  [[ $stage == 0 ]] || fail 'Git index 還有未解決的合併衝突；請先解決並暫存。'
  case "$mode" in
    100644|100755) ;;
    120000) fail "無法隔離已暫存的 symbolic link：${path}。請移除連結或改用一般檔案。" ;;
    160000) fail "無法隔離 submodule：${path}。此專案的提交檢查只支援單一 repository。" ;;
    *) fail "不支援的 Git index mode ${mode}：${path}" ;;
  esac
  case "$path" in
    .env.example) ;;
    .git|.git/*|.cache/*|.local/*|.state/*|secrets/*|run/*|logs/*|coverage/*|bin/*|build/*|dist/*|tmp/*|.env|.env.*|mise.local.toml|.mise.local.toml|rillway-proxy-backup.json|.rillway-proxy-backup.json)
      fail "私人設定或產物不應在 Git index：${path}。請先取消暫存該路徑。"
      ;;
  esac
done < "$scratch/index"

if [[ $needs_go == 0 ]]; then
  printf '%s\n' 'Rillway pre-commit: 沒有 Go 相關的已暫存變更，略過 Go 檢查。'
  completed=1
  exit 0
fi

command -v mise >/dev/null 2>&1 || fail '找不到 mise。請先安裝 mise，並在專案根目錄執行 mise install。'

snapshot="$scratch/snapshot"
run mkdir "$snapshot"
run git -C "$repository" checkout-index --all --force --prefix="$snapshot/"

printf '%s\n' 'Rillway pre-commit: 正在檢查已暫存副本，工作目錄與 Git index 保持不變。'

# Load the installed tools from the trusted ORIGINAL repository. Enter the
# snapshot only after mise has resolved its environment; otherwise config_root
# would relocate caches and mise would prompt to trust the temporary copy.
cd "$repository"
if run mise exec -- bash -c '
  set -euo pipefail
  repository=$1
  snapshot=$2
  for tool in go golangci-lint; do
    if ! command -v "$tool" >/dev/null 2>&1; then
      printf "Rillway pre-commit: mise 環境找不到 %s；請在專案根目錄執行 mise install。\n" "$tool" >&2
      exit 127
    fi
  done

  export GOCACHE="$repository/.cache/go-build"
  export GOMODCACHE="$repository/.cache/gomod"
  export GOPATH="$repository/.cache/go"
  export GOLANGCI_LINT_CACHE="$repository/.cache/golangci-lint"
  # Do not inherit live-test tags, a host workspace, or Git paths that point back
  # to the original checkout. This is a single-module, non-mutating check.
  export GOFLAGS=-mod=readonly
  export GOWORK=off
  unset GIT_DIR GIT_WORK_TREE GIT_COMMON_DIR GIT_INDEX_FILE GIT_PREFIX
  unset RILLWAY_LIVE_CONFIG RILLWAY_LIVE_OUTBOUND RILLWAY_LIVE_TARGET
  cd "$snapshot"

  check() {
    if command -v rtk >/dev/null 2>&1; then
      rtk proxy "$@"
    else
      "$@"
    fi
  }
  check golangci-lint run --allow-serial-runners ./...
  check go test ./...
' rillway-pre-commit "$repository" "$snapshot"; then
  printf '%s\n' 'Rillway pre-commit: 已暫存內容的 lint 與測試通過。'
  completed=1
else
  result=$?
  printf '%s\n' 'Rillway pre-commit: 檢查未通過，提交已停止；請修正上方錯誤後重新暫存。若缺少工具，請先執行 mise install。' >&2
  exit "$result"
fi
