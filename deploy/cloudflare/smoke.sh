#!/usr/bin/env bash
# Smoke-test a nitrovin host from outside: deploy.yml runs it against
# https://nitrovin.app after every deploy, worker/dev-test.sh against
# wrangler dev. It prints one line per check and exits 1 on the first
# failure.
#
#   deploy/cloudflare/smoke.sh <base-url> <image>
#
# <image> is the image the host should be running (a local tag or
# ghcr.io/joshuasprow/nitrovin:<tag>@<digest>): /v1/info must report its
# release, and a decode through the host must equal the image's own decode
# of the same request, byte for byte. NOW (default: now, to the second) is
# the decode's frozen clock, which makes its URL new, so its first GET is a
# MISS and its second a HIT; FIRST and SECOND name others (HIT: the cache
# was filled already; BYPASS: the container is not the release the Worker
# was deployed with, so nothing is stored). TOOLS
# is the release's docs/api/mcp-tools.json (default this tree's), whose
# tools /mcp must list. Over https it also checks HSTS, always-HTTPS, the
# www redirect and the TLS floor.
set -euo pipefail

base=${1:?usage: smoke.sh <base-url> <image>}
image=${2:?usage: smoke.sh <base-url> <image>}
base=${base%/}
now=${NOW:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}
vin=1HGCM82633A004352
root=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

pass() { printf 'ok    %s\n' "$*"; }
fail() {
	printf 'FAIL  %s\n' "$*" >&2
	exit 1
}

# req <name> <curl args…>: the response's headers in $tmp/<name>.h (lower
# case names), its body in $tmp/<name>.b, its status printed.
req() {
	local name=$1
	shift
	curl -sS --max-time 60 -D "$tmp/$name.h" -o "$tmp/$name.b" -w '%{http_code}' "$@"
	tr -d '\r' <"$tmp/$name.h" | awk 'BEGIN{FS=OFS=": "} {$1=tolower($1); print}' >"$tmp/$name.h2"
	mv "$tmp/$name.h2" "$tmp/$name.h"
}

# header <name> <header>: the header's value in a response, empty if absent.
header() { awk -v h="$2" 'BEGIN{FS=": "} $1==h {sub(/^[^:]*: /, ""); v=$0} END{print v}' "$tmp/$1.h"; }

docker_run() { docker run --rm --platform linux/amd64 "$image" "$@"; }

# Wait until the host answers: a first deploy's custom domain (DNS and
# certificate), or a new container's start, takes a few minutes. A rollout
# is deploy.yml's to wait for (rollout-wait.sh): the old container answers.
for i in $(seq 30); do
	curl -fsS --max-time 10 -o /dev/null "$base/healthz" 2>/dev/null && break
	((i < 30)) || fail "GET /healthz: no answer in 5 minutes"
	sleep 10
done
pass "GET /healthz: answering"

# The release: decoder version, data month and artifact hash.
docker_run info >"$tmp/want-info.json"
identity() { jq -c '{decoder_version, data_month, artifact_hash}' "$1"; }
want=$(identity "$tmp/want-info.json")
status=$(req info "$base/v1/info")
[[ $status == 200 ]] || fail "GET /v1/info: $status"
got=$(identity "$tmp/info.b")
[[ $got == "$want" ]] || fail "GET /v1/info: $got; the image reports $want"
pass "GET /v1/info: $got"

# A decode: the image's bytes, a MISS (or FIRST), then a HIT (or SECOND).
docker_run decode --now "$now" "$vin" >"$tmp/want-decode.json"
target="/v1/decode/$vin?now=$now"
for want_cache in "${FIRST:-MISS}" "${SECOND:-HIT}"; do
	status=$(req decode "$base$target")
	cache=$(header decode cf-cache-status)
	want_control="public, max-age=86400"
	[[ $want_cache == BYPASS ]] && want_control=no-store
	[[ $status == 200 ]] || fail "GET $target: $status"
	cmp -s "$tmp/decode.b" "$tmp/want-decode.json" || fail "GET $target: body differs from \`nitrovin decode --now $now $vin\`"
	[[ $cache == "$want_cache" ]] || fail "GET $target: cf-cache-status $cache; want $want_cache"
	[[ $(header decode cache-control) == "$want_control" ]] || fail "GET $target: cache-control $(header decode cache-control); want $want_control"
	pass "GET $target: 200, the image's bytes, cf-cache-status $cache"
