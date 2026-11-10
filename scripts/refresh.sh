#!/usr/bin/env bash
# The monthly refresh's own commands (ADR 0006, 0007, 0008). The workflow,
# .github/workflows/refresh.yml, runs them between scripts/oracle.sh,
# nitrovin-data's scripts/build.sh and go test; a workstation runs the same,
# with go and jq at mise.toml's pins on PATH (mise's PATH, or the same
# versions by hand) beside curl.
#
#   scripts/refresh.sh detect [<month>]           whether a data month needs building; prints month=,
#                                                 pinned=, pinned_month=, reason=, build=true|false
#   scripts/refresh.sh epa <artifact> <out dir>   build the EPA vocabulary against <artifact> from EPA's
#                                                 vehicles.csv of today and check it; prints faults=,
#                                                 drift=; fails on a fault
#   scripts/refresh.sh report <dir>               the refresh report from a run's artifacts in <dir>;
#                                                 prints green=true|false
#   scripts/refresh.sh contract-key <frozen dir>  renew pkg/contract/testdata/oracle from a frozen key
#
# detect compares NHTSA's newest month with the one go.mod pins (the
# manifest.json of github.com/joshuasprow/nitrovin-data/pkg/embedded at the
# pinned version). A newer month is "new". The pinned month is "re-touched"
# when its dump's Content-Length or SHA-256 differs from the manifest's: NHTSA
# re-touched a month once (research: issue #13), so a month's identity is
# (month, size, SHA-256), and only the same size costs a download. An older
# month builds nothing. Naming a month builds it whatever its dump
# ("requested": a rebuild, nitrovin-data v<format>.<YYYYMM>.<n+1>).
#
# epa downloads EPA's vehicles.csv.zip, builds epa-vehicles.jsonl from it
# against <artifact> with cmd/nitrovin-epa, and runs `nitrovin vocab check`
# on it with that artifact as the data. Into <out dir> it writes the
# vocabulary, vehicles.json (the download's URL, size, SHA-256 and
# Last-Modified), coverage.txt (the builder's summary), check.jsonl (the
# findings) and result: "pass" when nothing faults, else "fail". Drift
# (an id or qualifier the month lacks, from the built-in curated overrides)
# is reported, never gated: loading serves it (ADR 0011).
#
# report reads <dir> laid out as refresh.yml downloads it:
#   refresh-data/manifest.json        the built artifact's (nitrovin-data scripts/build.sh)
#   refresh-vins/shards.json          [{"shard": 1, "vins": "run-1.tsv"}, …]
#   refresh-vins/coverage.json        nitrovin-import vins -report
#   oracle-<month>-<shard>/           oracle.yml's artifact for that file
#   parity-<month>-<shard>/           report.md, report.jsonl, cover.out and result
#                                     ("pass" or "fail") from TestParity -parity-run
#   refresh-epa/                      epa's <out dir>
#   previous/stale-cache.jsonl        optional: the pinned release's list
# and writes into it report.md (the refresh PR's body, or the red issue's
# comment), stale-cache.jsonl (every shard's list, each call once: the
# witnesses are in every file) and cover.out (pkg/decoder's profiles merged).
# Green means every shard's oracle ran and its parity passed, and the EPA
# vocabulary built with no fault. $REFRESH_RUN (the run's URL),
# $REFRESH_REASON and $REFRESH_PINNED, when set, head the report.
#
# contract-key cuts the first calls of a frozen key (testdata/parity/<month>,
# which TestParity -update-parity writes) into pkg/contract/testdata/oracle:
# the calls of testdata/oracle/sample-vins.tsv, with which the frozen corpus
# begins. That key's month gates TestEngineAnswerKey, TestDecodeAnswerKey,
# TestText and TestStdio, which skip on any other month's artifact.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
DATA_MODULE=github.com/joshuasprow/nitrovin-data
EPA_URL=https://www.fueleconomy.gov/feg/epadata/vehicles.csv.zip

die() { echo "refresh: $*" >&2; exit 1; }

