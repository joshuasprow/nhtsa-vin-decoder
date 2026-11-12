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
# was deployed with, so nothing is stored). Both GETs go over one
# connection, so to one data center, whose cache it is (#131). The
# release's Service Worker module under /ui/r/ is held to them too, except
# that its first GET may be a HIT whenever the second must be: its URL is
# the release's, not new. TOOLS is the release's docs/api/mcp-tools.json
# (default this tree's), whose tools /mcp must list. The pages are never
# cached: the page without JavaScript, its fragment for htmx, and /sw.js,
# which keeps its own no-cache. Over https it also checks HSTS,
# always-HTTPS, the www redirect and the TLS floor.
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
	lower "$name"
}

# lower <name>: a response's header names in lower case, without \r.
lower() {
	tr -d '\r' <"$tmp/$1.h" | awk 'BEGIN{FS=OFS=": "} {$1=tolower($1); print}' >"$tmp/$1.h2"
	mv "$tmp/$1.h2" "$tmp/$1.h"
}

# colo <name>: the data center that answered a response, its cf-ray's
# suffix (ATL in …-ATL); empty off Cloudflare (wrangler dev).
colo() {
	local ray
	ray=$(header "$1" cf-ray)
	[[ $ray == *-* ]] && echo "${ray##*-}" || true
}

