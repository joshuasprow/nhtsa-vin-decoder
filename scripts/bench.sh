#!/usr/bin/env bash
# The README's benchmarks (docs/benchmarks.md): every row of the table, run
# on one machine over one corpus, each trial appended as one JSON line to
# <work>/trials.jsonl, and `report` turning that into docs/benchmarks.json.
# The method is ultravin's (github.com/blackthorn-interstellar/ultravin,
# docs/BENCHMARKS.md and scripts/bench/, MIT); no ultravin code is copied.
# ultravin's own rows run from a checkout of it, $ULTRAVIN.
#
#   scripts/bench.sh corpus <work> [<count>]      generate the corpus with ultravin's generator, sort it,
#                                                 draw the sample; writes corpus.txt, corpus-sorted.txt,
#                                                 sample.txt and corpus.json into <work>
#   scripts/bench.sh go <work> <workers> random|sorted
#                                                 nitrovin, Go module in process (cmd/nitrovin-bench)
#   scripts/bench.sh cli <work> <nitrovin>        nitrovin, `nitrovin decode -` over the corpus, GOMAXPROCS=1
#   scripts/bench.sh ultravin <work> <workers> random|sorted
#                                                 ultravin's examples/throughput, its headline command
#   scripts/bench.sh corgi <work> v2|v3           corgi in Node 22 over the sample (scripts/bench/corgi.mjs)
#   scripts/bench.sh postgres <work>              NHTSA spvindecode over the sample: scripts/oracle.sh decode stock
#   scripts/bench.sh mssql-up <dir>               start SQL Server 2022 (linux/amd64) and restore
#                                                 <dir>/vPICList_lite_<month>.bak
#   scripts/bench.sh mssql <work>                 NHTSA spVinDecode over the sample, one sqlcmd session
#   scripts/bench.sh mssql-down                   remove the SQL Server container
#   scripts/bench.sh agree <work>                 share of nitrovin's and ultravin's decodes of the sample
#                                                 that agree with the stock and freshened oracle
#   scripts/bench.sh process <work> <nitrovin>    a one-VIN `nitrovin decode` process, 11 runs
#   scripts/bench.sh all <work> <nitrovin>        two trials of every throughput row, the second round
#                                                 in reverse order
#   scripts/bench.sh report <work> <out.json>     every trial, and each row's median
#
# Environment: ULTRAVIN, the ultravin checkout (built: target/release/examples/throughput,
# and `uv run --frozen maturin develop --uv --release --locked` for its Python package);
# ORACLE_CONTAINER as scripts/oracle.sh (the oracle up, restored and freshened);
# MSSQL_CONTAINER (default nitrovin-bench-mssql). Before each trial the script waits
# up to BENCH_WAIT seconds (300) for a one-minute load average at most BENCH_MAX_LOAD
# (4), and records the load and the busiest processes either way.
#
# Clock: nitrovin and ultravin decode at 2026-09-01T00:00:00Z, ultravin's
# headline clock; corgi and the SQL procedures have none to freeze.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
NOW=2026-09-01T00:00:00Z
NOW_MICROS=1788220800000000
SAMPLE=2000
SAMPLE_SEED=162
MSSQL_CONTAINER="${MSSQL_CONTAINER:-nitrovin-bench-mssql}"
MSSQL_IMAGE=mcr.microsoft.com/mssql/server:2022-latest
MSSQL_PASSWORD='Bench!nitrovin1'
MSSQL_DB=vPICList_Lite
ONE_VIN=1HGCM82633A004352

die() { echo "bench: $*" >&2; exit 1; }
usage() { sed -n '/^#   scripts/,/^#$/s/^#  //p' "$0" >&2; exit 2; }

