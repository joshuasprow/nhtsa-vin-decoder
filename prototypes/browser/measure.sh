#!/usr/bin/env bash
# PROTOTYPE (#147): every number on #147, from a checkout of
# prototype/browser-147:
#
#   prototypes/browser/measure.sh
#
# Builds the modules (Go and TinyGo), the EPA vocabulary (cmd/nitrovin-epa
# over EPA's vehicles.csv.zip, downloaded) and the synthetic eBay-scale one
# (cmd/synth), starts the server on :8147 with yard, classics and EPA as
# server-side files, measures sizes (raw, gzip -9, brotli -q 11), then
# drives headless Chromium (measure.mjs) for timings and JS on/off
# screenshots. Writes results/: sizes.tsv, measurements.json, *.png,
# tinygo-sw.txt (the TinyGo Service Worker module failing at start),
# server.log (every request that reached the server, with its body size).
set -euo pipefail
cd "$(dirname "$0")/../.."
p=prototypes/browser
mkdir -p "$p/data" "$p/results"

$p/build.sh go >/dev/null
$p/build.sh tinygo >/dev/null
for m in hello vocab contract template nethttp; do
  GOOS=js GOARCH=wasm go build -trimpath -ldflags='-s -w' -o "$p/build/go/probe-$m.wasm" "./$p/wasm/probe/$m"
done

if [ ! -s "$p/data/epa-vehicles.jsonl" ]; then
  curl -sSL -o "$p/data/vehicles.csv.zip" https://www.fueleconomy.gov/feg/epadata/vehicles.csv.zip
  go run ./cmd/nitrovin-epa -o "$p/data/epa-vehicles.jsonl" "$p/data/vehicles.csv.zip"
fi
if [ ! -s "$p/data/ebay-synthetic.jsonl" ]; then
  go run ./$p/cmd/synth -n 150000 -o "$p/data/ebay-synthetic.jsonl"
fi
go run ./cmd/nitrovin vocab check "$p/data/epa-vehicles.jsonl" "$p/data/ebay-synthetic.jsonl"

go build -o "$p/build/serve" "./$p/cmd/serve"
"$p/build/serve" -vocab testdata/vocab/yard.jsonl,testdata/vocab/classics.jsonl,$p/data/epa-vehicles.jsonl >"$p/results/server.log" 2>&1 &
server=$!
trap 'kill $server 2>/dev/null || true' EXIT
for _ in $(seq 100); do if curl -s -o /dev/null http://127.0.0.1:8147/; then break; fi; sleep 0.2; done
curl -s -H 'Accept-Encoding: identity' -o "$p/build/spine.json" http://127.0.0.1:8147/ui/spine.json

size() { # name file
  printf '%s\t%s\t%s\t%s\n' "$1" "$(wc -c <"$2" | tr -d ' ')" "$(gzip -9c "$2" | wc -c | tr -d ' ')" "$(brotli -q 11 -c "$2" | wc -c | tr -d ' ')"
}
{
  printf 'artifact\traw\tgzip-9\tbrotli-11\n'
  size "Go sw.wasm (vocab+translator+/ui handlers: html/template, net/http)" "$p/build/go/sw.wasm"
  size "Go core.wasm (vocab+translator, JSON API)" "$p/build/go/core.wasm"
  size "TinyGo core.wasm (vocab+translator, JSON API)" "$p/build/tinygo/core.wasm"
  size "TinyGo sw.wasm (builds; panics at start)" "$p/build/tinygo/sw.wasm"
  size "probe: Go runtime alone (hello)" "$p/build/go/probe-hello.wasm"
  size "probe: Go pkg/vocab alone" "$p/build/go/probe-vocab.wasm"
  size "probe: Go pkg/vocab+pkg/contract" "$p/build/go/probe-contract.wasm"
  size "probe: Go vocab+contract+html/template" "$p/build/go/probe-template.wasm"
  size "probe: Go net/http alone" "$p/build/go/probe-nethttp.wasm"
  size "Go wasm_exec.js" "$p/build/go/wasm_exec.js"
  size "TinyGo wasm_exec.js" "$p/build/tinygo/wasm_exec.js"
  size "htmx 2.0.11 (htmx.min.js)" "$p/static/htmx-2.0.11.min.js"
  size "spine bundle 2026_10 (JSON)" "$p/build/spine.json"
  size "EPA vocabulary (5,747 entries)" "$p/data/epa-vehicles.jsonl"
  size "synthetic eBay-scale vocabulary (150,000 entries)" "$p/data/ebay-synthetic.jsonl"
} | tee "$p/results/sizes.tsv"

mise x node@24.21.0 -- node "$p/tinygo-sw.mjs" 2>&1 | grep -v "^    at" | tee "$p/results/tinygo-sw.txt" || true
mise x node@24.21.0 -- node "$p/measure.mjs"
