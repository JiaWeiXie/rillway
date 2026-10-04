#!/bin/sh
set -eu
mkdir -p dist
go run scripts/release-notices.go --prepare
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  target_os=${target%/*}
  target_arch=${target#*/}
  CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" go build -trimpath -ldflags='-s -w' -o "dist/rillway-$target_os-$target_arch" ./cmd/rillway
done
go run scripts/release-notices.go --hashes-only
