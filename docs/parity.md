# Parity

nitrovin claims one thing about its answers: for every VIN, they are the
answers NHTSA's own decoding procedure gives on the same data month. This
page says what is compared, against what, on which VINs, when a mismatch
stops a release, and where each month's evidence is kept. The decisions
behind it are [ADR 0006](adr/0006-deviations-are-detected-classes.md) and
[ADR 0008](adr/0008-parity-corpora-and-gates.md); words are
[`GLOSSARY.md`](../GLOSSARY.md)'s.

## What is compared

A decode's **items** are every row NHTSA's `spvindecode` emits for a VIN: 15
columns per row (element, value, pattern, VinSchema, keys, attribute id,
source, and the rest, listed in [the API contract](api/README.md#items-full)).
Parity means nitrovin's items equal the oracle's:

- all 15 columns, as exact strings (`"2000.0"` and `"2000.00"` differ);
- as a multiset per VIN, so the procedure's unstable row order does not
  matter, while nitrovin's own order must still follow the procedure's group
  ranks;
- with no threshold and no per-VIN excuse: one differing cell on one VIN
  fails the gate.

Both output layouts, `vpic` and `nitrovin`, are pure functions of the items.
The `vpic` layout has no oracle of its own (NHTSA builds it in its web
service, not in the dump), so it is unit-tested and never gated.

## The oracle

The oracle is the dump itself: NHTSA's monthly Postgres backup restored into
Postgres 17 with collation `C`, running the `spvindecode` procedure that
ships in it (`scripts/oracle.sh`, `.github/workflows/oracle.yml`).

It runs **freshened**: on a copy of the database with
`vpic.wmiyearvalidchars` emptied. That table is a cache of which characters
each VIN position may hold, and it contradicts the pattern rows it is
derived from; in 2026_10 it is stale in 5,706 (WMI, model year) cells.
With it empty, the procedure computes those characters from the pattern
rows instead, which is what nitrovin does and what live vPIC answers on
every witness VIN. The VINs on which the stock and freshened oracles differ
are the [`stale-cache`](deviations/stale-cache.md) deviation class, listed
every month and never gated. It is the only registered deviation; the
[registry](deviations/README.md) says what it takes to admit another.

## Which VINs

| Part | Source | 2026_10 |
| --- | --- | --- |
| Committed corpus | `testdata/parity/<month>/corpus.tsv`: the oracle sample, the engine's core, error and wrapper sets, ultravin's parity corpus and cover set, and every deviation witness | 641 calls |
| Mutations | one one-character change per committed VIN, spread over the error codes | 599 |
| Generated | VINs synthesized from the month's own pattern keys until every WMI and VinSchema appears in some decode | 17,078 |

`nitrovin-import vins` writes the list in files of at most 5,000 VINs, one
oracle job each, so a bigger month means more jobs, not a weaker gate. On
2026_10 the 18,318 VINs reach all 13,014 WMIs and 25,283 of 25,293
VinSchemas; the committed corpus alone reaches 383 WMIs. The other 10
schemas lose every decode to another schema on the same WMI and years
([#72](https://github.com/joshuasprow/nitrovin/issues/72)), so no VIN can
reach them.

## The gates

- **Every pull request** that touches decoding runs the engine against a
  restored dump (`import.yml`: the matcher, the core pass, the wrapper and
  the whole projection, live), and offline against the committed frozen key
  (`TestParity` over `testdata/parity/<month>/answer-key.csv`).
- **Every data month**: the refresh (`refresh.yml`, daily) decodes the whole
  list on the stock and freshened oracles, gates each file with
  `TestParity -parity-run`, and only on green publishes the data release,
  merges the `go.mod` bump and tags a nitrovin patch release. Red publishes
  nothing; the previous month stays current and the report goes to an issue
  titled "Refresh <month> is red".
- **Reported, never gated:** the stale-cache count and its change from the
  previous month, the witness re-check against live vPIC, and Go coverage of
  `pkg/decoder`.

## Where each month's report lives

| What | Where | Kept |
| --- | --- | --- |
| The report: per-file gates, coverage, what the list reaches, stale-cache count, witnesses | body of the refresh pull request, titled "Refresh: data month `<month>` (nitrovin-data `<tag>`)": [all of them](https://github.com/joshuasprow/nitrovin/pulls?q=is%3Apr+%22Refresh%3A+data+month%22) | forever |
| The run that gated the month: its logs, per-(VIN, element) `report.jsonl`, `coverage.json` and both oracles' keys | linked from the PR body and from the nitrovin-data release notes ("refresh"); the files are the run's artifacts | 90 days (the repository's Actions retention) |
| The frozen answer key (`corpus.tsv`, `answer-key.csv`, `manifest.json`) | `parity-<month>.tar.gz` on the [nitrovin-data release](https://github.com/joshuasprow/nitrovin-data/releases); the current month also in `testdata/parity/<month>/` | forever |
| The stale-cache list, one line per call: VIN, hint, differing element ids | `stale-cache.jsonl` on the same release | forever |

### 2026_10, as shipped in v0.1.2

[Refresh run 42662006323](https://github.com/joshuasprow/nitrovin/actions/runs/42662006323),
report in [#80](https://github.com/joshuasprow/nitrovin/pull/80), data
release [`v1.202610.1`](https://github.com/joshuasprow/nitrovin-data/releases/tag/v1.202610.1):

| | |
| --- | --- |
| Calls gated | 18,339 in 4 files (18,318 VINs, plus the 7 witnesses repeated in files 2–4) |
| Freshened rows | 966,232 |
| Disagreements | 0 |
| WMIs / VinSchemas reached | 13,014 of 13,014 / 25,283 of 25,293 |
| stale-cache calls | 52 |
| Witnesses against live vPIC | 7 of 7 agree, 253 fields |
| `pkg/decoder` coverage | 89.2 % of statements |

## Check a release yourself

The frozen key is enough to re-run the offline gate on a release with no
Postgres, in well under a second:

```sh
git clone --depth 1 --branch v0.1.2 https://github.com/joshuasprow/nitrovin && cd nitrovin
gh release download v1.202610.1 -R joshuasprow/nitrovin-data -p nitrovin-2026_10.artifact
NITROVIN_ARTIFACT=$PWD/nitrovin-2026_10.artifact go test ./pkg/decoder -run 'TestParity$' -v
```

```text
=== RUN   TestParity
    parity_test.go:102: ../../testdata/parity/2026_10, 2026_10 at 2026-11-09T17:12:22Z: 641 calls, 26747 rows: 0 disagree, 0 misordered
--- PASS: TestParity (0.09s)
```

`parity-2026_10.tar.gz` holds the same three files as the tag's
`testdata/parity/2026_10/`. To produce the oracle's answers yourself, from
the dump on the same release, see
[the Postgres oracle](../CONTRIBUTING.md#the-postgres-oracle).
