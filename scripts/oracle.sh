#!/usr/bin/env bash
# The Postgres oracle: NHTSA's dump restored unmodified into Postgres 17+, and
# its own vpic.spvindecode run over a VIN list to export an answer key, on the
# stock dump and on a freshened copy (ADR 0006).
# CI (.github/workflows/oracle.yml) and a workstation run the same commands.
#
#   scripts/oracle.sh month                       newest published data month (YYYY_MM)
#   scripts/oracle.sh fetch <month> <dir>         download and unzip that month's custom dump;
#                                                 prints month=, dump_bytes=, dump_sha256=, backup=
#   scripts/oracle.sh up                          workstation only: start a postgres:17 container, collate C
#   scripts/oracle.sh restore <backup>            pg_restore the dump into the container's `vpic` db
#   scripts/oracle.sh freshen                     copy `vpic` to `vpic_fresh` and empty its wmiyearvalidchars
#   scripts/oracle.sh corpus <vins.tsv>           print the VIN list, then every deviation witness it lacks
#   scripts/oracle.sh decode <mode> <vins.tsv> <key.csv> [<times.tsv>]
#                                                 run spvindecode over every VIN on the stock or
#                                                 freshened oracle, write the answer key and per-VIN times
#   scripts/oracle.sh stale-cache <stock.csv> <freshened.csv> <out.jsonl>
#                                                 the stale-cache detector: VINs whose two keys differ
#   scripts/oracle.sh recheck <stock.csv> <freshened.csv> <out.jsonl>
#                                                 compare every witness's answers with live vPIC; never fails on a mismatch
#   scripts/oracle.sh down                        workstation only: remove the container
#
# Every command runs inside $ORACLE_CONTAINER (default nhtsa-oracle) with
# `docker exec`, so the client tools always match the server: the dump's
# archive format 1.16 needs pg_restore 17+, and runner images ship 16.
# If $ORACLE_TIMINGS names a file, each command appends "<command>\t<seconds>"
# ("decode <mode>" for decode).
#
# Collation: the database is initialized with --lc-collate=C and nothing else,
# ultravin's oracle contract (docs/ACCEPTANCE.md, "Oracle collation"): C
# reproduces SQL Server's tie-breaks and a locale-aware collation does not
# (under the image default en_US.utf8, fVinModelYear2's BETWEEN 'A' AND 'H'
# accepts 'É'). LC_CTYPE stays en_US.utf8: a C ctype makes initdb pick
# SQL_ASCII and changes upper(). restore and decode refuse any other collation.
#
# Modes (ADR 0006, docs/deviations/): `stock` decodes the dump as restored, in
# the `vpic` database. `freshened` decodes `vpic_fresh`, made by `freshen`: a
# copy with vpic.wmiyearvalidchars truncated. That table is a stale
# materialised copy of fExtractValidCharsPerWmiYear; spvindecode_errorcode is
# its only reader and, finding it empty, computes every charset from the
# pattern keys. The refresh gates nitrovin against the freshened key. decode
# refuses a database whose table does not match the mode.
#
# vins.tsv: one VIN per line, optionally a tab and a model-year hint; blank
# lines and lines starting with # are skipped. The answer key is CSV with
# every column spvindecode returns, one row per row the procedure emits
# (an element can repeat), ordered by input line, element id, then the whole
# row. The procedure's own row order is not used: it shifts between restores
# of the same dump, and the key must not. times.tsv has a header and one line
# per VIN: input row, VIN, model-year hint, milliseconds in spvindecode.
#
# Each VIN is decoded in its own transaction: the freshened fallback creates a
# temporary table per pattern key, and one statement over many VINs exhausts
# max_locks_per_transaction. Each call drops its own temp tables, so the rows
# are those of one statement over every VIN.
#
# stale-cache writes one JSON line per input line whose stock and freshened
# rows differ, in input order: {"vin", "year" (the hint or null), "elements"
# (the ids of every element with any differing row)}. Those VINs are the
# stale-cache class (docs/deviations/stale-cache.md). The list belongs in the
# refresh report and its release, never in git.
#
# recheck asks live vPIC's DecodeVinValues about every witness and compares
# it with the freshened answer on each element either key decodes for that
# VIN without a model-year hint; elements live vPIC lacks are skipped, and
# multi-valued ones compare as sets (vPIC's join order is unstable). One JSON
# line per witness: {"class", "vin", "fields" compared, "disputed" (fields
# where stock and freshened differ), "agrees", "disagree": [{"element",
# "code", "stock", "freshened", "live"}]}, or {"class", "vin", "error"} when
# vPIC did not answer. Live vPIC runs on newer data than any dump, so a
# disagreement is reported for a human, never gated (docs/deviations/README.md).
set -euo pipefail