sha256() {
  if command -v sha256sum >/dev/null; then sha256sum "$1" | cut -d' ' -f1
  else shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

ultravin_dir() {
  [ -n "${ULTRAVIN:-}" ] || die "set ULTRAVIN to an ultravin checkout"
  [ -x "$ULTRAVIN/target/release/examples/throughput" ] ||
    die "$ULTRAVIN/target/release/examples/throughput: build it (cargo build -p ultravin --release --example throughput --locked)"
  echo "$ULTRAVIN"
}

# The harness, built from this checkout into <work>.
harness() {
  (cd "$ROOT" && go build -o "$1/nitrovin-bench" ./cmd/nitrovin-bench)
  echo "$1/nitrovin-bench"
}

load1() {
  if [ -r /proc/loadavg ]; then cut -d' ' -f1 /proc/loadavg
  else sysctl -n vm.loadavg | awk '{ print $2 }'
  fi
}

# Wait for a quiet host, then print {"load", "waited", "busy"} for the record.
quiet() {
  local max="${BENCH_MAX_LOAD:-4}" deadline=$((SECONDS + ${BENCH_WAIT:-300})) waited=$SECONDS
  while awk -v l="$(load1)" -v m="$max" 'BEGIN { exit !(l > m) }' && [ "$SECONDS" -lt "$deadline" ]; do
    sleep 10
  done
  ps -Ao pcpu=,ucomm= | sort -rn | awk 'NR <= 5 { $1 = $1; print }' |
    jq -Rn --arg load "$(load1)" --argjson waited $((SECONDS - waited)) \
      '{load: ($load | tonumber), waited: $waited, busy: [inputs]}'
}

# Append one trial: record <work> <row> <params json> <result json> <host json>.
record() {
  local work="$1" row="$2" params="$3" result="$4" host="$5" n
  n=$( (grep -F "\"row\":\"$row\"" "$work/trials.jsonl" 2>/dev/null || true) |
    jq -s --argjson p "$params" '[.[] | select(.params == $p)] | length')
  jq -cn --arg row "$row" --argjson params "$params" --argjson result "$result" --argjson host "$host" \
    --arg at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --argjson trial $((n + 1)) \
    '{row: $row, params: $params, trial: $trial, at: $at, host: $host, result: $result}' |
    tee -a "$work/trials.jsonl"
}

input() {
  case "$2" in
    random) echo "$1/corpus.txt" ;;
    sorted) echo "$1/corpus-sorted.txt" ;;
    *) die "input must be random or sorted, got '$2'" ;;
  esac
}

cmd_corpus() {
  local work="${1:?work}" count="${2:-20000000}" uv
  uv=$(ultravin_dir)
  mkdir -p "$work"
  work=$(cd "$work" && pwd)
  (cd "$uv" && uv run --frozen python -m scripts.bench.large_corpus --count "$count" \
    --out "$work/corpus.txt" --manifest "$work/corpus.manifest.json")
  LC_ALL=C sort "$work/corpus.txt" >"$work/corpus-sorted.txt"
  uv run --no-project python - "$work/corpus.txt" "$work/sample.txt" "$SAMPLE" "$SAMPLE_SEED" <<'PY'
import random, sys
src, dst, n, seed = sys.argv[1], sys.argv[2], int(sys.argv[3]), int(sys.argv[4])
lines = open(src).read().split()
pick = sorted(random.Random(seed).sample(range(len(lines)), n))
open(dst, "w").write("".join(lines[i] + "\n" for i in pick))
PY
  jq -n --slurpfile m "$work/corpus.manifest.json" \
    --arg sorted "$(sha256 "$work/corpus-sorted.txt")" --arg sample "$(sha256 "$work/sample.txt")" \
    --arg ultravin "$(git -C "$uv" rev-parse HEAD)" \
    --argjson n "$SAMPLE" --argjson seed "$SAMPLE_SEED" \
    '{ultravin_commit: $ultravin, generator: $m[0].generator, count: $m[0].count, seed: $m[0].seed,
      frozen_now: $m[0].frozen_now, data_month: $m[0].database.data_month,
      check_digit_failures: $m[0].check_digit_failures, diversity: $m[0].diversity,
      sha256: $m[0].corpus.sha256, sorted_sha256: $sorted,
      sample: {vins: $n, seed: $seed, sha256: $sample}}' >"$work/corpus.json"
  cat "$work/corpus.json"
}

