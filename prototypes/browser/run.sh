#!/usr/bin/env bash
# PROTOTYPE (#147), throwaway. One command per variant, from a checkout of
# the prototype/browser-147 branch:
#
#   prototypes/browser/run.sh htmx           # htmx + /ui fragments + Service Worker running the Go handlers as WASM
#                                            #   → http://127.0.0.1:8147/vocab (and the htmx decode form at /)
#   prototypes/browser/run.sh js             # plain-JS variant, page calls the Go WASM directly → /vocab-js
#   prototypes/browser/run.sh js tinygo      # the same with the TinyGo module
#
# Anything after is passed to the server, e.g. `-vocab testdata/vocab/yard.jsonl`
# for a server-side file, `-addr 127.0.0.1:9000`. Both variants are served
# by the same server; the argument picks which module the plain-JS page loads
# and which page to open. Measurements: prototypes/browser/measure.sh.
set -euo pipefail
cd "$(dirname "$0")/../.."
variant=${1:-htmx}
shift || true
tc=go
if [ "$variant" = js ] && { [ "${1:-}" = go ] || [ "${1:-}" = tinygo ]; }; then
  tc=$1
  shift
fi
case "$variant" in
htmx) page=/vocab ;;
js) page=/vocab-js ;;
*) echo "usage: $0 htmx | js [go|tinygo] [serve flags]" >&2; exit 2 ;;
esac
prototypes/browser/build.sh go >/dev/null
if [ "$tc" = tinygo ]; then prototypes/browser/build.sh tinygo >/dev/null; fi
echo "open http://127.0.0.1:8147$page  (Ctrl-C to stop)"
exec go run ./prototypes/browser/cmd/serve -core "$tc" "$@"
