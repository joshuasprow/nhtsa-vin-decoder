#!/usr/bin/env bash
# Throwaway (#124): miss-path latency of https://nitrovin.app.
#   .github/latency-124.sh [n=20] [base=https://nitrovin.app]
# n decodes of one VIN, each under a `now` never asked before (a cache MISS),
# over one reused connection, then the same n URLs again (HITs). Per request:
# cache status, edge colo (cf-ray), wait = time to first byte after the
# request was sent (TLS and connect excluded), and total.
set -euo pipefail
n=${1:-20}
base=${2:-https://nitrovin.app}
vin=1HGCM82633A004352
# A random second within ~30 years: a `now` nobody has decoded under.
t0=$(( 1000000000 + (RANDOM * 32768 + RANDOM) % 900000000 ))
urls=()
for i in $(seq 0 $((n - 1))); do
	now=$(jq -rn --argjson t $((t0 + i)) '$t | todate')
	urls+=("$base/v1/decode/$vin?now=$now")
done
fmt='%{http_code} %header{cf-cache-status} %header{cf-ray} %{time_pretransfer} %{time_starttransfer} %{time_total}\n'
run() {
	args=()
	for u in "${urls[@]}"; do args+=(-o /dev/null -w "$fmt" "$u"); done
	curl -sS --max-time 60 "${args[@]}"
}
summary() {
	# stdin: rows "status cache ray pre start total"; one line per cache status.
	awk '{ wait = ($5 - $4) * 1000; split($3, r, "-"); key = $2 " " r[2]; print key, wait, $6 * 1000 }' |
		sort -k1,2 -k3,3n |
		awk '
		function flush() {
			if (c == 0) return
			printf "%-6s %-4s n=%-3d wait ms: min %4.0f p50 %4.0f p90 %4.0f max %4.0f\n",
				k1, k2, c, w[1], w[int((c + 1) / 2)], w[int(c * 0.9 + 0.999)], w[c]
		}
		{ if ($1 " " $2 != k) { flush(); k = $1 " " $2; k1 = $1; k2 = $2; c = 0 } w[++c] = $3 }
		END { flush() }'
}
echo "# $base, $(date -u +%FT%TZ), $(curl -sS "$base/v1/info" | jq -r .decoder_version), from $(curl -sS https://www.cloudflare.com/cdn-cgi/trace | awk -F= '$1=="colo"||$1=="loc"{printf "%s=%s ", $1, $2}')"
miss=$(run)
hit=$(run)
echo "$miss" | summary
echo "$hit" | summary
echo "# raw: status cache ray pretransfer starttransfer total (s)"
printf '%s\n%s\n' "$miss" "$hit"