usage() {
  sed -n '/^#   scripts/,/^#$/s/^#  //p' "$0" >&2
  exit 2
}

sha256() {
  if command -v sha256sum >/dev/null; then sha256sum "$1" | cut -d' ' -f1
  else shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

cmd_detect() {
  local month="${1:-}" pinned manifest pinned_month reason="" url length tmp
  pinned=$(cd "$ROOT" && go list -m -f '{{.Version}}' "$DATA_MODULE")
  manifest="$(cd "$ROOT" && go mod download -json "$DATA_MODULE" | jq -r .Dir)/pkg/embedded/manifest.json"
  pinned_month=$(jq -r .data_month "$manifest")
  if [ -n "$month" ]; then
    [[ "$month" =~ ^[0-9]{4}_[0-9]{2}$ ]] || die "month must be YYYY_MM, got '$month'"
    reason=requested
  else
    month=$("$ROOT/scripts/oracle.sh" month)
    if [[ "$month" > "$pinned_month" ]]; then
      reason=new
    elif [ "$month" = "$pinned_month" ]; then
      url=$(jq -r .dump.url "$manifest")
      length=$(curl -fsSI --retry 3 "$url" | awk 'tolower($1) == "content-length:" { print $2 + 0 }')
      [ -n "$length" ] || die "HEAD $url gave no Content-Length"
      if [ "$length" != "$(jq -r .dump.bytes "$manifest")" ]; then
        reason=re-touched
      else
        tmp=$(mktemp)
        curl -fsSL --retry 3 -o "$tmp" "$url"
        [ "$(sha256 "$tmp")" = "$(jq -r .dump.sha256 "$manifest")" ] || reason=re-touched
        rm -f "$tmp"
      fi
    fi
  fi
  echo "month=$month"
  echo "pinned=$pinned"
  echo "pinned_month=$pinned_month"
  echo "reason=$reason"
  if [ -n "$reason" ]; then echo build=true; else echo build=false; fi
  case "$reason" in
    new) echo "refresh: NHTSA publishes $month, newer than the pinned $pinned_month ($pinned)" >&2 ;;
    re-touched) echo "refresh: NHTSA re-touched $month's dump since $pinned was built" >&2 ;;
    requested) echo "refresh: $month requested (pinned: $pinned_month, $pinned)" >&2 ;;
    *) echo "refresh: nothing to do: NHTSA's newest month is $month and $pinned ($pinned_month) holds its dump" >&2 ;;
  esac
}

# A coverage profile's blocks merged over runs: a block counts if any run
# covered it (mode set or count, as go test -coverprofile writes them).
merge_cover() {
  awk 'FNR == 1 { if (!mode) { mode = $0 }; next }
    !($1 in n) { order[++k] = $1; stmts[$1] = $2; n[$1] = $3 + 0; next }
    $3 + 0 > n[$1] { n[$1] = $3 + 0 }
    END { if (!mode) exit; print mode; for (i = 1; i <= k; i++) print order[i], stmts[order[i]], n[order[i]] }' "$@"
}

