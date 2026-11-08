# `stale-cache`: the dump's valid-characters cache contradicts its pattern rows

First seen in nitrovin's first data month, 2026_10. ultravin first observed it
in 2026_08.

## Root cause

`spvindecode_errorcode` (`spvindecode_errorcode.sql:46-66` in the dump)
looks up which characters are valid at each VIN position for the decode's
WMI and model year. It reads them from `vpic.wmiyearvalidchars` when that
(WMI, year) cell has any rows. Only when the cell is empty does it compute
them with `vpic.fExtractValidCharsPerWmiYear`, from the `pattern` keys of
every schema whose band covers the year. The table is meant to be a
materialised copy of that function. It is not. In 2026_10 it disagrees with
the function in 5,706 cells, which is **the dump contradicting itself**: the
cache and the pattern rows ship in the same file and cannot both be right.
In 5,013 of those cells the recompute finds no characters at all. The rest:
198,338 rows appear only in the cache and 984 only in the recompute
(ultravin's `stale_cache_cells.json` summary for 2026_10).

A stale cell changes a decode in three ways:

- **A character the pattern rows reject passes.** The cell lists it, so a
  pass that should take a correction code takes none.
- **A character the pattern rows allow is flagged.** The cell lacks it, so a
  clean pass is penalized.
- **A position is never checked.** The cell has no row for it at all.

Usually only the correction elements change: 142 Suggested VIN, 143 Error
Code, 144 Possible Values, 156 Additional Error Text and 191 Error Text. When
the change flips which model-year pass wins, the vehicle changes too.

Live vPIC answers every witness below the way the recompute does, so NHTSA's
production cache is fresh, or not read at all.

## Detector

The refresh decodes its corpus twice: on the stock dump, and on a copy with
`TRUNCATE vpic.wmiyearvalidchars`. `spvindecode_errorcode` is the table's
only reader, so emptying it only sends every charset read to the dump's own
fallback. Every VIN whose two answers differ belongs to this class. The list
goes in the refresh report and as a release asset. `scripts/oracle.sh
stale-cache` implements the diff, and `.github/workflows/oracle.yml` uploads
the list as `stale-cache.jsonl`.

On 2026_10 the freshened oracle reproduced ultravin's answer on 100 of 100
VINs that ultravin excuses as this class, and changed nothing on 100 VINs it
does not excuse. The detector flagged all of a random 100 of the 5,215 VINs
ultravin's answer key marks `~` (this class) and none of a random 100 it
leaves unmarked.

## What nitrovin does

nitrovin recomputes every charset from the pattern keys and never reads the
cache, so it answers as the freshened oracle does. The refresh gate compares
nitrovin with the freshened oracle and allows no differences.

The recompute is `charset` in `pkg/decoder`. `TestCharsetPostgres` checks it
against `fExtractValidCharsPerWmiYear` on every (WMI, year) cell and logs the
cache's stale cells; on 2026_10 they are ultravin's 5,706, position for
position.

## Witnesses

Data month 2026_10. On 2026-11-08 live vPIC `DecodeVinValues` equalled the
freshened oracle on every VIN, on the four fields below and on Make
(element 26). Make is left out of the table because it differs only on
`10T2CE2A?LPY1111S`, where stock gives none and freshened and live give
OSHKOSH. `scripts/oracle.sh recheck`, run by every refresh, compares each
witness with live vPIC on every element either oracle decodes: on the same
day, all 253 such fields agreed.

| VIN | answer | model year | error codes | suggested VIN | possible values |
|---|---|---|---|---|---|
| `10T2CE2A?LPY1111S` | stock | 2020 | 4,14,400 | `10T!CE2A!LPY1111!` | `(4:ABCDEFHJSZ)` |
| | freshened, live | 1990 | 5,14,400 | `10T2C!2A!L!Y1111!` | `(6:0123456789)(11:12S)` |
| `10TAK8H20RSAA0000` | stock | 2024 | 0,14 | | |
| | freshened, live | 1994 | 4,14 | `10T!K8H20RSAA0000` | `(4:123456789M)` |
| `1GD67MHFXPA077111` | stock | 2023 | 3,14 | `1GD67MHFXP1077111` | `(11:1)` |
| | freshened, live | 2023 | 5,14 | `1GD67MH!XP!077111` | `(8:178DKLPTY)(11:1FZ)` |
| `1GTW7NFH5PA077131` | stock | 2023 | 1,3,14 | `1GTW7NFH5PZ077131` | `(11:Z)` |
| | freshened, live | 2023 | 1,5,14 | `1GTW7NF!5P!077131` | `(8:178ADKLPTY)(11:1FUZ)` |
| `1GT289EF0P9P77111` | stock | 2023 | 3,14 | `1GT289EF0PZP77111` | `(11:Z)` |
| | freshened, live | 2023 | 5,14 | `1GT289E!0P!P77111` | `(8:178ADKLPTY)(11:1FUZ)` |
| `1GD67LHF3RA077111` | stock | 2024 | 3,14 | `1GD67LHF3R1077111` | `(11:1)` |
| | freshened, live | 2024 | 5,14 | `1GD67LH!3R!077111` | `(8:178CDKLPY)(11:1FZ)` |
| `2APCDT9R0TC111111` | stock | 2026 | 1,3,14 | `2APCDTAR0TC111111` | `(7:A)` |
| | freshened, live | 2026 | 1,5,14 | `2APCDT!!0T!111111` | `(7:ABCDEFGHK)(8:ABCDEFGHJKLMNPST)(11:ELR)` |

The two 10T VINs change vehicle: the stale cell makes the wrong model-year
pass win. On the others only the correction elements change.

## Credit

Found, root-caused and evidenced by
[ultravin](https://github.com/blackthorn-interstellar/ultravin) (MIT), in
`docs/KNOWN_DEVIATIONS.md` §2 ("Stale `WMIYearValidChars` cache") and §5
("Stale cache extra error takes the multi-position rung") at `dbc9777`. All
seven witnesses come from its `scripts/known_problems.json`. We re-derived
their answers on our own oracle and live vPIC rather than copying ultravin's.
The freshened-oracle detector is ours. ultravin instead rolls back individual
cells and excuses only the correction elements by rule.
