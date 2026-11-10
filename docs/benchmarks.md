# Benchmarks

How many VINs a second each engine decodes, every row measured on one
machine over one corpus, against NHTSA's own `spVinDecode` in SQL Server.
The README's [Benchmarks](../README.md#benchmarks) table is this page's
summary. The method is [ultravin](https://github.com/blackthorn-interstellar/ultravin)'s
([docs/BENCHMARKS.md](https://github.com/blackthorn-interstellar/ultravin/blob/main/docs/BENCHMARKS.md),
[scripts/bench/](https://github.com/blackthorn-interstellar/ultravin/tree/main/scripts/bench)),
so that its rows and nitrovin's compare; ultravin's published figures were
taken on another machine and are not copied here. Every trial is in
[benchmarks.json](benchmarks.json).

RESULTS

## Method

**Machine.** HOST

**Corpus.** CORPUS

**Engines.**

| Row | What runs | Version | Workers | Input |
|---|---|---|---|---|
| nitrovin, Go module | `cmd/nitrovin-bench throughput`: `nitrovin.Embedded()`, then `Decoder.Decode` (shape `vpic`, no items) on N goroutines under `GOMAXPROCS=N`, decodes discarded | VERSION_GO | 1, 4, 12 | whole corpus, random and sorted |
| nitrovin, CLI | the release binary: `nitrovin decode --now … -` reading the corpus on stdin, JSON lines to a pipe, `GOMAXPROCS=1` | VERSION_CLI | 1 process | whole corpus but the 14 VINs of [#165](https://github.com/joshuasprow/nitrovin/issues/165) |
| ultravin | its headline command, `target/release/examples/throughput <corpus> 10 batch full auto` (Rust, full results, automatic batching), `RAYON_NUM_THREADS=N`, `ULTRAVIN_NOW_MICROS` | VERSION_UV | 1, 4, 12 | whole corpus, random and sorted |
| corgi v3 | `scripts/bench/corgi.mjs v3`: `@cardog/corgi` 3.0.1-beta.0 in Node 22, `createDecoder(<index dir>)`, `decode(vin)` one at a time | 3.0.1-beta.0, indexes of 3.0.0-alpha.1 | 1 | sample |
| corgi v2 | `scripts/bench/corgi.mjs v2`: `@cardog/corgi` 2.0.1 in Node 22, `createDecoder()` on its bundled SQLite, `await decode(vin)` one at a time | 2.0.1 | 1 | sample |
| NHTSA Postgres | `scripts/oracle.sh decode stock`: the stock 2026_10 dump restored into Postgres 17, collation `C`, one connection, `vpic.spvindecode` per VIN in one procedure call | VERSION_PG | 1 connection | sample |
| NHTSA MSSQL | `scripts/bench.sh mssql`: NHTSA's `.bak` for 2026_10 restored into SQL Server 2022 (Developer, `linux/amd64` under Docker's emulation), one `sqlcmd` session, `EXEC dbo.spVinDecode` per VIN | VERSION_MSSQL | 1 connection | sample |

**Clock.** nitrovin and ultravin decode as of 2026-09-01T00:00:00Z,
ultravin's headline clock. corgi and the two SQL procedures have no clock
to freeze; they read today's.

**Timing.** Each trial is a fresh process. Like ultravin's `throughput`,
every in-process harness loads the decoder untimed, warms up untimed, then
times complete passes over its input until at least 10 seconds have
elapsed, and reports rows over elapsed seconds. ultravin warms on the whole
corpus, filling its per-thread caches; nitrovin keeps no cache across VINs,
so its harness warms on the corpus's first 1,000,000 VINs; corgi warms on
20 VINs. The CLI row is the wall time of one `nitrovin decode -` process
over the corpus, start-up included. The SQL rows sum each VIN's time inside
the server (Postgres: the procedure's own per-VIN clock in
`scripts/oracle.sh`; SQL Server: `sysdatetime()` around each `EXEC`); the
wall time, which adds loading the list and the client, is in the trials.

**Trials.** Two per row, in two rounds: every row once (nitrovin and
ultravin alternating at each worker count, random input then sorted, then
the CLI, corgi v3, corgi v2, Postgres, SQL Server), then every row again in
reverse order. The table gives the median of the two (their mean); every
trial is below. Before each trial `scripts/bench.sh` waited up to five
minutes for a one-minute load average of 4 or less and recorded the load
and the busiest processes either way.

**vs NHTSA MSSQL** divides each row's rate by SQL Server's.

## Correctness aside

Speed means nothing without the same answers. On the sample, the share of
decodes that agree with the Postgres oracle element for element: for every
element either side emits, the same values, as a multiset per VIN (NULL and
the empty string alike, since ultravin's Python API renders some NULLs as
`""`). The other 13 columns are not compared here; nitrovin's gate compares
all 15 ([parity](parity.md)).

AGREE

## Not re-measured here

- **NHTSA vPIC web API.** Not a decode measurement. NHTSA publishes no
  number: [its API page](https://vpic.nhtsa.dot.gov/api/) says only that
  users "will be controlled by an automated traffic rate control mechanism"
  (read 2026-11-10). The ~10 requests a second in the table is
  [Cardog's figure](https://cardog.app/blog/corgi-vin-decoder) ("Rate
  limits: ~10 requests/second", read 2026-11-10), which ultravin also cites.

Every other row was measured here.

## Reproduce

Each row is one command, one trial; run it twice for the two trials, or
`scripts/bench.sh all` for every row's two trials in the order above. From
a checkout with `mise.toml`'s tools on PATH (`mise exec --`), Docker, and an
ultravin checkout built at the commit above:

```sh
git clone https://github.com/blackthorn-interstellar/ultravin /tmp/ultravin
git -C /tmp/ultravin checkout ULTRAVIN_COMMIT
(cd /tmp/ultravin && make download MONTH=2026_10 &&
  make data DUMP=downloads/vPICList_lite_2026_10.plain.zip MONTH=2026_10 &&
  cargo build -p ultravin --release --example throughput --locked &&
  uv run --frozen maturin develop --uv --release --locked)
export ULTRAVIN=/tmp/ultravin W=/tmp/bench     # W: the work directory, about 1.2 GB
gh release download v0.1.8 -R joshuasprow/nitrovin -p 'nitrovin_0.1.8_darwin_arm64.tar.gz' -O - | tar -xz -C "$W" nitrovin

scripts/bench.sh corpus "$W"                   # the corpus, sorted copy and sample; prints corpus.json

scripts/bench.sh go "$W" 1 random              # nitrovin, Go module: workers 1, 4, 12; random or sorted
scripts/bench.sh cli "$W" "$W/nitrovin"        # nitrovin, CLI
scripts/bench.sh ultravin "$W" 1 random        # ultravin: workers 1, 4, 12; random or sorted
mise exec node@22 -- scripts/bench.sh corgi "$W" v3   # corgi v3; v2 likewise

scripts/oracle.sh up                           # NHTSA Postgres
scripts/oracle.sh fetch 2026_10 /tmp/vpic && scripts/oracle.sh restore /tmp/vpic/vPICList_lite_2026_10.backup
scripts/oracle.sh freshen                      # for agree only
scripts/bench.sh postgres "$W"

curl -fsSLO https://vpic.nhtsa.dot.gov/downloads/vPICList_lite_2026_10.bak.zip && unzip vPICList_lite_2026_10.bak.zip -d /tmp/bak
scripts/bench.sh mssql-up /tmp/bak             # NHTSA MSSQL
scripts/bench.sh mssql "$W"

scripts/bench.sh agree "$W"                    # the correctness aside
scripts/bench.sh process "$W" "$W/nitrovin"    # one-VIN process
scripts/bench.sh report "$W" docs/benchmarks.json
scripts/bench.sh mssql-down; scripts/oracle.sh down
```

`scripts/bench.sh`'s header documents each command and its environment;
`cmd/nitrovin-bench`'s, the harness. corgi needs Node 22: its v2 driver,
better-sqlite3 9, has no build for the Node 24 `mise.toml` pins. corgi v2
unpacks its database into `~/.corgi-cache` on first use.

## Decisions made while building

DECISIONS