cmd_epa() {
  local artifact="${1:?artifact}" out="${2:?out dir}" tmp status=0 faults drift
  [ -r "$artifact" ] || die "no $artifact"
  mkdir -p "$out"
  artifact="$(cd "$(dirname "$artifact")" && pwd)/$(basename "$artifact")"
  out=$(cd "$out" && pwd)
  echo fail >"$out/result"
  tmp=$(mktemp -d)
  # shellcheck disable=SC2064 # expanded now: $tmp is local to this function
  trap "rm -rf '$tmp'" EXIT
  # Built, not run with go run, which turns every exit status into 1.
  (cd "$ROOT" && go build -o "$tmp/" ./cmd/nitrovin-epa ./cmd/nitrovin)

  curl -fsSL --retry 3 -D "$tmp/headers" -o "$tmp/vehicles.csv.zip" "$EPA_URL"
  jq -n --arg url "$EPA_URL" --argjson bytes "$(wc -c <"$tmp/vehicles.csv.zip" | tr -d ' ')" \
    --arg sha256 "$(sha256 "$tmp/vehicles.csv.zip")" \
    --arg last_modified "$(tr -d '\r' <"$tmp/headers" | awk 'tolower($1) == "last-modified:" { sub(/^[^:]*: */, ""); v = $0 } END { print v }')" \
    '{$url, $bytes, $sha256, $last_modified}' >"$out/vehicles.json"

  "$tmp/nitrovin-epa" -data "$artifact" -o "$out/epa-vehicles.jsonl" "$tmp/vehicles.csv.zip" 2>"$out/coverage.txt" ||
    { cat "$out/coverage.txt" >&2; die "cmd/nitrovin-epa failed"; }
  cat "$out/coverage.txt" >&2

  # Exit 1 is findings on stdout, or a problem on stderr and none.
  "$tmp/nitrovin" --data "$artifact" vocab check "$out/epa-vehicles.jsonl" >"$out/check.jsonl" || status=$?
  case "$status" in
    0) ;;
    1) [ -s "$out/check.jsonl" ] || die "nitrovin vocab check failed" ;;
    *) die "nitrovin vocab check exited $status" ;;
  esac
  faults=$(jq -s 'map(select(.severity == "fault")) | length' "$out/check.jsonl")
  drift=$(jq -s 'map(select(.severity == "drift")) | length' "$out/check.jsonl")
  echo "faults=$faults"
  echo "drift=$drift"
  [ "$faults" -eq 0 ] || die "epa-vehicles.jsonl: $faults faults (check.jsonl)"
  echo pass >"$out/result"
  echo "refresh: epa-vehicles.jsonl $(head -1 "$out/epa-vehicles.jsonl" | jq -r .version): no fault, $drift drift" >&2
}

# The report's EPA section from epa's <out dir>: the build, the gate (no
# fault) and the drift, which is reported, never gated.
epa_report() {
  local epa="$1" faults drift n
  echo '### The EPA vocabulary (`epa-vehicles.jsonl`)'
  echo
  if [ -r "$epa/epa-vehicles.jsonl" ] && [ -r "$epa/vehicles.json" ]; then
    jq -r --arg version "$(head -1 "$epa/epa-vehicles.jsonl" | jq -r .version)" \
      --argjson entries "$(($(wc -l <"$epa/epa-vehicles.jsonl") - 1))" \
      '"`cmd/nitrovin-epa` built version `\($version)`, \($entries) entries, from EPA'\''s [`vehicles.csv.zip`](\(.url)) (\(.bytes) bytes, sha256 `\(.sha256)`, Last-Modified \(.last_modified)) and this month'\''s artifact. It is published with the data release, gated on `nitrovin vocab check` finding no fault; drift is reported, never gated (ADR 0011)."' \
      "$epa/vehicles.json"
    echo
  fi
  if [ "$(cat "$epa/result" 2>/dev/null || true)" != pass ] && [ ! -s "$epa/check.jsonl" ]; then
    echo '**Not built or not checked**: the `epa` job failed before `nitrovin vocab check` reported (see its log).'
    echo
    return
  fi
  faults=$(jq -s 'map(select(.severity == "fault")) | length' "$epa/check.jsonl")
  drift=$(jq -s 'map(select(.severity == "drift")) | length' "$epa/check.jsonl")
  echo '| `nitrovin vocab check` | |'
  echo '| --- | --- |'
  if [ "$faults" -eq 0 ]; then echo '| Faults (gated) | pass: 0 |'; else echo "| Faults (gated) | **fail: $faults** |"; fi
  if [ "$faults" -eq 0 ]; then echo "| Drift (reported) | $drift |"; else echo '| Drift (reported) | not measured: a fault stops the load |'; fi
  echo
  n=$(jq -s length "$epa/check.jsonl")
  if [ "$n" -gt 0 ]; then
    echo '| Line | Severity | Detail |'
    echo '| --- | --- | --- |'
    jq -sr '.[:100][] | "| \(.line) | \(.severity) | \(.detail | gsub("\\|"; "\\|")) |"' "$epa/check.jsonl"
    [ "$n" -le 100 ] || echo "| | | $((n - 100)) more in the run's \`refresh-epa\` artifact (\`check.jsonl\`) |"
    echo
  fi
  if [ -s "$epa/coverage.txt" ]; then
    echo '<details><summary>Coverage, as the builder reports it</summary>'
    echo
    echo '```text'
    cat "$epa/coverage.txt"
    echo '```'
    echo
    echo '</details>'
    echo
  fi
}