cmd_go() {
  local work="${1:?work}" workers="${2:?workers}" order="${3:?random|sorted}" bin host result
  bin=$(harness "$work")
  host=$(quiet)
  result=$("$bin" throughput -workers "$workers" -now "$NOW" "$(input "$work" "$order")")
  record "$work" nitrovin-go "{\"workers\":$workers,\"input\":\"$order\"}" "$result" "$host"
}

# `nitrovin decode -` stops at a VIN whose decode fails (#165), so this row
# decodes the corpus without the VINs the Go module refuses
# (corpus-cli.txt, refused.txt), and checks it wrote one line per VIN.
cmd_cli() {
  local work="${1:?work}" nitrovin="${2:?nitrovin}" bin host run vins
  bin=$(harness "$work")
  if [ ! -e "$work/corpus-cli.txt" ]; then
    "$bin" refused -now "$NOW" "$work/corpus.txt" >"$work/refused.txt"
    grep -vxFf "$work/refused.txt" "$work/corpus.txt" >"$work/corpus-cli.txt" || true
  fi
  vins=$(wc -l <"$work/corpus-cli.txt" | tr -d ' ')
  host=$(quiet)
  run=$(GOMAXPROCS=1 "$bin" time -in "$work/corpus-cli.txt" -- "$nitrovin" decode --now "$NOW" -)
  [ "$(jq .stdout_lines <<<"$run")" -eq "$vins" ] || die "nitrovin decode - wrote $(jq .stdout_lines <<<"$run") lines for $vins VINs"
  record "$work" nitrovin-cli '{"workers":1,"input":"random"}' \
    "$(jq -c --argjson vins "$vins" --argjson refused "$(wc -l <"$work/refused.txt" | tr -d ' ')" \
      --arg version "$("$nitrovin" --version)" \
      '. + {vins: $vins, refused: $refused, vins_per_second: ($vins / .wall_seconds), version: $version}' <<<"$run")" "$host"
}

cmd_ultravin() {
  local work="${1:?work}" workers="${2:?workers}" order="${3:?random|sorted}" uv host result
  uv=$(ultravin_dir)
  host=$(quiet)
  result=$(RAYON_NUM_THREADS="$workers" ULTRAVIN_NOW_MICROS="$NOW_MICROS" \
    "$uv/target/release/examples/throughput" "$(input "$work" "$order")" 10 batch full auto | tail -1)
  record "$work" ultravin "{\"workers\":$workers,\"input\":\"$order\"}" \
    "$(jq -c --arg commit "$(git -C "$uv" rev-parse HEAD)" \
      '{rows, seconds: .elapsed_seconds, vins_per_second: .actual_rows_per_second, workers,
        user_cpu_seconds: .process_user_cpu_seconds, system_cpu_seconds: .process_system_cpu_seconds,
        commit: $commit}' <<<"$result")" "$host"
}

cmd_corgi() {
  local work="${1:?work}" engine="${2:?v2|v3}" host result
  case "$(node --version)" in v22.*) ;; *) die "corgi needs Node 22 (mise exec node@22 -- $0 corgi …)" ;; esac
  [ -d "$ROOT/scripts/bench/node_modules" ] || (cd "$ROOT/scripts/bench" && npm ci --no-audit --no-fund)
  host=$(quiet)
  result=$(node "$ROOT/scripts/bench/corgi.mjs" "$engine" "$work/sample.txt")
  record "$work" "corgi-$engine" '{"workers":1,"input":"sample"}' "$result" "$host"
}