CONTAINER="${ORACLE_CONTAINER:-nhtsa-oracle}"
IMAGE="${ORACLE_IMAGE:-postgres:17}"
DB=vpic # the stock oracle; `DB=<name> psql_ …` targets another database
FRESH_DB=vpic_fresh
BASE_URL=https://vpic.nhtsa.dot.gov/downloads
VPIC_API=https://vpic.nhtsa.dot.gov/api/vehicles
DEVIATIONS="$(dirname "$0")/../docs/deviations"
# The answer key's columns: the input line's, then spvindecode's own.
KEY_COLUMNS='input_row, vin, year_hint, groupname, variable, value, itempatternid, itemvinschemaid, itemkeys, itemelementid, itemattributeid, itemcreatedon, itemwmiid, code, datatype, decode, itemsource, itemtobeqced'

die() { echo "oracle: $*" >&2; exit 1; }

usage() {
  sed -n '/^#   scripts/,/^#$/s/^#  //p' "$0" >&2
  exit 2
}

PSQL=(psql -X -v ON_ERROR_STOP=1 -U postgres)
INITDB_ARGS=--lc-collate=C
# No -i: docker exec would otherwise swallow the caller's stdin.
psql_at() { docker exec "$CONTAINER" "${PSQL[@]}" -d "$1" "${@:2}"; }
psql_() { psql_at "$DB" "$@"; }
psql_stdin() { docker exec -i "$CONTAINER" "${PSQL[@]}" -d "$DB" "$@"; }

sha256() {
  if command -v sha256sum >/dev/null; then sha256sum "$1" | cut -d' ' -f1
  else shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

dump_url() { echo "$BASE_URL/vPICList_lite_$1.custom.zip"; }

# NHTSA lists one month at a time and publishes no index: probe the current
# UTC month, then walk back (research: issue #13, "NHTSA's dump", §3).
cmd_month() {
  local y m month status
  y=$(date -u +%Y); m=$((10#$(date -u +%m)))
  for _ in $(seq 0 12); do
    month=$(printf '%04d_%02d' "$y" "$m")
    # Only a 404 means "not published"; anything else must not fall back to an older month.
    status=$(curl -sI --retry 3 -o /dev/null -w '%{http_code}' "$(dump_url "$month")") || true
    case "$status" in
      200) echo "$month"; return ;;
      404) ;;
      *) die "HEAD $(dump_url "$month") answered '$status'" ;;
    esac
    m=$((m - 1)); if [ "$m" -eq 0 ]; then m=12; y=$((y - 1)); fi
  done
  die "no dump found in the last 13 months under $BASE_URL"
}

cmd_fetch() {
  local month="${1:?month}" dir="${2:?dir}" zip member
  [[ "$month" =~ ^[0-9]{4}_[0-9]{2}$ ]] || die "month must be YYYY_MM, got '$month'"
  mkdir -p "$dir"
  zip="$dir/vPICList_lite_$month.custom.zip"
  curl -fsSL --retry 3 -o "$zip" "$(dump_url "$month")"
  member=$(unzip -Z1 "$zip")
  [ "$(printf '%s\n' "$member" | wc -l)" -eq 1 ] || die "expected one member in $zip, got: $member"
  unzip -o -q "$zip" -d "$dir"
  echo "month=$month"
  echo "dump_bytes=$(wc -c <"$zip" | tr -d ' ')"
  echo "dump_sha256=$(sha256 "$zip")"
  echo "backup=$dir/$member"
}