cmd_report() {
  local dir="${1:?dir}" data vins month shards green=true n file o p result md \
        calls rows stock_s fresh_s stale disagree parity cover total prev_n epa
  data="$dir/refresh-data/manifest.json"
  vins="$dir/refresh-vins"
  [ -r "$data" ] || die "no $data"
  [ -r "$vins/shards.json" ] || die "no $vins/shards.json"
  month=$(jq -r .data_month "$data")
  shards=$(jq -r '.[] | "\(.shard)\t\(.vins)"' "$vins/shards.json")
  md="$dir/report.md"

  # Each file's oracle and parity outcome; any miss is red.
  local rows_md=""
  while IFS=$'\t' read -r n file; do
    o="$dir/oracle-$month-$n"
    p="$dir/parity-$month-$n"
    result=$(cat "$p/result" 2>/dev/null || true)
    [ "$result" = pass ] || green=false
    if [ -r "$o/manifest.json" ]; then
      calls=$(jq -r .vin_count "$o/manifest.json")
      rows=$(jq -r .keys.freshened.answer_rows "$o/manifest.json")
      stock_s=$(jq -r '.timings["decode stock"] // "?"' "$o/manifest.json")
      fresh_s=$(jq -r '.timings["decode freshened"] // "?"' "$o/manifest.json")
      stale=$(jq -r .stale_cache.vins "$o/manifest.json")
    else
      calls="" rows="" stock_s="" fresh_s="" stale=""
      green=false
    fi
    disagree=$(sed -n 's/^| Calls that disagree | \([0-9]*\) |$/\1/p' "$p/report.md" 2>/dev/null || true)
    case "$result:${disagree:-}" in
      pass:*) parity="pass: 0 calls disagree" ;;
      fail:[0-9]*) parity="**fail: $disagree calls disagree**" ;;
      fail:*) parity="**fail** (see the job's log)" ;;
      *) if [ -r "$o/manifest.json" ]; then parity="**not gated**"; else parity="**oracle failed**"; fi ;;
    esac
    rows_md+="| \`$file\` | $calls | $rows | ${stock_s:+$stock_s / $fresh_s} | $stale | $parity |"$'\n'
  done <<<"$shards"

  # The stale-cache class: each call once over every file, in file order.
  cat "$dir"/oracle-"$month"-*/stale-cache.jsonl 2>/dev/null | awk '!seen[$0]++' >"$dir/stale-cache.jsonl" || true
  stale=$(wc -l <"$dir/stale-cache.jsonl" | tr -d ' ')

  # pkg/decoder's statement coverage over every file, recorded, not gated.
  cover=""
  if compgen -G "$dir/parity-$month-*/cover.out" >/dev/null; then
    merge_cover "$dir"/parity-"$month"-*/cover.out >"$dir/cover.out"
    total=$(cd "$ROOT" && go tool cover -func="$dir/cover.out" | awk '$1 == "total:" { print $3 }')
    cover="**pkg/decoder coverage** over every file: $total of statements (recorded, not gated)."
  fi

  # The EPA vocabulary's gate: no fault.
  epa="$dir/refresh-epa"
  [ "$(cat "$epa/result" 2>/dev/null || true)" = pass ] || green=false

  {
    echo "## Refresh: data month $month"
    echo
    if [ "$green" = true ]; then
      echo "**Green.** nitrovin equals the freshened oracle on every row of every call."
    else
      echo "**Red.** No data release; the pinned month stays. The default reading of a disagreement is a decoder bug (ADR 0006)."
    fi
    echo
    echo '| | |'
    echo '| --- | --- |'
    [ -z "${REFRESH_REASON:-}" ] || echo "| Why | $(case "$REFRESH_REASON" in
      new) echo "a new month" ;; re-touched) echo "NHTSA re-touched the pinned month's dump" ;;
      requested) echo "a rebuild was requested" ;; *) echo "$REFRESH_REASON" ;; esac) |"
    [ -z "${REFRESH_PINNED:-}" ] || echo "| Pinned before | nitrovin-data \`$REFRESH_PINNED\` |"
    jq -r '"| Dump | [`\(.dump.url | split("/") | last)`](\(.dump.url)), \(.dump.bytes) bytes, sha256 `\(.dump.sha256)` |",
      "| Artifact | `nitrovin-\(.data_month).artifact`, format \(.format), \(.bytes) bytes, blake3 `\(.blake3)` |",
      "| Importer | `\(.importer.package)` at [`\(.importer.commit[:12])`](https://github.com/joshuasprow/nitrovin/tree/\(.importer.commit)) |"' "$data"
    [ -z "${REFRESH_RUN:-}" ] || echo "| Run | $REFRESH_RUN (artifacts: \`report.jsonl\` per file, \`coverage.json\`, \`stale-cache.jsonl\`) |"
    echo
    echo '### Gates'
    echo
    echo 'Every row of every call, all 15 columns, against the freshened oracle (ADR 0008); one `oracle.yml` job per file.'
    echo
    echo '| File | Calls | Freshened rows | Decode s, stock / freshened | stale-cache calls | Parity |'
    echo '| --- | --- | --- | --- | --- | --- |'
    printf '%s' "$rows_md"
    echo
    [ -z "$cover" ] || { echo "$cover"; echo; }
    epa_report "$epa"
    if [ -r "$vins/coverage.json" ]; then
      echo '### What the list reaches (`coverage.json`)'
      echo
      jq -r '"\(.vins.total) VINs in \(.files | length) files of at most \(.max_per_file): \(.vins.corpus) committed, \(.vins.mutations) mutations, \(.vins.generated) generated.",
        "",
        "| | In the month | Corpus | + mutations | + generated | Corpus misses | Unreached |",
        "| --- | --- | --- | --- | --- | --- | --- |",
        (["WMIs", .wmis], ["VinSchemas", .schemas] | "| \(.[0]) | \(.[1].month) | \(.[1].corpus) | \(.[1].mutations) | \(.[1].generated) | \(.[1].corpus_misses | length) | \(.[1].unreached | length) |"),
        "",
        (if (.schemas.unreached | length) > 0 then "Unreached VinSchemas: \(.schemas.unreached | join(", "))." else empty end),
        (if (.wmis.unreached | length) > 0 then "Unreached WMIs: \(.wmis.unreached | join(", "))." else empty end)' "$vins/coverage.json"
      echo
    fi
    echo '### stale-cache (ADR 0006: reported, never gated)'
    echo
    printf '%s calls differ between the stock and freshened keys (`stale-cache.jsonl`, an asset of the data release).' "$stale"
    if [ -r "$dir/previous/stale-cache.jsonl" ]; then
      prev_n=$(wc -l <"$dir/previous/stale-cache.jsonl" | tr -d ' ')
      printf ' The pinned release listed %s (%+d).' "$prev_n" "$((stale - prev_n))"
      if [ "$stale" -eq 0 ] && [ "$prev_n" -gt 0 ]; then printf ' **The class healed.**'; fi
      if [ "$stale" -gt 0 ] && [ "$prev_n" -eq 0 ]; then printf ' **The class relapsed.**'; fi
    else
      printf ' The pinned release has no list to compare with.'
      if [ "$stale" -eq 0 ]; then printf ' **No call is in the class.**'; fi
    fi
    echo
    echo
    o="$dir/oracle-$month-$(jq -r '.[0].shard' "$vins/shards.json")"
    if [ -r "$o/witnesses.jsonl" ]; then
      echo '### Witnesses against live vPIC (reported, never gated)'
      echo
      echo '| Witness | Class | Live vPIC | Fields | Stock ≠ freshened | Disagreeing elements |'
      echo '| --- | --- | --- | --- | --- | --- |'
      jq -r '"| `\(.vin)` | \(.class) | \(if .error then "unchecked" elif .agrees then "agrees" else "**disagrees**" end) | \(.fields // "") | \(.disputed // "") | \(.disagree // [] | map("\(.element) \(.code)") | join(", ")) |"' "$o/witnesses.jsonl"
      echo
    fi
    echo '### Parity, per file'
    while IFS=$'\t' read -r n file; do
      p="$dir/parity-$month-$n/report.md"
      echo
      echo "<details><summary><code>$file</code></summary>"
      echo
      if [ -r "$p" ]; then sed 's/^### /#### /' "$p"; else echo 'No report: the gate did not run on this file.'; fi
      echo
      echo '</details>'
    done <<<"$shards"
  } >"$md"
  echo "green=$green"
}

