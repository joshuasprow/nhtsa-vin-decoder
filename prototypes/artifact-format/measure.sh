#!/usr/bin/env bash
# PROTOTYPE: every number in results/ comes from this script. Throwaway.
# Needs: the oracle container restored (scripts/oracle.sh up && restore), zstd, flatc, sqlite3.
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p work bin results
R=results/raw.txt
: >"$R"
log() { echo "$*" | tee -a "$R"; }

go build -o bin/bakeoff ./cmd/bakeoff
B=bin/bakeoff

log "## host"
log "$(sysctl -n machdep.cpu.brand_string) $(go version | cut -d' ' -f3-) $(zstd --version | head -1)"

log "## extract (shared by all candidates)"
log "$($B extract -tsv work/tsv -wyvc)"
$B genvins -n 5000 -out work/gen.txt
{ grep -v '^#' ../../testdata/oracle/sample-vins.tsv | cut -f1 | grep . || true; cat work/gen.txt; } >work/vins.txt
log "vins=$(wc -l <work/vins.txt | tr -d ' ')"

ext() { case $1 in custom) echo bin ;; sqlite) echo db ;; fb) echo fb ;; esac; }

log "## build twice (determinism), zstd -19"
for f in custom sqlite fb; do
  a=work/$f.$(ext "$f")
  log "$($B build -format "$f" -out "$a")"
  log "$($B build -format "$f" -out "$a.again")"
  cmp -s "$a" "$a.again" && log "$f deterministic=yes" || log "$f deterministic=NO"
  rm -f "$a.again"
  t=$EPOCHREALTIME; zstd -q -f -19 -T1 "$a" -o "$a.zst"; t2=$EPOCHREALTIME
  log "$f zstd19_bytes=$(wc -c <"$a.zst" | tr -d ' ') zstd19_s=$(echo "$t2 - $t" | bc)"
done

log "## size with wmiyearvalidchars (the bug-for-bug charset table, 8.8M rows)"
for f in custom sqlite fb; do
  a=work/$f-wyvc.$(ext "$f")
  log "$($B build -wyvc -format "$f" -out "$a")"
  zstd -q -f -19 -T0 "$a" -o "$a.zst"
  log "$f-wyvc zstd19_bytes=$(wc -c <"$a.zst" | tr -d ' ')"
done

log "## warm decode, one core (GOMAXPROCS=1), 3 s per row"
for f in custom sqlite fb; do
  a=work/$f.$(ext "$f")
  log "$($B bench -format "$f" -artifact "$a" -dur 3s)"
  log "$($B bench -format "$f" -artifact "$a.zst" -zstd -dur 1s)"
done
log "$($B bench -format sqlite -artifact work/sqlite.db -dur 3s -pushdown -loadall 2>&1 | tr '\n' ' ')"

VIN=1HGCM82633A004352
log "## cold process: --data <file>, one VIN, 20 runs (page cache warm)"
for f in custom sqlite fb; do
  a=work/$f.$(ext "$f")
  log "$f raw  $($B cold -n 20 "$B" decode -format "$f" -artifact "$a" "$VIN")"
  log "$f zstd $($B cold -n 20 "$B" decode -format "$f" -artifact "$a.zst" -zstd "$VIN")"
done

log "## cold process: go:embed binary, one VIN, 20 runs"
for f in custom sqlite fb; do
  a=work/$f.$(ext "$f")
  for k in raw zstd; do
    if [ $k = raw ]; then cp "$a" cmd/embedded/artifact; else cp "$a.zst" cmd/embedded/artifact; fi
    echo "$f $k" >cmd/embedded/kind.txt
    t=$EPOCHREALTIME; go build -o "bin/embedded-$f-$k" ./cmd/embedded; t2=$EPOCHREALTIME
    log "$f $k binary_bytes=$(wc -c <"bin/embedded-$f-$k" | tr -d ' ') go_build_s=$(echo "$t2 - $t" | bc) $(bin/embedded-$f-$k $VIN | tr '\n' ' ')"
    log "$f $k $($B cold -n 20 "bin/embedded-$f-$k" "$VIN")"
  done
done
rm -f cmd/embedded/artifact cmd/embedded/kind.txt

log "## inspectability"
log "custom: $($B dump -artifact work/custom.bin wmi wmi=1HG | head -3 | tr '\n' ' ')"
log "sqlite: $(sqlite3 work/sqlite.db "select id, keys, elementid, attributeid from pattern where vinschemaid = (select vinschemaid from wmi_vinschema where wmiid = (select id from wmi where wmi='1HG') limit 1) limit 2" | tr '\n' ' ')"
t=$EPOCHREALTIME
flatc --json --strict-json --raw-binary -o work artifact.fbs -- work/fb.fb >/dev/null 2>&1 && ok=yes || ok=no
t2=$EPOCHREALTIME
log "fb: flatc --json ok=$ok s=$(echo "$t2 - $t" | bc) json_bytes=$(wc -c <work/fb.json 2>/dev/null | tr -d ' ' || echo 0)"
