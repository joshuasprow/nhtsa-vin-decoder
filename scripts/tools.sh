#!/usr/bin/env bash
# The tool pins (mise.toml, mise.lock): whether the tools on PATH are the
# pinned ones, and which have newer releases upstream.
# CI (.github/workflows/tools.yml) and a workstation run the same commands.
#
#   scripts/tools.sh check      every tool mise.toml pins, run by bare name from PATH,
#                               reports exactly its pin; lists them all, then fails if
#                               any differs
#   scripts/tools.sh refuse     a copy of mise.lock with jq's checksum for this platform
#                               changed makes `mise install --locked` fail on it
#   scripts/tools.sh outdated   a Markdown table of the pins with a newer release
#                               upstream; prints nothing when every pin is current
#
# check expects mise's PATH (`mise install`, then a shell mise activates, or
# `mise exec -- scripts/tools.sh check`), and the Worker's node_modules
# (`npm ci` in deploy/cloudflare/worker), where wrangler's pin lives. A pin
# must be an exact version: a range would let two machines differ. check also
# requires every postgres:17 service image and scripts/oracle.sh's default to
# name one digest.
#
# refuse installs into a scratch directory of its own (mise's data and cache
# there too, so nothing installed already can answer) and fails unless
# mise refuses the download for its checksum.
#
# outdated asks mise (`mise outdated --bump`: the newest release, majors
# included), the npm registry for wrangler, for htmx (vendored in
# pkg/ui/static, its version pkg/ui/ui.go's htmxVersion) and for axe-core
# (vendored in cmd/nitrovin/testdata for TestForm's audits, its version
# cmd/nitrovin/browser_test.go's axeVersion), and Docker Hub for
# postgres:17's digest (with `docker buildx imagetools`). It reports and
# never bumps: a bump is an edit of mise.toml and `mise lock` (mise.toml's
# header), or for htmx and axe-core a new file, version and hash.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORKER="$ROOT/deploy/cloudflare/worker"

die() { echo "tools: $*" >&2; exit 1; }

usage() {
  sed -n '/^#   scripts/,/^#$/s/^#  //p' "$0" >&2
  exit 2
}

# The first x.y.z in the input.
semver() { awk '!found && match($0, /[0-9]+\.[0-9]+\.[0-9]+/) { print substr($0, RSTART, RLENGTH); found = 1 }'; }

# The command that prints a tool's version, by its name in mise.toml.
version_cmd() {
  case "$1" in
    go) echo "go version" ;;
    zig) echo "zig version" ;;
    goreleaser) echo "goreleaser --version" ;;
    cosign) echo "cosign version" ;;
    terraform) echo "terraform version" ;;
    node) echo "node --version" ;;
    jq) echo "jq --version" ;;
    yq) echo "yq --version" ;;
    uv) echo "uv --version" ;;
    github:modelcontextprotocol/registry) echo "mcp-publisher --version" ;;
    *) return 1 ;;
  esac
}