cmd_up() {
  docker run -d --name "$CONTAINER" --shm-size 1g \
    -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB="$DB" -e POSTGRES_INITDB_ARGS="$INITDB_ARGS" \
    --health-cmd 'pg_isready -U postgres' --health-interval 1s "$IMAGE" >/dev/null
  until [ "$(docker inspect -f '{{.State.Health.Status}}' "$CONTAINER")" = healthy ]; do sleep 1; done
  echo "$CONTAINER ready ($IMAGE, $(psql_ -tA -c "select $COLLATION_SQL"))"
}

# "collate <datcollate>, ctype <datctype>" of the connected database, as SQL.
COLLATION_SQL="(select 'collate ' || datcollate || ', ctype ' || datctype from pg_database where datname = current_database())"

require_collate_c() {
  local c
  c=$(psql_ -tA -c "select datcollate from pg_database where datname = current_database()")
  [ "$c" = C ] || die "database $DB has collation $c, not C: initialize it with POSTGRES_INITDB_ARGS=$INITDB_ARGS (scripts/oracle.sh up does)"
}

# Into an empty database only: a second restore over a loaded one fails
# with hundreds of "already exists" errors, which --exit-on-error surfaces.
cmd_restore() {
  local backup="${1:?backup}"
  require_collate_c
  docker cp "$backup" "$CONTAINER:/tmp/dump.backup"
  docker exec "$CONTAINER" sh -c \
    'pg_restore -U postgres -d '"$DB"' --no-owner --no-privileges --exit-on-error -j "$(nproc)" /tmp/dump.backup && rm /tmp/dump.backup'
  psql_ -tA -c "select 'restored ' || pg_size_pretty(pg_database_size('$DB')) || ', ' || (select count(*) from vpic.pattern) || ' patterns, ' || (select count(*) from vpic.wmi) || ' WMIs, ' || $COLLATION_SQL"
}

# A copy rather than a TRUNCATE in place, so both modes stay decodable.
# FILE_COPY clones the files instead of WAL-logging 1.3 GB page by page.
cmd_freshen() {
  local rows
  require_collate_c
  rows=$(psql_ -tA -c 'select count(*) from vpic.wmiyearvalidchars')
  [ "$rows" -gt 0 ] || die "vpic.wmiyearvalidchars is empty in $DB: restore the dump first"
  # CREATE DATABASE … TEMPLATE needs no session on the template, this one's included.
  psql_at postgres -q -c 'set client_min_messages = warning' \
                      -c "drop database if exists $FRESH_DB" \
                      -c "create database $FRESH_DB template $DB strategy file_copy"
  psql_at "$FRESH_DB" -q -c 'truncate vpic.wmiyearvalidchars'
  psql_at "$FRESH_DB" -tA -c "select 'freshened $FRESH_DB: emptied $rows wmiyearvalidchars rows, '
      || pg_size_pretty(pg_database_size(current_database())) || ', ' || $COLLATION_SQL"
}

# Point DB at a mode's database, refusing one whose cache does not match it.
use_mode() {
  local mode="$1" cache
  case "$mode" in
    stock) DB=vpic ;;
    freshened) DB=$FRESH_DB ;;
    *) die "mode must be stock or freshened, got '$mode'" ;;
  esac
  [ "$(psql_at postgres -tA -c "select count(*) from pg_database where datname = '$DB'")" -eq 1 ] ||
    die "no database $DB for mode $mode: run restore, then freshen"
  cache=$(psql_ -tA -c "select case when exists (select from vpic.wmiyearvalidchars) then 'populated' else 'empty' end")
  case "$mode:$cache" in
    stock:populated | freshened:empty) ;;
    *) die "$DB is not a $mode oracle: its vpic.wmiyearvalidchars is $cache" ;;
  esac
}

