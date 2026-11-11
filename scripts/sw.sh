#!/usr/bin/env bash
# Build the browser's Service Worker module (cmd/nitrovin-sw, ADR 0013):
# package ui's vocabulary pages for js/wasm, with Go's toolchain, and the
# wasm_exec.js of the same Go. `nitrovin serve` embeds them from
# cmd/nitrovin/sw (the default <dir>) and publishes them under
# /ui/r/<release>/; a build without them serves its page without the
# visitor's own files. goreleaser's before hook (.goreleaser.yaml) and
# contract.yml run it before building or testing cmd/nitrovin.
#
#   scripts/sw.sh [<dir>]
#
# Reproducible: -trimpath and -buildvcs=false, so one checkout and one Go
# build the same bytes wherever they run. The release names the module's
# URL, and browsers keep it a year.
set -euo pipefail
cd "$(dirname "$0")/.."
dir="${1:-cmd/nitrovin/sw}"
mkdir -p "$dir"
GOOS=js GOARCH=wasm go build -trimpath -buildvcs=false -ldflags='-s -w' -o "$dir/sw.wasm" ./cmd/nitrovin-sw
install -m 0644 "$(go env GOROOT)/lib/wasm/wasm_exec.js" "$dir/wasm_exec.js"