# The postgres:17 references the oracle runs: the workflows' service images
# and scripts/oracle.sh's default.
postgres_refs() {
  grep -hE '^[[:space:]]*image: postgres|ORACLE_IMAGE:-' "$ROOT"/.github/workflows/*.yml "$ROOT/scripts/oracle.sh" |
    grep -oE 'postgres:[^ "}]+' | sort -u
}

cmd_check() {
  local fail=0 tool want requested cmd got path pins jq refs
  cd "$ROOT"
  # The pinned jq by its install path: the jq on PATH is under test.
  jq=$(mise which jq)
  pins=$(mise ls --current --project --json | "$jq" -r 'to_entries[] | "\(.key)\t\(.value[0].version)\t\(.value[0].requested_version)"')
  [ -n "$pins" ] || die "mise lists no tools for $ROOT/mise.toml"
  printf '%-38s %-9s %-9s %s\n' tool pin reports path
  while IFS=$'\t' read -r tool want requested; do
    if [ "$requested" != "$want" ]; then
      echo "tools: $tool is pinned as \"$requested\", not an exact version ($want)" >&2
      fail=1
    fi
    cmd=$(version_cmd "$tool") || { echo "tools: no version command for $tool: add it to scripts/tools.sh" >&2; fail=1; continue; }
    path=$(command -v "${cmd%% *}") || { echo "tools: ${cmd%% *} ($tool $want) is not on PATH" >&2; fail=1; continue; }
    got=$($cmd 2>&1 | semver)
    printf '%-38s %-9s %-9s %s\n' "$tool" "$want" "${got:-?}" "$path"
    [ "$got" = "$want" ] || { echo "tools: \`$cmd\` reports ${got:-no version}, the pin is $want" >&2; fail=1; }
  done <<<"$pins"

  want=$("$jq" -r '.devDependencies.wrangler' "$WORKER/package.json")
  path="$WORKER/node_modules/.bin/wrangler"
  if [ -x "$path" ]; then
    got=$("$path" --version 2>&1 | semver)
    printf '%-38s %-9s %-9s %s\n' "wrangler (package.json)" "$want" "${got:-?}" "${path#"$ROOT/"}"
    [ "$got" = "$want" ] || { echo "tools: wrangler reports ${got:-no version}, package.json pins $want" >&2; fail=1; }
  else
    echo "tools: no wrangler in $WORKER/node_modules: run npm ci there" >&2
    fail=1
  fi

  refs=$(postgres_refs)
  printf '%-38s %s\n' "postgres (image)" "${refs//$'\n'/ }"
  if [ "$(grep -c . <<<"$refs")" -ne 1 ] || [[ $refs != postgres:17@sha256:* ]]; then
    echo "tools: the workflows and scripts/oracle.sh must name one postgres:17@sha256:<digest>, not: ${refs//$'\n'/ }" >&2
    fail=1
  fi
  return "$fail"
}

cmd_outdated() {
  local rows pinned latest ref digest
  cd "$ROOT"
  rows=$(mise outdated --bump --json --no-global |
    jq -r 'to_entries[] | .value | select(.latest != null and .latest != .requested) | "| \(.name) | \(.requested) | \(.latest) |"')

  pinned=$(jq -r '.devDependencies.wrangler' "$WORKER/package.json")
  latest=$(npm view wrangler version)
  [ "$latest" = "$pinned" ] || rows+=$'\n'"| wrangler (deploy/cloudflare/worker/package.json) | $pinned | $latest |"

  pinned=$(sed -n 's/^const htmxVersion = "\(.*\)"$/\1/p' pkg/ui/ui.go)
  [ -n "$pinned" ] || die "no htmxVersion in pkg/ui/ui.go"
  latest=$(npm view htmx.org version)
  [ "$latest" = "$pinned" ] || rows+=$'\n'"| htmx (pkg/ui/static/htmx.min.js) | $pinned | $latest |"

  pinned=$(sed -n 's/^const axeVersion = "\(.*\)"$/\1/p' cmd/nitrovin/browser_test.go)
  [ -n "$pinned" ] || die "no axeVersion in cmd/nitrovin/browser_test.go"
  latest=$(npm view axe-core version)
  [ "$latest" = "$pinned" ] || rows+=$'\n'"| axe-core (cmd/nitrovin/testdata/axe.min.js) | $pinned | $latest |"

  ref=$(postgres_refs | head -n 1)
  digest=$(docker buildx imagetools inspect postgres:17 --format '{{json .Manifest}}' | jq -r .digest)
  [ "$ref" = "postgres:17@$digest" ] || rows+=$'\n'"| postgres:17 (image digest) | \`${ref#postgres:17@}\` | \`$digest\` |"

  rows=$(sed '/^$/d' <<<"$rows")
  [ -n "$rows" ] || return 0
  echo '| Tool | Pinned | Newest |'
  echo '| --- | --- | --- |'
  echo "$rows"
}

cmd_refuse() {
  # scratch stays global: the EXIT trap reads it after the function returns.
  local os arch platform sum bad
  case "$(uname -s)" in Darwin) os=macos ;; Linux) os=linux ;; *) die "no mise platform for $(uname -s)" ;; esac
  case "$(uname -m)" in arm64 | aarch64) arch=arm64 ;; x86_64) arch=x64 ;; *) die "no mise platform for $(uname -m)" ;; esac
  platform="$os-$arch"
  sum=$(awk -v t="[tools.jq.\"platforms.$platform\"]" '$0 == t { f = 1; next } f && /^checksum/ { split($0, a, "\""); print a[2]; exit }' "$ROOT/mise.lock")
  [[ $sum == sha256:* ]] || die "mise.lock has no jq checksum for $platform"
  # The same digest with its last hex digit changed.
  bad="${sum%?}$([ "${sum: -1}" = 0 ] && echo 1 || echo 0)"
  scratch=$(mktemp -d)
  trap 'rm -rf "$scratch"' EXIT
  cp "$ROOT/mise.toml" "$scratch/"
  awk -v s="$sum" -v b="$bad" '{ i = index($0, s); if (i) $0 = substr($0, 1, i - 1) b substr($0, i + length(s)); print }' \
    "$ROOT/mise.lock" >"$scratch/mise.lock"
  if (cd "$scratch" && MISE_DATA_DIR="$scratch/data" MISE_CACHE_DIR="$scratch/cache" MISE_TRUSTED_CONFIG_PATHS="$scratch" \
        mise install --locked jq) >"$scratch/install.log" 2>&1; then
    die "mise installed jq for $platform from a mise.lock whose checksum is wrong"
  fi
  grep -q 'Checksum mismatch' "$scratch/install.log" || { cat "$scratch/install.log" >&2; die "mise refused jq, but not for its checksum"; }
  echo "jq for $platform: mise.lock says $bad, the download is $sum: refused"
}

case "${1:-}" in
  check) cmd_check ;;
  refuse) cmd_refuse ;;
  outdated) cmd_outdated ;;
  *) usage ;;
esac