cmd_contract_key() {
  local frozen="${1:?frozen dir}" out="$ROOT/pkg/contract/testdata/oracle" sample="$ROOT/testdata/oracle/sample-vins.tsv" n rows
  [ -r "$frozen/answer-key.csv" ] && [ -r "$frozen/corpus.tsv" ] && [ -r "$frozen/manifest.json" ] ||
    die "$frozen is not a frozen key (corpus.tsv, answer-key.csv, manifest.json)"
  # Calls as scripts/oracle.sh decode reads a list: the VIN, then the hint or nothing.
  calls() { awk -F'\t' '{ sub(/\r$/, "") } /^[[:space:]]*(#|$)/ { next } { print $1 "\t" (NF > 1 ? $2 : "") }' "$1"; }
  n=$(calls "$sample" | wc -l | tr -d ' ')
  calls "$frozen/corpus.tsv" | awk -v n="$n" 'NR <= n' | cmp -s - <(calls "$sample") ||
    die "$frozen/corpus.tsv does not begin with the $n calls of testdata/oracle/sample-vins.tsv"
  # The key's records up to input row n. A CSV record ends at a line end
  # outside quotes (an even number of quotes so far), and a doubled quote
  # inside a value keeps that count even; input_row opens every record.
  awk -v n="$n" '
    NR == 1 { print; next }
    !open { row = $0; sub(/,.*/, "", row); if (row + 0 > n) exit; records++ }
    { print; if (gsub(/"/, "&") % 2) open = !open }
    END { print records > "/dev/stderr" }' "$frozen/answer-key.csv" >"$out/answer-key.csv" 2>"$out/.rows"
  rows=$(cat "$out/.rows"); rm -f "$out/.rows"
  jq --arg vins_sha256 "$(sha256 "$sample")" --argjson vin_count "$n" --argjson answer_rows "$rows" \
     --arg answer_key_sha256 "$(sha256 "$out/answer-key.csv")" --arg from "testdata/parity/$(basename "$frozen")/answer-key.csv" '{
      month, dump_bytes, dump_sha256, oracle, now,
      vins: "testdata/oracle/sample-vins.tsv", $vins_sha256, $vin_count, $answer_rows, $answer_key_sha256,
      server, collate, ctype, cut_from: $from}' "$frozen/manifest.json" >"$out/manifest.json"
  echo "refresh: pkg/contract/testdata/oracle: the first $n calls of $frozen's key, $rows rows ($(jq -r .month "$out/manifest.json"))" >&2
}

cmd="${1:-}"; shift || true
case "$cmd" in
  detect | epa | report | contract-key) "cmd_${cmd//-/_}" "$@" ;;
  *) usage ;;
esac