# spvindecode's time per VIN, summed, is the rate; the wall time adds loading
# the list and exporting the key. The first trial's key is kept for agree.
cmd_postgres() {
  local work="${1:?work}" host started wall key result
  host=$(quiet)
  key="$work/postgres-stock.csv"
  [ -e "$key" ] && key="$work/postgres-stock.again.csv"
  started=$EPOCHREALTIME
  "$ROOT/scripts/oracle.sh" decode stock "$work/sample.txt" "$key" "$work/postgres-times.tsv" >&2
  wall=$(awk -v s="$started" -v e="$EPOCHREALTIME" 'BEGIN { print e - s }')
  result=$(awk -F'\t' -v wall="$wall" 'NR > 1 { n++; ms += $4 }
    END { printf "{\"vins\":%d,\"seconds\":%.3f,\"vins_per_second\":%.2f,\"wall_seconds\":%.3f}", n, ms / 1000, n / (ms / 1000), wall }' \
    "$work/postgres-times.tsv")
  record "$work" postgres '{"workers":1,"input":"sample"}' \
    "$(jq -c --arg v "$(docker exec "${ORACLE_CONTAINER:-nhtsa-oracle}" postgres --version)" '. + {version: $v}' <<<"$result")" "$host"
}

sqlcmd() {
  docker exec -i "$MSSQL_CONTAINER" /opt/mssql-tools18/bin/sqlcmd -C -S localhost -U sa -P "$MSSQL_PASSWORD" -b "$@"
}

cmd_mssql_up() {
  local dir="${1:?dir}" bak
  bak=$(cd "$dir" && ls vPICList_lite_*.bak | tail -1) || die "no vPICList_lite_<month>.bak in $dir"
  docker run -d --name "$MSSQL_CONTAINER" --platform linux/amd64 -e ACCEPT_EULA=Y -e MSSQL_PID=Developer \
    -e "MSSQL_SA_PASSWORD=$MSSQL_PASSWORD" -v "$(cd "$dir" && pwd):/bak:ro" "$MSSQL_IMAGE" >/dev/null
  until sqlcmd -Q 'select 1' >/dev/null 2>&1; do sleep 2; done
  sqlcmd -Q "restore database $MSSQL_DB from disk = '/bak/$bak'
    with move 'vPICList_Lite' to '/var/opt/mssql/data/$MSSQL_DB.mdf',
         move 'vPICList_Lite_log' to '/var/opt/mssql/data/${MSSQL_DB}_log.ldf', replace, recovery" >/dev/null
  sqlcmd -d "$MSSQL_DB" -h -1 -W -Q "set nocount on; select concat('restored $bak: ',
    (select count(*) from dbo.Pattern), ' patterns, ', (select count(*) from dbo.Wmi), ' WMIs')"
}

cmd_mssql_down() { docker rm -f "$MSSQL_CONTAINER" >/dev/null; }

# One batch, one session: each VIN's EXEC, its server time recorded beside
# it; the result sets stream to sqlcmd and are discarded.
cmd_mssql() {
  local work="${1:?work}" host started wall result
  grep -qvE '^[A-Z0-9]{17}$' "$work/sample.txt" && die "$work/sample.txt: not all 17-character VINs"
  host=$(quiet)
  started=$EPOCHREALTIME
  awk 'BEGIN { print "set nocount on;"
               print "drop table if exists dbo.bench_time;"
               print "create table dbo.bench_time (input_row int primary key, us bigint not null);"
               print "declare @t datetime2(7);" }
       { printf "set @t = sysdatetime(); exec dbo.spVinDecode @v = '\''%s'\''; insert dbo.bench_time values (%d, datediff_big(microsecond, @t, sysdatetime()));\n", $1, NR }' \
    "$work/sample.txt" | sqlcmd -d "$MSSQL_DB" >/dev/null
  wall=$(awk -v s="$started" -v e="$EPOCHREALTIME" 'BEGIN { print e - s }')
  result=$(sqlcmd -d "$MSSQL_DB" -h -1 -W -Q "set nocount on; select concat('{\"vins\":', count(*),
    ',\"us\":', sum(us), ',\"wall_seconds\":$wall}') from dbo.bench_time")
  record "$work" mssql '{"workers":1,"input":"sample"}' \
    "$(jq -c --arg v "$(sqlcmd -h -1 -W -Q 'set nocount on; select @@version' | awk 'NR == 1')" \
      '{vins, seconds: (.us / 1e6), vins_per_second: (.vins / (.us / 1e6)), wall_seconds, version: $v}' <<<"$result")" "$host"
}

