#!/usr/bin/env bash
# Fetch EPA's vocabulary (epa-vehicles.jsonl) of the data month go.mod pins
# from its nitrovin-data release, checked against that release's
# checksums.txt, into cmd/nitrovin/sw (the default <dir>), beside the
# Service Worker module scripts/sw.sh builds. `nitrovin serve` embeds it,
# publishes it under /ui/r/<release>/ and offers it on /vocab as a
# one-click load into the browser (ADR 0013); a build without it offers
# none. goreleaser's before hook (.goreleaser.yaml) runs it.
#
#   scripts/epa.sh [<dir>]
set -euo pipefail
cd "$(dirname "$0")/.."
dir="${1:-cmd/nitrovin/sw}"
version=$(go list -m -f '{{.Version}}' github.com/joshuasprow/nitrovin-data)
base="https://github.com/joshuasprow/nitrovin-data/releases/download/$version"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL --retry 3 -o "$tmp/epa-vehicles.jsonl" "$base/epa-vehicles.jsonl"
curl -fsSL --retry 3 -o "$tmp/checksums.txt" "$base/checksums.txt"
want=$(awk '$2 == "epa-vehicles.jsonl" { print $1 }' "$tmp/checksums.txt")
if command -v sha256sum >/dev/null; then
  got=$(sha256sum "$tmp/epa-vehicles.jsonl" | awk '{ print $1 }')
else
  got=$(shasum -a 256 "$tmp/epa-vehicles.jsonl" | awk '{ print $1 }')
fi
if [[ -z $want || $got != "$want" ]]; then
  echo "scripts/epa.sh: epa-vehicles.jsonl of nitrovin-data $version has sha256 $got; checksums.txt says ${want:-nothing}" >&2
  exit 1
fi
mkdir -p "$dir"
install -m 0644 "$tmp/epa-vehicles.jsonl" "$dir/epa-vehicles.jsonl"