cmd_decode() {
  local mode="${1:?mode}" vins="${2:?vins.tsv}" out="${3:?key.csv}" times="${4:-}" missing
  [ -r "$vins" ] || die "cannot read $vins"
  use_mode "$mode"
  require_collate_c
  psql_ -q -c 'set client_min_messages = warning' \
           -c 'drop schema if exists oracle cascade' \
           -c 'create schema oracle' \
           -c 'create table oracle.input (input_row bigserial primary key, vin text not null, year_hint integer)'
  # Text-format COPY: a missing hint becomes \N (NULL); backslashes in a VIN are escaped.
  awk -F'\t' '{ sub(/\r$/, "") } /^[[:space:]]*(#|$)/ { next }
    { vin = $1; gsub(/\\/, "\\\\", vin); print vin "\t" (NF > 1 && $2 != "" ? $2 : "\\N") }' "$vins" |
    psql_stdin -q -c 'copy oracle.input (vin, year_hint) from stdin'
  # The procedure's defaults, as NHTSA ships them, except the per-VIN model-year
  # hint. A procedure, because only CALL may COMMIT between VINs.
  psql_stdin -q <<'SQL'
create table oracle.answer_key as
  select i.input_row, i.vin, i.year_hint, d.*
  from oracle.input i cross join lateral vpic.spvindecode(v => i.vin, year => i.year_hint) d
  with no data;
create table oracle.vin_time (input_row bigint primary key, ms numeric not null);
create procedure oracle.decode() language plpgsql as $$
declare
  r oracle.input;
  started timestamptz;
begin
  for r in select * from oracle.input order by input_row loop
    started := clock_timestamp();
    begin
      insert into oracle.answer_key
        select r.input_row, r.vin, r.year_hint, d.*
        from vpic.spvindecode(v => r.vin, year => r.year_hint) d;
    exception when others then
      raise exception 'spvindecode failed on input row % (VIN %, hint %): %',
        r.input_row, r.vin, r.year_hint, sqlerrm;
    end;
    insert into oracle.vin_time
      values (r.input_row, round(extract(epoch from clock_timestamp() - started) * 1000, 3));
    commit;
  end loop;
end $$;
call oracle.decode();
SQL
  psql_ -q -c 'copy (select * from oracle.answer_key k
                     order by input_row, itemelementid, k::text collate "C")
      to stdout with (format csv, header)' >"$out"
  missing=$(psql_ -tA -c 'select count(*) from oracle.input i
      where not exists (select from oracle.answer_key k where k.input_row = i.input_row)')
  [ "$missing" -eq 0 ] || die "$missing VINs produced no spvindecode rows"
  if [ -n "$times" ]; then
    psql_ -q -c "copy (select input_row, vin, year_hint, ms from oracle.vin_time join oracle.input using (input_row)
                       order by input_row)
        to stdout with (format csv, delimiter E'\t', header)" >"$times"
  fi
  psql_ -tA -c "select format('decoded %s VINs (%s) into %s rows; per VIN %s ms mean, %s ms max (%s); %s',
          count(*), '$mode', (select count(*) from oracle.answer_key), round(avg(ms), 1),
          max(ms), (array_agg(vin order by ms desc))[1], $COLLATION_SQL)
      from oracle.vin_time join oracle.input using (input_row)"
}