cmd_agree() {
  local work="${1:?work}" uv bin
  uv=$(ultravin_dir)
  bin=$(harness "$work")
  [ -e "$work/postgres-stock.csv" ] || die "no $work/postgres-stock.csv: run postgres first"
  [ -e "$work/postgres-freshened.csv" ] ||
    "$ROOT/scripts/oracle.sh" decode freshened "$work/sample.txt" "$work/postgres-freshened.csv" >&2
  "$bin" values -now "$NOW" "$work/sample.txt" >"$work/nitrovin-values.jsonl"
  uv run --project "$uv" --frozen python "$ROOT/scripts/bench/ultravin_values.py" "$work/sample.txt" \
    >"$work/ultravin-values.jsonl"
  : >"$work/agree.jsonl"
  for engine in nitrovin ultravin; do
    for mode in stock freshened; do
      "$bin" agree "$work/postgres-$mode.csv" "$work/$engine-values.jsonl" |
        jq -c --arg engine "$engine" --arg mode "$mode" '{engine: $engine, oracle: $mode} + .' |
        tee -a "$work/agree.jsonl"
    done
  done
}

cmd_process() {
  local work="${1:?work}" nitrovin="${2:?nitrovin}" bin
  bin=$(harness "$work")
  "$nitrovin" decode "$ONE_VIN" >/dev/null # page the binary in
  "$bin" time -n 11 -- "$nitrovin" decode "$ONE_VIN" |
    jq -sc --arg version "$("$nitrovin" --version)" '{version: $version, runs: .}' | tee "$work/process.json"
}

# Every throughput row once per round; the second round runs in reverse.
cmd_all() {
  local work="${1:?work}" nitrovin="${2:?nitrovin}" rows i
  rows=()
  for order in random sorted; do
    for w in 1 4 12; do rows+=("go $w $order" "ultravin $w $order"); done
  done
  rows+=("cli" "corgi v3" "corgi v2" "postgres" "mssql")
  run_row() {
    set -- $1
    case "$1" in
      go | ultravin) "cmd_$1" "$work" "$2" "$3" ;;
      cli) cmd_cli "$work" "$nitrovin" ;;
      corgi) cmd_corgi "$work" "$2" ;;
      postgres | mssql) "cmd_$1" "$work" ;;
    esac
  }
  for row in "${rows[@]}"; do run_row "$row"; done
  for ((i = ${#rows[@]} - 1; i >= 0; i--)); do run_row "${rows[i]}"; done
}

# Per row: the trials in run order and their median; the corpus and agree
# results beside them.
cmd_report() {
  local work="${1:?work}" out="${2:?out.json}"
  jq -s --slurpfile corpus "$work/corpus.json" \
    --slurpfile agree <(cat "$work/agree.jsonl" 2>/dev/null || true) \
    --slurpfile process <(cat "$work/process.json" 2>/dev/null || true) '
    def median: sort | if length % 2 == 1 then .[length / 2 | floor] else (.[length / 2 - 1] + .[length / 2]) / 2 end;
    {corpus: $corpus[0], agree: $agree, process: $process[0],
     rows: (group_by([.row, .params.workers, .params.input]) | map({
       row: .[0].row, workers: .[0].params.workers, input: .[0].params.input,
       median_vins_per_second: (map(.result.vins_per_second) | median),
       trials: map({trial, at, vins_per_second: .result.vins_per_second, load: .host.load, result, host})}))}
  ' "$work/trials.jsonl" >"$out"
  jq -r '.rows[] | "\(.row)\t\(.workers)\t\(.input)\t\(.median_vins_per_second | floor)\t\([.trials[].vins_per_second | floor] | join(" / "))"' "$out"
}

cmd="${1:-}"; shift || true
case "$cmd" in
  corpus | go | cli | ultravin | corgi | postgres | mssql-up | mssql | mssql-down | agree | process | all | report) ;;
  *) usage ;;
esac
"cmd_${cmd//-/_}" "$@"