# cached <path> <check> <what> <first…>: two GETs of <path> over one
# connection, each answer held to <check> <name> <status> (as req leaves
# it under <name>), the first's cf-cache-status one of <first…>, the
# second's SECOND (default HIT). The Cache API is each data center's own,
# and two connections can end in two (#131): one connection ends in one.
# Should the two answers' cf-ray colos differ anyway, the pair says nothing
# of the cache and is asked again, at most three times; a first GET then
# may also be SECOND, the colos before having kept the answer.
cached() {
	local path=$1 check=$2 what=$3 attempt statuses s1 s2 one two c1 c2 ok want
	shift 3
	for attempt in 1 2 3; do
		statuses=$(curl -sS --max-time 60 -D "$tmp/one.h" -o "$tmp/one.b" -w '%{http_code} ' "$base$path" \
			--next -sS --max-time 60 -D "$tmp/two.h" -o "$tmp/two.b" -w '%{http_code}' "$base$path")
		read -r s1 s2 <<<"$statuses"
		lower one
		lower two
		"$check" one "$s1"
		"$check" two "$s2"
		one=$(header one cf-cache-status) two=$(header two cf-cache-status)
		c1=$(colo one) c2=$(colo two)
		ok=
		for want in "$@"; do [[ $one == "$want" ]] && ok=1; done
		((attempt > 1)) && [[ $one == "${SECOND:-HIT}" ]] && ok=1
		[[ -n $ok ]] || fail "GET $path: cf-cache-status $one; want $*"
		if [[ $c1 != "$c2" ]]; then
			printf 'again %s\n' "GET $path: answered in $c1, then $c2, so the cache is untested"
			continue
		fi
		[[ $two == "${SECOND:-HIT}" ]] ||
			fail "GET $path: cf-cache-status $one, then $two${c1:+, both in $c1}; want ${SECOND:-HIT}"
		pass "GET $path: 200, $what, cf-cache-status $one, then $two${c1:+, both in $c1}"
		return
	done
	fail "GET $path: each of three pairs over one connection answered in two data centers; the cache is untested"
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
decode() {
	local want_control="public, max-age=86400"
	[[ $(header "$1" cf-cache-status) == BYPASS ]] && want_control=no-store
	[[ $2 == 200 ]] || fail "GET $target: $2"
	cmp -s "$tmp/$1.b" "$tmp/want-decode.json" || fail "GET $target: body differs from \`nitrovin decode --now $now $vin\`"
	[[ $(header "$1" cache-control) == "$want_control" ]] || fail "GET $target: cache-control $(header "$1" cache-control); want $want_control"
}
cached "$target" decode "the image's bytes" "${FIRST:-MISS}"

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
status=$(req form "$base/?from.vin=$vin")
[[ $status == 200 && $(header form content-type) == "text/html; charset=utf-8" ]] || fail "GET /?from.vin=$vin: $status"
grep -qi '^access-control-' "$tmp/form.h" && fail "GET /?from.vin=$vin: CORS headers on the page"
pass "CORS: * on /v1 and /mcp, preflight 204; the page has none"

# Old links, to the decode form and to the pages before the one page,
# move to it.
status=$(req old "$base/?vin=$vin")
[[ $status == 301 && $(header old location) == "/?from.vin=$vin" ]] || fail "GET /?vin=$vin: $status $(header old location)"
status=$(req old "$base/ui/decode?vin=$vin")
[[ $status == 301 && $(header old location) == "/?from.vin=$vin" ]] || fail "GET /ui/decode?vin=$vin: $status $(header old location)"
status=$(req old "$base/vocab")
[[ $status == 301 && $(header old location) == "/" ]] || fail "GET /vocab: $status $(header old location)"
pass "GET /?vin=$vin and /ui/decode?vin=$vin: 301 to /?from.vin=$vin; /vocab: 301 to /"

# The page, never cached. Without JavaScript: a whole page, whose
# Vocabularies dialog says the visitor's own files need it and lists the
# server's catalog.
status=$(req page "$base/")
[[ $status == 200 && $(header page content-type) == "text/html; charset=utf-8" ]] || fail "GET /: $status $(header page content-type)"
[[ $(header page cache-control) == no-store ]] || fail "GET /: cache-control $(header page cache-control); want no-store"
grep -q '^<!doctype html>' "$tmp/page.b" || fail "GET /: not a whole page"
grep -qF 'Loading your own files needs JavaScript' "$tmp/page.b" ||
	fail "GET /: does not say that your own files need JavaScript (a build without scripts/sw.sh's module?)"
grep -qF 'data-where="vpic"' "$tmp/page.b" || fail "GET /: no catalog"
pass "GET /: 200, a whole page, no-store; says your own files need JavaScript, lists the catalog"

# A VIN as htmx asks it: the answer, a fragment, no page around it.
status=$(req frag -H 'HX-Request: true' "$base/?from.vin=$vin")
[[ $status == 200 && $(header frag content-type) == "text/html; charset=utf-8" ]] || fail "GET /?from.vin=$vin (htmx): $status"
[[ $(header frag vary) == HX-Request && $(header frag cache-control) == no-store ]] ||
	fail "GET /?from.vin=$vin (htmx): vary $(header frag vary), cache-control $(header frag cache-control)"
grep -qi '<!doctype\|<html' "$tmp/frag.b" && fail "GET /?from.vin=$vin (htmx): a whole page, not a fragment"
grep -qF '<section id="result"' "$tmp/frag.b" && grep -qF "value=\"$vin\"" "$tmp/frag.b" ||
	fail "GET /?from.vin=$vin (htmx): no answer for $vin"
pass "GET /?from.vin=$vin (htmx): 200, the answer as a fragment, no-store"

# The Service Worker: no-cache, never from the cache, the release's own,
# alone allowed WebAssembly.
status=$(req sw "$base/sw.js")
[[ $status == 200 && $(header sw cache-control) == no-cache && -z $(header sw cf-cache-status) ]] ||
	fail "GET /sw.js: $status, cache-control $(header sw cache-control), cf-cache-status $(header sw cf-cache-status)"
[[ $(header sw content-security-policy) == *"'wasm-unsafe-eval'"* ]] || fail "GET /sw.js: no 'wasm-unsafe-eval'"
sed -n 's/^self\.nitrovinConfig = \(.*\);$/\1/p' "$tmp/sw.b" >"$tmp/sw-config.json"
[[ $(identity "$tmp/sw-config.json") == "$want" ]] || fail "GET /sw.js: release $(identity "$tmp/sw-config.json"); want $want"
pass "GET /sw.js: 200, no-cache, never cached, 'wasm-unsafe-eval', the release's worker"

# Its module, under the release's assets: immutable, cached under the
# release. Its URL is not new, so a GET finds it cached whenever anyone
# asked for it before: a HIT passes wherever the second GET must be one.
module="$(jq -r .assets "$tmp/sw-config.json")/sw.wasm"
wasm() {
	[[ $2 == 200 && $(header "$1" content-type) == application/wasm ]] || fail "GET $module: $2 $(header "$1" content-type)"
	[[ $(head -c 4 "$tmp/$1.b" | od -An -tx1 | tr -d ' \n') == 0061736d ]] || fail "GET $module: not WebAssembly"
	[[ $(header "$1" cache-control) == "public, max-age=31536000, immutable" ]] ||
		fail "GET $module: cache-control $(header "$1" cache-control); want immutable"
}
if [[ ${SECOND:-HIT} == HIT ]]; then
	cached "$module" wasm "WebAssembly, immutable" "${FIRST:-MISS}" HIT
else
	cached "$module" wasm "WebAssembly, immutable" "${FIRST:-MISS}"
fi

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
