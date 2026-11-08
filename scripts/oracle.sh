#!/usr/bin/env bash
# The Postgres oracle: NHTSA's dump restored unmodified into Postgres 17+, and
# its own vpic.spvindecode run over a VIN list to export an answer key.
# CI (.github/workflows/oracle.yml) and a workstation run the same commands.
#
#   scripts/oracle.sh month                       newest published data month (YYYY_MM)
#   scripts/oracle.sh fetch <month> <dir>         download and unzip that month's custom dump;
#                                                 prints month=, dump_bytes=, dump_sha256=, backup=
#   scripts/oracle.sh up                          workstation only: start a postgres:17 container, collate C
#   scripts/oracle.sh restore <backup>            pg_restore the dump into the container's `vpic` db
#   scripts/oracle.sh decode <vins.tsv> <out.csv> run spvindecode over every VIN, write the answer key
#   scripts/oracle.sh down                        workstation only: remove the container
#
# Every command runs inside $ORACLE_CONTAINER (default nhtsa-oracle) with
# `docker exec`, so the client tools always match the server: the dump's
# archive format 1.16 needs pg_restore 17+, and runner images ship 16.
# If $ORACLE_TIMINGS names a file, each command appends "<command>\t<seconds>".
#
# Collation: the database is initialized with --lc-collate=C and nothing else,
# ultravin's oracle contract (docs/ACCEPTANCE.md, "Oracle collation"): C
# reproduces SQL Server's tie-breaks and a locale-aware collation does not
# (under the image default en_US.utf8, fVinModelYear2's BETWEEN 'A' AND 'H'
# accepts 'É'). LC_CTYPE stays en_US.utf8: a C ctype makes initdb pick
# SQL_ASCII and changes upper(). restore and decode refuse any other collation.
#
# vins.tsv: one VIN per line, optionally a tab and a model-year hint; blank
# lines and lines starting with # are skipped. The answer key is CSV with
# every column spvindecode returns, one row per row the procedure emits
# (an element can repeat), ordered by input line, element id, then the whole
# row. The procedure's own row order is not used: it shifts between restores
# of the same dump, and the key must not.
set -euo pipefail

CONTAINER="${ORACLE_CONTAINER:-nhtsa-oracle}"
IMAGE="${ORACLE_IMAGE:-postgres:17}"
DB=vpic
BASE_URL=https://vpic.nhtsa.dot.gov/downloads

die() { echo "oracle: $*" >&2; exit 1; }

usage() {
  sed -n '6,12s/^#  //p' "$0" >&2
  exit 2
}

PSQL=(psql -X -v ON_ERROR_STOP=1 -U postgres -d "$DB")
INITDB_ARGS=--lc-collate=C
# No -i: docker exec would otherwise swallow the caller's stdin.
psql_() { docker exec "$CONTAINER" "${PSQL[@]}" "$@"; }

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

# "collate <datcollate>, ctype <datctype>" of the vpic database, as SQL.
COLLATION_SQL="(select 'collate ' || datcollate || ', ctype ' || datctype from pg_database where datname = '$DB')"

require_collate_c() {
  local c
  c=$(psql_ -tA -c "select datcollate from pg_database where datname = '$DB'")
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

cmd_decode() {
  local vins="${1:?vins.tsv}" out="${2:?out.csv}"
  [ -r "$vins" ] || die "cannot read $vins"
  require_collate_c
  psql_ -q -c 'set client_min_messages = warning' \
           -c 'drop schema if exists oracle cascade' \
           -c 'create schema oracle' \
           -c 'create table oracle.input (input_row bigserial primary key, vin text not null, year_hint integer)'
  # Text-format COPY: a missing hint becomes \N (NULL); backslashes in a VIN are escaped.
  awk -F'\t' '{ sub(/\r$/, "") } /^[[:space:]]*(#|$)/ { next }
    { vin = $1; gsub(/\\/, "\\\\", vin); print vin "\t" (NF > 1 && $2 != "" ? $2 : "\\N") }' "$vins" |
    docker exec -i "$CONTAINER" "${PSQL[@]}" -q -c 'copy oracle.input (vin, year_hint) from stdin'
  # The procedure's defaults, as NHTSA ships them, except the per-VIN model-year hint.
  # One statement decodes every VIN: identical rows to one session per VIN
  # (checked on the sample), because each call drops its own temp tables.
  psql_ -q -c "create table oracle.answer_key as
      select i.input_row, i.vin, i.year_hint,
             d.groupname, d.variable, d.value, d.itempatternid, d.itemvinschemaid,
             d.itemkeys, d.itemelementid, d.itemattributeid, d.itemcreatedon,
             d.itemwmiid, d.code, d.datatype, d.decode, d.itemsource, d.itemtobeqced
      from oracle.input i
      cross join lateral vpic.spvindecode(v => i.vin, year => i.year_hint) d"
  psql_ -q -c 'copy (select * from oracle.answer_key k
                     order by input_row, itemelementid, k::text collate "C")
      to stdout with (format csv, header)' >"$out"
  local missing
  missing=$(psql_ -tA -c 'select count(*) from oracle.input i
      where not exists (select from oracle.answer_key k where k.input_row = i.input_row)')
  [ "$missing" -eq 0 ] || die "$missing VINs produced no spvindecode rows"
  psql_ -tA -c "select 'decoded ' || (select count(*) from oracle.input) || ' VINs into '
      || count(*) || ' rows, ' || $COLLATION_SQL from oracle.answer_key"
}

cmd_down() { docker rm -f "$CONTAINER" >/dev/null; }

cmd="${1:-}"; shift || true
case "$cmd" in
  month | fetch | up | restore | decode | down) ;;
  *) usage ;;
esac
start=${EPOCHREALTIME:-$(date +%s)}
"cmd_$cmd" "$@"
if [ -n "${ORACLE_TIMINGS:-}" ]; then
  end=${EPOCHREALTIME:-$(date +%s)}
  awk -v c="$cmd" -v s="$start" -v e="$end" 'BEGIN { printf "%s\t%.1f\n", c, e - s }' >>"$ORACLE_TIMINGS"
fi