# Witnesses: the backquoted VINs opening the rows of the "## Witnesses" table
# in each docs/deviations/<class>.md, as "<class>\t<vin>".
witnesses() {
  local f
  for f in "$DEVIATIONS"/*.md; do
    awk -v class="$(basename "$f" .md)" '
      /^## / { in_witnesses = ($0 == "## Witnesses") }
      in_witnesses && /^\| `[^`]+` \|/ { split($0, cell, "`"); print class "\t" cell[2] }' "$f"
  done
}

# The refresh corpus always includes every witness (ADR 0006): print the list
# as given, then each witness it does not already list without a model-year hint.
cmd_corpus() {
  local vins="${1:?vins.tsv}"
  [ -r "$vins" ] || die "cannot read $vins"
  witnesses | awk -F'\t' '
    FILENAME == ARGV[1] { print; sub(/\r$/, ""); if ($2 == "") listed[$1]; next }
    !($2 in listed) {
      if ($1 != class) { class = $1; print "# " class " witnesses (docs/deviations/" class ".md)" }
      print $2; listed[$2]
    }' "$vins" -
}

# A scratch schema in the stock database holding answer keys as text.
cmp_schema() {
  psql_ -q -c 'set client_min_messages = warning' \
           -c 'drop schema if exists oracle_cmp cascade' \
           -c 'create schema oracle_cmp'
}

# HEADER MATCH refuses a CSV that is not an answer key.
load_key() {
  local table="$1" csv="$2"
  [ -r "$csv" ] || die "cannot read $csv"
  psql_ -q -c "create table oracle_cmp.$table (${KEY_COLUMNS//,/ text,} text)"
  psql_stdin -q -c "copy oracle_cmp.$table from stdin with (format csv, header match)" <"$csv"
}

# Both keys into oracle_cmp.stock and oracle_cmp.freshened, refusing two keys
# of different VIN lists.
load_keys() {
  local stock="$1" fresh="$2" differ
  cmp_schema
  load_key stock "$stock"
  load_key freshened "$fresh"
  differ=$(psql_ -tA -c 'select count(*) from (
      (select input_row, vin, year_hint from oracle_cmp.stock
       except select input_row, vin, year_hint from oracle_cmp.freshened)
      union all
      (select input_row, vin, year_hint from oracle_cmp.freshened
       except select input_row, vin, year_hint from oracle_cmp.stock)) x')
  [ "$differ" -eq 0 ] || die "$stock and $fresh decode different VIN lists ($differ input rows differ)"
}

cmd_stale_cache() {
  local stock="${1:?stock.csv}" fresh="${2:?freshened.csv}" out="${3:?out.jsonl}"
  load_keys "$stock" "$fresh"
  # Per input row and element, the sorted rows of each key; an element differs
  # when the two sets differ, including when only one key has it.
  psql_stdin -q -tA >"$out" <<'SQL'
with
s as (select input_row, itemelementid, min(vin) vin, min(year_hint) year_hint,
             array_agg(k::text order by k::text) key_rows from oracle_cmp.stock k group by 1, 2),
f as (select input_row, itemelementid, min(vin) vin, min(year_hint) year_hint,
             array_agg(k::text order by k::text) key_rows from oracle_cmp.freshened k group by 1, 2)
select jsonb_build_object(
         'vin', min(coalesce(s.vin, f.vin)),
         'year', min(coalesce(s.year_hint, f.year_hint))::int,
         'elements', jsonb_agg(itemelementid::int order by itemelementid::int))
from s full join f using (input_row, itemelementid)
where s.key_rows is distinct from f.key_rows
group by input_row
order by input_row::bigint;
SQL
  psql_ -tA -c "select format('stale-cache: %s of %s VINs differ between the stock and freshened keys',
      $(wc -l <"$out" | tr -d ' '), count(distinct input_row)) from oracle_cmp.stock"
}

cmd_recheck() {
  local stock="${1:?stock.csv}" fresh="${2:?freshened.csv}" out="${3:?out.jsonl}" class vin response missing
  load_keys "$stock" "$fresh"
  psql_stdin -q <<'SQL'
create table oracle_cmp.live (line bigserial primary key, doc jsonb not null);
-- A value as a set: the distinct ", "-separated parts, in byte order.
create function oracle_cmp.norm(vals text[]) returns text immutable language sql as $$
  select coalesce(string_agg(distinct part, ', ' order by part), '')
  from unnest(vals) v, regexp_split_to_table(v, ', ') part where part <> ''
$$;
SQL
  # One JSON document per witness: its live Results[0], or why there is none.
  witnesses | while IFS=$'\t' read -r class vin; do
    if response=$(curl -fsS --retry 3 --max-time 60 \
         "$VPIC_API/DecodeVinValues/$(jq -rn --arg v "$vin" '$v | @uri')?format=json" 2>&1) &&
       response=$(jq -ce '.Results[0] | objects // error("no Results[0] object")' <<<"$response" 2>&1); then
      jq -cn --arg class "$class" --arg vin "$vin" --argjson live "$response" '{$class, $vin, $live}'
    else
      jq -cn --arg class "$class" --arg vin "$vin" --arg error "$response" '{$class, $vin, $error}'
    fi
  done | psql_stdin -q -c "copy oracle_cmp.live (doc) from stdin with (format csv, quote e'\x01', delimiter e'\x02')"
  missing=$(psql_ -tA -c "select string_agg(doc->>'vin', ' ') from oracle_cmp.live l
      where not exists (select from oracle_cmp.freshened f where f.vin = l.doc->>'vin' and f.year_hint is null)")
  [ -z "$missing" ] || die "witnesses missing from the keys without a model-year hint (decode the corpus command's list): $missing"
  psql_stdin -q -tA <<'SQL' | jq -c . >"$out"
with
w as (select line, doc->>'class' class, doc->>'vin' vin, doc->'live' live, doc->>'error' error
      from oracle_cmp.live),
s as (select vin, itemelementid::int element, min(code) code, oracle_cmp.norm(array_agg(coalesce(value, ''))) v
      from oracle_cmp.stock where year_hint is null and vin in (select vin from w) group by 1, 2),
f as (select vin, itemelementid::int element, min(code) code, oracle_cmp.norm(array_agg(coalesce(value, ''))) v
      from oracle_cmp.freshened where year_hint is null and vin in (select vin from w) group by 1, 2),
-- Every element either key decodes, where live vPIC has a key for it.
field as (select w.line, element, coalesce(f.code, s.code) code,
                 coalesce(s.v, '') stock, coalesce(f.v, '') freshened,
                 oracle_cmp.norm(array[w.live ->> coalesce(f.code, s.code)]) live
          from (f full join s using (vin, element)) join w using (vin)
          where w.live ? coalesce(f.code, s.code))
select case when w.error is not null
  then json_build_object('class', w.class, 'vin', w.vin, 'error', w.error)
  else json_build_object(
    'class', w.class, 'vin', w.vin,
    'fields', count(field.element),
    'disputed', count(*) filter (where field.stock <> field.freshened),
    'agrees', count(*) filter (where field.live <> field.freshened) = 0,
    'disagree', coalesce(json_agg(json_build_object('element', field.element, 'code', field.code,
                                    'stock', field.stock, 'freshened', field.freshened, 'live', field.live)
                                  order by field.element)
                         filter (where field.live <> field.freshened), '[]'))
  end
from w left join field using (line)
group by w.line, w.class, w.vin, w.error
order by w.line;
SQL
  jq -rs '"recheck: \(length) witnesses; \(map(select(.agrees == true)) | length) agree with live vPIC on every field, \(map(select(.agrees == false)) | length) disagree, \(map(select(.error)) | length) unchecked; \(map(.fields // 0) | add // 0) fields compared, \(map(.disputed // 0) | add // 0) of them disputed by stock and freshened"' "$out"
}

cmd_down() { docker rm -f "$CONTAINER" >/dev/null; }

cmd="${1:-}"; shift || true
case "$cmd" in
  month | fetch | up | restore | freshen | corpus | decode | stale-cache | recheck | down) ;;
  *) usage ;;
esac
label=$cmd; if [ "$cmd" = decode ]; then label="decode ${1:-}"; fi
start=${EPOCHREALTIME:-$(date +%s)}
"cmd_${cmd//-/_}" "$@"
if [ -n "${ORACLE_TIMINGS:-}" ]; then
  end=${EPOCHREALTIME:-$(date +%s)}
  awk -v c="$label" -v s="$start" -v e="$end" 'BEGIN { printf "%s\t%.1f\n", c, e - s }' >>"$ORACLE_TIMINGS"
fi

// ci-matrix probe