done

# Never cached: a POST, a refusal.
for _ in 1 2; do
	status=$(req post -X POST -H 'Content-Type: application/json' --data "[{\"vin\":\"$vin\",\"now\":\"$now\"}]" "$base/v1/decode")
	[[ $status == 200 && $(header post cf-cache-status) != HIT ]] || fail "POST /v1/decode: $status, cf-cache-status $(header post cf-cache-status)"
done
pass "POST /v1/decode: 200 twice, never from the cache"
bad="/v1/decode/${vin}1"
for _ in 1 2; do
	status=$(req bad "$base$bad")
	[[ $status == 400 && $(header bad content-type) == application/problem+json ]] || fail "GET $bad: $status $(header bad content-type)"
	[[ $(header bad cf-cache-status) != HIT && $(header bad cache-control) == no-store ]] ||
		fail "GET $bad: cf-cache-status $(header bad cf-cache-status), cache-control $(header bad cache-control)"
done
pass "GET $bad: 400 application/problem+json twice, no-store"

# MCP over Streamable HTTP: tools/list is mcp-tools.json's tools.
tools=${TOOLS:-$root/docs/api/mcp-tools.json}
status=$(req mcp -X POST -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
	--data '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' "$base/mcp")
[[ $status == 200 ]] || fail "POST /mcp tools/list: $status"
cmp -s <(jq -c .result.tools "$tmp/mcp.b") <(jq -c .tools "$tools") ||
	fail "POST /mcp tools/list: tools differ from $tools"
pass "POST /mcp tools/list: mcp-tools.json's tools"

# CORS on /v1 and /mcp, not on the pages.
[[ $(header info access-control-allow-origin) == '*' ]] || fail "GET /v1/info: no Access-Control-Allow-Origin: *"
status=$(req preflight -X OPTIONS -H 'Origin: https://example.com' -H 'Access-Control-Request-Method: POST' "$base/mcp")
[[ $status == 204 && $(header preflight access-control-allow-methods) == POST ]] || fail "OPTIONS /mcp: $status"
status=$(req form "$base/ui/decode?vin=$vin")
[[ $status == 200 && $(header form content-type) == "text/html; charset=utf-8" ]] || fail "GET /ui/decode?vin=$vin: $status"
grep -qi '^access-control-' "$tmp/form.h" && fail "GET /ui/decode?vin=$vin: CORS headers on the form"
pass "CORS: * on /v1 and /mcp, preflight 204; the form has none"

# An old link to the form redirects to it.
status=$(req old "$base/?vin=$vin")
[[ $status == 301 && $(header old location) == "/ui/decode?vin=$vin" ]] || fail "GET /?vin=$vin: $status $(header old location)"
pass "GET /?vin=$vin: 301 to /ui/decode?vin=$vin"

[[ $base == https://* ]] || exit 0
host=${base#https://}
status=$(req root "$base/")
hsts=$(header root strict-transport-security)
[[ $status == 200 && $hsts == *max-age=* ]] || fail "GET /: $status, strict-transport-security '$hsts'"
pass "GET /: 200, strict-transport-security: $hsts"
status=$(req plain "http://$host/v1/info")
[[ $status == 301 && $(header plain location) == "https://$host/v1/info" ]] || fail "http://$host/v1/info: $status $(header plain location)"
pass "http://$host/v1/info: 301 to https"
status=$(req www "https://www.$host/v1/info?x=1")
[[ $status == 301 && $(header www location) == "https://$host/v1/info?x=1" ]] || fail "https://www.$host/v1/info?x=1: $status $(header www location)"
pass "https://www.$host/v1/info?x=1: 301 to https://$host/v1/info?x=1"
curl -sS --max-time 30 -o /dev/null --tlsv1.2 --tls-max 1.2 "$base/healthz" || fail "TLS 1.2 refused"
if curl -sS --max-time 30 -o /dev/null --tlsv1.0 --tls-max 1.1 "$base/healthz" 2>/dev/null; then
	fail "TLS 1.1 accepted"
fi
pass "TLS: 1.2 accepted, 1.1 refused"
