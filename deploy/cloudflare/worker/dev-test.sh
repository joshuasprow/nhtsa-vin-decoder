#!/usr/bin/env bash
# The Worker under wrangler dev, on a container built from this tree: its
# routing, its cache and the cache key's release. Needs Docker, Go and the
# worker's node_modules (npm ci); no Cloudflare account.
#
#   deploy/cloudflare/worker/dev-test.sh
#
# Two images of this tree's nitrovin, differing only in decoder_version
# (one VCS-stamped, one built with -buildvcs=false, "(devel)"), are served
# in turn under one persisted wrangler dev state, and ../smoke.sh runs
# against each with the same frozen clock. The second image's first decode
# is a MISS though the URL already sits in the cache, under the first
# release's key; the first image again finds it there (a HIT), so the state
# did persist.
set -euo pipefail
# Job control: wrangler dev runs in a process group of its own, so stop()
# ends workerd and its container proxies with it, not npx alone.
set -m

worker=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$worker/../../.." && pwd)
port=${PORT:-8787}
tmp=$(mktemp -d)
cp "$worker/Dockerfile" "$tmp/Dockerfile"
cleanup() {
	[[ -n ${dev:-} ]] && stop
	cp "$tmp/Dockerfile" "$worker/Dockerfile"
	rm -rf "$tmp"
}
trap cleanup EXIT

# image <tag> <go build flags…>: this tree's nitrovin as the release image
# is built (the repository's Dockerfile, one platform).
image() {
	local tag=$1
	shift
	mkdir -p "$tmp/ctx/linux/amd64"
	(cd "$root" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath "$@" -o "$tmp/ctx/linux/amd64/nitrovin" ./cmd/nitrovin)
	cp "$root/LICENSE" "$root/NOTICE" "$tmp/ctx/"
	docker build -q --platform linux/amd64 -f "$root/Dockerfile" -t "$tag" "$tmp/ctx" >/dev/null
	echo "$tag: $(docker run --rm --platform linux/amd64 "$tag" info | jq -r .decoder_version)"
}

# serve <image>: wrangler dev with the worker's Dockerfile on <image>, its
# state in $tmp/state across runs.
serve() {
	sed "s|^FROM .*|FROM $1|" "$tmp/Dockerfile" >"$worker/Dockerfile"
	(cd "$worker" && exec npx wrangler dev --port "$port" --persist-to "$tmp/state" --show-interactive-dev-session=false) \
		>"$tmp/dev.log" 2>&1 &
	dev=$!
	for _ in $(seq 300); do
		grep -q 'Ready on' "$tmp/dev.log" && return
		kill -0 "$dev" 2>/dev/null || break
		sleep 1
	done
	cat "$tmp/dev.log" >&2
	echo "dev-test: wrangler dev did not start" >&2
	exit 1
}

# stop: wrangler dev's whole process group, then its port, free.
stop() {
	kill -TERM -- "-$dev" 2>/dev/null || true
	wait "$dev" 2>/dev/null || true
	dev=
	for _ in $(seq 30); do
		curl -s -o /dev/null "http://localhost:$port/" || return 0
		sleep 1
	done
	echo "dev-test: localhost:$port still answers after wrangler dev stopped" >&2
	exit 1
}

image nitrovin-dev:vcs
image nitrovin-dev:devel -buildvcs=false
export NOW
NOW=$(date -u +%Y-%m-%dT%H:%M:%SZ)
for run in nitrovin-dev:vcs:MISS nitrovin-dev:devel:MISS nitrovin-dev:vcs:HIT; do
	tag=${run%:*}
	echo "--- wrangler dev on $tag"
	serve "$tag"
	FIRST=${run##*:} "$worker/../smoke.sh" "http://localhost:$port" "$tag"
	# The keep-warm cron's handler.
	status=$(curl -sS -o /dev/null -w '%{http_code}' "http://localhost:$port/cdn-cgi/local/scheduled")
	[[ $status == 200 ]] || { echo "FAIL  scheduled: $status" >&2; exit 1; }
	echo "ok    scheduled (keep-warm): 200"
	stop
done
