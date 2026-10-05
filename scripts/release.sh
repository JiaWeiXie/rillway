#!/bin/sh
set -eu
release_version=${RILLWAY_RELEASE_VERSION:-0.1.0-dev}
# Only inert version text may enter linker arguments; never evaluate user input.
case "$release_version" in
  ''|*[!0-9A-Za-z.+-]*) printf '%s\n' 'Invalid release version' >&2; exit 1 ;;
esac
mkdir -p dist
go run scripts/release-notices.go --prepare
# Build metadata must describe exactly the reviewed source, including notices.
if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  if [ -n "$(git status --porcelain)" ]; then
    printf '%s\n' 'Release source is dirty; commit reviewed changes before building.' >&2
    git status --short >&2
    exit 1
  fi
fi
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  target_os=${target%/*}
  target_arch=${target#*/}
  CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" go build -trimpath -ldflags="-s -w -X main.version=$release_version" -o "dist/rillway-$target_os-$target_arch" ./cmd/rillway
done
go run scripts/release-notices.go --hashes-only
