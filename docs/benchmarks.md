# Benchmarks

How many VINs a second each engine decodes, with every row measured on one
machine over one corpus, against NHTSA's own `spVinDecode` in SQL Server.
The README's [Benchmarks](../README.md#benchmarks) table summarizes this
page. The method is [ultravin](https://github.com/blackthorn-interstellar/ultravin)'s
([docs/BENCHMARKS.md](https://github.com/blackthorn-interstellar/ultravin/blob/main/docs/BENCHMARKS.md),
[scripts/bench/](https://github.com/blackthorn-interstellar/ultravin/tree/main/scripts/bench)),
so its rows and nitrovin's compare. ultravin's published figures come from
another machine (an M2 Max, at `56cf2e9`) and are not used here: its rows
below were re-run by its own harness on this machine. Every trial, with the
host's load and busiest processes when it started, is in
[benchmarks.json](benchmarks.json).

## Results

Medians of two fresh-process trials, in VIN/s; "vs MSSQL" divides by the
SQL Server median, 55.8.

| Row | Workers | Input | Median | Trials (run order) | vs MSSQL |
|---|---:|---|---:|---|---:|
| nitrovin, Go module | 12 | sorted | 154,313 | 158,125 / 150,501 | 2,766× |
| nitrovin, Go module | 12 | random | 146,881 | 152,038 / 141,723 | 2,632× |
| nitrovin, Go module | 4 | sorted | 92,743 | 91,073 / 94,413 | 1,662× |
| nitrovin, Go module | 4 | random | 82,173 | 83,136 / 81,211 | 1,473× |
| nitrovin, Go module | 1 | sorted | 26,435 | 26,524 / 26,346 | 474× |
| nitrovin, Go module | 1 | random | 23,987 | 23,128 / 24,845 | 430× |
| nitrovin, CLI | 1 process | random | 14,524 | 14,491 / 14,557 | 260× |
| ultravin | 12 | sorted | 5,288,394 | 5,372,355 / 5,204,434 | 94,779× |
| ultravin | 12 | random | 2,340,410 | 2,398,404 / 2,282,415 | 41,945× |
| ultravin | 4 | sorted | 2,688,609 | 2,687,652 / 2,689,567 | 48,186× |
| ultravin | 4 | random | 1,062,987 | 1,049,717 / 1,076,257 | 19,051× |
| ultravin | 1 | sorted | 656,545 | 675,078 / 638,011 | 11,767× |
| ultravin | 1 | random | 301,844 | 259,014 / 344,674 | 5,410× |
| corgi v3 | 1 | random (corpus) | 149,565 | 146,792 / 152,338 | 2,681× |
| corgi v3 | 1 | sample | 121,714 | 124,294 / 119,135 | 2,181× |
| corgi v2 | 1 | sample | 59.9 | 60.3 / 59.5 | 1.07× |
| NHTSA MSSQL | 1 connection | sample | 55.8 | 55.0 / 56.6 | 1× |
| NHTSA Postgres | 1 connection | sample | 66.1 | 64.7 / 67.4 | 1.18× |
| NHTSA vPIC web API | — | — | ~10, cited | not measured | 0.18× |

The README shows corgi v3's corpus row, which decodes the same 20,000,000
VINs as nitrovin and ultravin; on the 2,000-VIN sample, which fits in
cache and repeats about 600 times in ten seconds, it reads 19% slower than
on the corpus. ultravin's one-worker random trials are 33% apart, the
widest pair; the host was shared (below), and the median is all the table
claims.

**Run order.** Round one, from 20:53 UTC: nitrovin then ultravin at 1, 4
and 12 workers on random input, the same on sorted input, then the CLI,
corgi v3 (sample), corgi v2, Postgres and SQL Server. Round two, in
reverse: SQL Server, Postgres, corgi v2 and corgi v3 (sample), until
22:24 UTC, when the run was paused for unrelated work and the CLI trial in
flight was discarded. It resumed at 22:49 UTC with corgi v3 on the corpus
(its first trial), then the CLI, ultravin then nitrovin at 12, 4 and 1
workers on sorted input, the same on random, and corgi v3 on the corpus
again, ending 00:14 UTC. Twice in round two the script was stopped between
trials, for a minute each, while another agent on this Mac timed a
browser; no trial was running then.

**Host.** Before each trial `scripts/bench.sh` waited up to five minutes
for a one-minute load average of 4 or less (ultravin's runs waited for
3.5), and recorded the load and the five busiest processes either way. The
load when trials started ranged from 1.9 to 4.0; 17 of the 36 trials
waited, the longest 221 s. The other load was this Mac's usual work: other
agents' builds and tests, a Linux VM (Docker Desktop, which also hosted the
oracles), browsers. Each trial's `at` in `benchmarks.json` is when it
finished.

**What dominates nitrovin's per-VIN cost.** A CPU profile of the
one-worker Go row (`go run ./bench/decode throughput -workers 1
-cpuprofile …` over the corpus's first million VINs) puts 88% of the time
in `Decoder.Decode`: the decode passes, up to four per VIN as in
`spvindecode`, take 46%; filling in vehicle-type defaults 24% (13% in a
linear duplicate check, `slices.Contains` over the items); building the
`vpic` layout 14% (`contract.NewDecode`); allocation and garbage
collection about a sixth throughout.

**One VIN, one process.** `nitrovin decode 1HGCM82633A004352` (v0.1.8),
11 runs after one to page the binary in: median 9.8 ms wall and 32 MB peak
resident memory (`scripts/bench.sh process`; the runs are under `process`
in `benchmarks.json`). The README's earlier figure, 6 ms and 22 MB, was
v0.1.2's.

## Method

**Machine.** Apple M5 Pro, 6 performance and 12 efficiency cores, 48 GB,
macOS 27.2.1, Docker Desktop 29.9.3 (a Linux VM with 18 CPUs and 8 GB).

**Corpus.** 20,000,000 unique VINs from ultravin's generator,
`scripts.bench.large_corpus --count 20000000` at ultravin `deb4e2f`, which
calls `ultravin.generate(n, seed=42 + chunk, now=2026-09-01, invalid_rate=0.05)`
on the 2026_10 data month: VINs drawn from each WMI's own patterns and kept
only if they decode cleanly, and 5% given one realistic defect (a mistyped
character, a wrong check digit, or two adjacent characters swapped);
924,767 fail the check digit; 12,670 WMIs.

| File | VINs | SHA-256 |
|---|---:|---|
| `corpus.txt` | 20,000,000 | `14950b83966d7858ccbc713770e8020d4bbd3b9b8b18fadcbafa70e6f3fa46ec` |
| `corpus-sorted.txt` (`LC_ALL=C sort`) | 20,000,000 | `11c4f519b570c1e42d6c18b0360035502cd3b0b34993cd89046bfb7bbcdd1f15` |
| `sample.txt` (2,000 lines, Python `random.Random(162).sample`, in corpus order) | 2,000 | `ebfa616ec7ab5cb37d6ec391d9f21224e57e12fbd3fdfe09937965fb98017a77` |

Nothing of the corpus is committed; `scripts/bench.sh corpus` rebuilds it
byte for byte.

**Engines.**

| Row | What runs | Version |
|---|---|---|
| nitrovin, Go module | `bench/decode throughput`: `nitrovin.Embedded()`, then `Decoder.Decode` (shape `vpic`, no items) on N goroutines under `GOMAXPROCS=N`, 256 VINs a grab, decodes discarded | decoder and data of v0.1.8 (`4834e5c`; the branch added only the harness), Go 1.27.3 |
| nitrovin, CLI | the release binary, `nitrovin decode --now 2026-09-01T00:00:00Z -`, the corpus on stdin, JSON lines out to a pipe the harness counts, `GOMAXPROCS=1` | v0.1.8, `nitrovin_0.1.8_darwin_arm64.tar.gz`, SHA-256 `970270da…69be` |
| ultravin | its headline command, `target/release/examples/throughput <corpus> 10 batch full auto` (Rust, full results, automatic batching), `RAYON_NUM_THREADS=N`, `ULTRAVIN_NOW_MICROS=1788220800000000` | `deb4e2f0415563dea9ff22fc0c98ef59853884a2`, Rust 1.99.1, data built by its `make data` from NHTSA's 2026_10 plain dump (SHA-256 `863445df…2282`), `vpic.rkyv` `6f2aebd2…b48e`, binary `e36b4a5f…232e` |
| corgi v3 | `bench/decode/corgi.mjs v3`: `createDecoder(<index dir>)`, then `decode(vin)` one at a time | `@cardog/corgi` 3.0.1-beta.0, its indexes from 3.0.0-alpha.1, Node 22.23.4 |
| corgi v2 | `bench/decode/corgi.mjs v2`: `createDecoder()` on its bundled SQLite, then `await decode(vin)` one at a time | `@cardog/corgi` 2.0.1, better-sqlite3 9.6.0, Node 22.23.4 |
| NHTSA Postgres | `scripts/oracle.sh decode stock`: the stock dump in Postgres, collation `C`, one connection, `vpic.spvindecode` per VIN inside one procedure call, one transaction per VIN | Postgres 17.11 (`postgres:17@sha256:ff871389…b4f`), dump `vPICList_lite_2026_10.custom.zip` SHA-256 `373e6a87…d2c1` |
| NHTSA MSSQL | `scripts/bench.sh mssql`: one `sqlcmd` session, `EXEC dbo.spVinDecode @v = '<vin>'` per VIN, result sets read and discarded | SQL Server 2022 Developer RTM-CU27-GDR 16.0.4315.2, `mcr.microsoft.com/mssql/server:2022-latest@sha256:11927f5e…73fd`, `linux/amd64` under Docker's emulation; NHTSA's `vPICList_lite_2026_10.bak.zip` SHA-256 `c9802252…02cd` |

**Clock.** nitrovin and ultravin decode as of 2026-09-01T00:00:00Z,
ultravin's headline clock. corgi and the two SQL procedures have no clock
to freeze; they read today's.

**Timing.** Every trial is a fresh process. As in ultravin's `throughput`,
each in-process harness loads its decoder untimed, warms up untimed, then
times complete passes over its input until at least 10 seconds have
elapsed, and reports rows over elapsed seconds. ultravin warms on the
whole corpus, filling its per-thread caches; nitrovin keeps no cache
across VINs and warms on the corpus's first 1,000,000; corgi warms on 20.
One pass of the corpus outlasts ten seconds for nitrovin and corgi, so
each of their trials is one pass of all 20,000,000 VINs (nitrovin's
one-worker trial is 14 minutes). ultravin times one pass at one worker and
at four on random input, two or three otherwise. The CLI row is the wall
time of one `nitrovin decode -` process over the corpus, start-up
included. The SQL rows sum each VIN's
time inside the server: Postgres's per-VIN clock in `scripts/oracle.sh`,
SQL Server's `sysdatetime()` around each `EXEC`. Their wall time, which
adds loading the list and the client, is in `benchmarks.json` (Postgres
36.4 and 34.7 s, SQL Server 40.8 and 38.9 s, for 2,000 VINs).

## Correctness aside

Speed means little without the same answers. On the 2,000-VIN sample
(`scripts/bench.sh agree`), the share of decodes that agree with the
Postgres oracle element for element: for every element either side emits,
the same values, as a multiset per VIN, NULL and the empty string alike
(ultravin's Python API renders some NULLs as `""`). The other 13 columns
of each row are not compared here; nitrovin's own gate compares all 15
([parity](parity.md)).

| Engine | Freshened oracle | Stock oracle |
|---|---:|---:|
| nitrovin v0.1.8 | 2,000 / 2,000 (100%) | 1,998 / 2,000 (99.9%) |
| ultravin `deb4e2f` | 2,000 / 2,000 (100%) | 1,998 / 2,000 (99.9%) |

The two stock disagreements, `1ACCW0597DB684525` and `1A9D01306N6925202`,
are the same for both engines: the stock dump's stale valid-characters
cache, the one registered deviation
([`stale-cache`](deviations/stale-cache.md)), which both engines recompute
as the freshened oracle does.

Beyond the sample, 14 of the corpus's 20,000,000 VINs, all `3FMTK…`
(2023 Mustang Mach-E), ended in an Internal error in v0.1.8:
"element 1 (Other Battery Info) has several items, but it is not
multi-valued" ([#165](https://github.com/joshuasprow/nitrovin/issues/165),
fixed after this measurement in #167). The Go rows decode them and count
them in each trial's `errors`; `nitrovin decode -` stops at the first, so
the CLI row decodes the corpus without them (19,999,986 VINs,
`bench/decode refused` lists them).

corgi does other work than the procedure: v3 returns about 20 summary
fields (make, model, trim, engine, plant) from its own index and marks
3,900,796 of the 20,000,000 VINs (19.5%) valid; v2 marks 598 of the 2,000
sample VINs valid. Neither was compared with the oracle.

## Not re-measured here

- **NHTSA vPIC web API.** Not a decode measurement. NHTSA publishes no
  number: [its API page](https://vpic.nhtsa.dot.gov/api/) says only that
  users "will be controlled by an automated traffic rate control mechanism"
  (read 2026-11-10). The ~10 a second is
  [Cardog's figure](https://cardog.app/blog/corgi-vin-decoder) ("Rate
  limits: ~10 requests/second", read 2026-11-10), which ultravin also cites.

Every other row was measured here, the SQL Server baseline included.

## Reproduce

Each command below runs one trial of one row; run it twice for the two
trials, or `scripts/bench.sh all` for two trials of every row in the order
above. From a checkout with `mise.toml`'s tools on PATH, Docker, and
ultravin built at the commit above (about 4 GB of disk in all):

```sh
git clone https://github.com/blackthorn-interstellar/ultravin /tmp/ultravin
git -C /tmp/ultravin checkout deb4e2f0415563dea9ff22fc0c98ef59853884a2
(cd /tmp/ultravin && make download MONTH=2026_10 &&
  make data DUMP=downloads/vPICList_lite_2026_10.plain.zip MONTH=2026_10 &&
  cargo build -p ultravin --release --example throughput --locked &&
  uv run --frozen maturin develop --uv --release --locked)
export ULTRAVIN=/tmp/ultravin W=/tmp/bench && mkdir -p "$W"
gh release download v0.1.8 -R joshuasprow/nitrovin -p nitrovin_0.1.8_darwin_arm64.tar.gz -O - | tar -xz -C "$W" nitrovin

scripts/bench.sh corpus "$W"                          # corpus, sorted copy, sample; prints corpus.json

scripts/bench.sh go "$W" 1 random                     # nitrovin, Go module (workers 1, 4 or 12; random or sorted)
scripts/bench.sh cli "$W" "$W/nitrovin"               # nitrovin, CLI
scripts/bench.sh ultravin "$W" 1 random               # ultravin (workers 1, 4 or 12; random or sorted)
mise exec node@22 -- scripts/bench.sh corgi "$W" v3 random   # corgi v3 on the corpus
mise exec node@22 -- scripts/bench.sh corgi "$W" v3   # corgi v3 on the sample; v2 likewise

scripts/oracle.sh up && scripts/oracle.sh fetch 2026_10 /tmp/vpic &&
  scripts/oracle.sh restore /tmp/vpic/vPICList_lite_2026_10.backup && scripts/oracle.sh freshen
scripts/bench.sh postgres "$W"                        # NHTSA Postgres

curl -fsSLO https://vpic.nhtsa.dot.gov/downloads/vPICList_lite_2026_10.bak.zip && unzip vPICList_lite_2026_10.bak.zip -d /tmp/bak
scripts/bench.sh mssql-up /tmp/bak                    # NHTSA MSSQL
scripts/bench.sh mssql "$W"

scripts/bench.sh agree "$W"                           # the correctness aside
scripts/bench.sh process "$W" "$W/nitrovin"           # one VIN, one process
scripts/bench.sh report "$W" docs/benchmarks.json     # medians, and this page's numbers
scripts/bench.sh mssql-down; scripts/oracle.sh down
```

`scripts/bench.sh`'s header documents each command and its environment
(`BENCH_MAX_LOAD`, `BENCH_WAIT`, `ORACLE_CONTAINER`, `MSSQL_CONTAINER`);
`bench/decode`'s, the Go harness. corgi needs Node 22: corgi v2's
better-sqlite3 9 has no build for Node 24 and later, which `mise.toml` pins. corgi v2
unpacks its database into `~/.corgi-cache` on first use. NHTSA's
downloads page lists only the newest month; once 2026_10 is gone, its
Postgres dump is an asset of nitrovin-data's release (see
[CONTRIBUTING](../CONTRIBUTING.md#the-postgres-oracle)), and nitrovin keeps
no copy of its `.bak`.

## Decisions made while building

- **SQL Server was measured, not cited.** `mcr.microsoft.com/mssql/server:2022-latest`
  runs under Docker's amd64 emulation on this Mac and restores NHTSA's
  `.bak` in seconds, so the baseline column divides by a measurement
  (55.8 VIN/s, where ultravin's M2 Max measured 22.5). Emulation
  understates SQL Server against native x86.
- **corgi v3 is a prerelease, run over the whole corpus.** npm has no
  stable v3: the newest is 3.0.1-beta.0, which ships no index files, so
  it reads those of 3.0.0-alpha.1, the one v3 that ships them (the two
  share `index-reader.js` byte for byte). The issue lists corgi among the
  slow engines that decode a sample, but v3 decodes about 150,000 VIN/s,
  so it also ran over the corpus, the input nitrovin's and ultravin's
  rows decode, and the README shows that row.
- **corgi v2 on Node 22**, not the Node `mise.toml` pins: its
  better-sqlite3 9 does not build on Node 24 and later.
- **The SQL rates are server time.** Both are summed per-VIN times inside
  the server, one connection, so a client library's overhead is not
  charged to NHTSA's procedure; the wall times are recorded beside them.
- **nitrovin warms on 1,000,000 VINs, not the whole corpus.** ultravin
  warms on its whole input to fill caches nitrovin does not have; a
  20,000,000-VIN warm-up would add 14 minutes to every one-worker trial
  and change nothing measured.
- **nitrovin's CLI row is the release binary of the measured commit.**
  v0.1.8 is `4834e5c`, the decoder the Go rows ran; v0.2.0 was published
  during the run and is not in the table.
- **#165** ([above](#correctness-aside)): the Go rows decode all
  20,000,000 VINs and count the 14 failures; the CLI row leaves them out.
- **Agreement is element for element on values**, not all 15 columns,
  because ultravin's Python API exposes a subset of columns in its own
  formats; NULL and `""` count as equal for the same reason. Both oracle
  modes are reported.
- **Load threshold 4**, above ultravin's 3.5: this Mac carried other
  agents' work all day; every trial's load and busiest processes are
  recorded instead.
- **The harness lives in `bench/decode`**, beside `bench/vocab`, not under
  `pkg/`: it is not API. It is `//go:build unix` (it reads rusage).
