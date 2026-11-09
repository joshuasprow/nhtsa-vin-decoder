# nitrovin

An offline VIN decoder that answers like NHTSA's vPIC, as one binary.

Status: **v0.x prereleases** (ADR 0007: v0 means the release pipeline is not
yet proven, not that the API may change). The plan is the
[wayfinder map](https://github.com/joshuasprow/nitrovin/issues?q=label%3Awayfinder%3Amap)
in this repo's issues; decisions already made are indexed there.

What it will be, as decided so far:

- A Go port of NHTSA's `spVinDecode` procedure, proven against two oracles:
  [ultravin](https://github.com/blackthorn-interstellar/ultravin) on every PR
  and NHTSA's own procedure in Postgres on every monthly data refresh.
- The vPIC data embedded in the binary (`--data <file>` to load another
  month), rebuilt from NHTSA's monthly dump by an unattended pipeline.
- Surfaces: CLI, HTTP, stdio JSON lines, MCP over stdio, a Go module, and a
  C-shared library.
- Released per OS/arch on GitHub Releases, Homebrew, Docker, winget/scoop.

MIT licensed. See `NOTICE` for data provenance and the projects this builds
on.

## Install

Every release carries the vPIC data month it embeds (`nitrovin --version`).

```sh
brew install joshuasprow/tap/nitrovin                    # macOS, Linux
scoop bucket add joshuasprow https://github.com/joshuasprow/scoop-bucket
scoop install nitrovin                                    # Windows
go install github.com/joshuasprow/nitrovin/cmd/nitrovin@latest
docker run --rm -p 8080:8080 ghcr.io/joshuasprow/nitrovin # serve on :8080
```

[GitHub Releases](https://github.com/joshuasprow/nitrovin/releases) has the
binaries and the C library (`libnitrovin` and `nitrovin.h`, no data:
`nitrovin_open` loads an artifact) for linux, darwin and windows on amd64 and
arm64, with `checksums.txt` and a cosign keyless bundle per asset; the release
notes show how to verify them. winget (`JoshuaSprow.Nitrovin`) follows.
`.goreleaser.yaml` builds all of it from a `v*` tag
(`.github/workflows/release.yml`).

## The Postgres oracle

`scripts/oracle.sh` restores a month of NHTSA's dump into Postgres 17 and
runs its `spvindecode` over a VIN list, writing an answer key (CSV, every
row the procedure emits per VIN, in a stable order). It decodes in two
modes (ADR 0006): `stock`, the dump as restored, and `freshened`, a copy
with `vpic.wmiyearvalidchars` emptied, which the refresh gates against.
The VINs whose two keys differ are the
[`stale-cache`](docs/deviations/stale-cache.md) class; `stale-cache` lists
them, and `recheck` compares every deviation witness with live vPIC, for
the report only. The database's collation is `C` (ctype `en_US.utf8`),
ultravin's oracle contract: `restore` and `decode` refuse any other, and
CI's `manifest.json` records it beside the keys. CI runs it through the
reusable workflow `.github/workflows/oracle.yml`; on a workstation with
Docker:

```sh
scripts/oracle.sh up
scripts/oracle.sh fetch "$(scripts/oracle.sh month)" /tmp/vpic   # prints backup=<path>
scripts/oracle.sh restore /tmp/vpic/vPICList_lite_<YYYY_MM>.backup
scripts/oracle.sh freshen
scripts/oracle.sh corpus testdata/oracle/sample-vins.tsv >corpus.tsv   # adds the witnesses
scripts/oracle.sh decode stock corpus.tsv stock.csv
scripts/oracle.sh decode freshened corpus.tsv freshened.csv
scripts/oracle.sh stale-cache stock.csv freshened.csv stale-cache.jsonl
scripts/oracle.sh recheck stock.csv freshened.csv witnesses.jsonl
scripts/oracle.sh down
```

## The importer

`cmd/nitrovin-import` turns a restored dump into the artifact the decoder
reads (format: `github.com/joshuasprow/nitrovin-data/pkg/artifact`, ADR
0003). It prints a psql script that extracts every table as one COPY stream,
then builds the artifact and a manifest (format, data month, size, blake3,
rows per table) from that stream. The same dump always gives the same bytes.

```sh
go run ./cmd/nitrovin-import sql |
  docker exec -i nhtsa-oracle psql -X -q -U postgres -d vpic |
  go run ./cmd/nitrovin-import build -month 2026_10   # nitrovin.artifact, nitrovin.json
go run ./cmd/nitrovin-import inspect nitrovin.artifact            # header and tables
go run ./cmd/nitrovin-import inspect nitrovin.artifact wmi wmi=5YJ
```

`NITROVIN_PSQL='docker exec -i nhtsa-oracle psql -X -q -U postgres -d vpic'
go test ./cmd/nitrovin-import/` also checks a build against the database:
row counts, NULL and empty-string counts, sampled rows, and one hash from two
extracts. `.github/workflows/import.yml` runs that on PRs touching the
importer.

`nitrovin-import vins` writes the refresh's VIN list (ADR 0008) from an
artifact and the committed corpus. The first file opens with the corpus as
given, byte for byte, which is the prefix `TestParity`'s `-update-parity`
freezes. Next comes one one-character mutation of each 17-character corpus
VIN, chosen among 24 draws so the mutations spread over the error codes. Then
come VINs synthesized from the month's pattern keys until every WMI and
VinSchema the month links appears in some decode's items. A few of those carry
a model-year hint, where only a hint gets the decode to an old schema's year.
Each candidate is decoded by the engine before it is kept: a VIN goes in only
if it reaches something nothing else reached, and never if the procedure would
raise on it. The list is split into files of at most 5,000 VINs, one
`oracle.yml` job each. A month needs more than one file, because each VIN
reaches a single WMI: 2026_10 has 13,014 WMIs and needs 18,318 VINs, so it gets
four files. The coverage summary goes to stderr, and `-report` writes the full
report, including what the corpus alone misses.

```sh
scripts/oracle.sh corpus testdata/parity/2026_10/corpus.tsv >corpus.tsv   # adds any new witness
go run ./cmd/nitrovin-import vins -o run -report coverage.json nitrovin.artifact corpus.tsv   # prints run-1.tsv …
```

## The engine

`pkg/decoder` ports `spvindecode` stage by stage, each tested against
Postgres running the dump's own SQL; `decoder.NewEngine(artifact).Project`
is the whole procedure. Most answer keys are committed under
`pkg/decoder/testdata` (regenerate with `go test ./pkg/decoder -run <Test>
-update-oracle` against a restored dump). The pattern matcher is also checked
on every distinct `pattern.keys` of a month and every VinSchema's index; the
core pass (stage 5) on a VIN corpus against `vpic.spvindecode_core` called
directly; the decode up to value resolution (stage 7: the year plan, passes,
scoring and the QC filter) against `vpic.spvindecode` cut just before it
resolves values; and the rows `Project` returns (stage 8: value resolution and
the projection) against the whole `vpic.spvindecode`, field for field. Those
two run with and without model-year hints, on the core's corpus and
ultravin's parity corpus and cover set, against the freshened database and an
artifact built from it:

```sh
NITROVIN_PSQL='docker exec -i nhtsa-oracle psql -X -q -U postgres -d vpic' \
NITROVIN_ARTIFACT=nitrovin.artifact go test ./pkg/decoder -run 'TestMatcherPostgres|TestCorePostgres|TestWrapperPostgres|TestProjectionPostgres'
```

`.github/workflows/import.yml` runs these on PRs touching the importer or
`pkg/`.

## The decode contract

`pkg/contract` is [the API contract](docs/api/README.md) in Go: the request
every surface parses (`ParseRequest`, `ParseBatch`, `ParseStdio`,
`ParseQuery`), the RFC 9457 `Problem`s it refuses with, the `Decode`, `Item`
and `Info` types, and both attribute layouts as pure functions of the items.
`pkg/nitrovin` is the Go module's entry point (`Embedded`, `Open`, `Load`,
`Decoder.Decode`); it reads the clock, the engine never does. Every decode of
the oracle's answer key for `testdata/oracle/sample-vins.tsv`, committed under
`pkg/contract/testdata/oracle`, is checked against `decode.schema.json` in both
layouts (`.github/workflows/contract.yml`). With an artifact, `go test
./pkg/contract` also checks the committed element table against it, and
decodes the answer key's VINs through `pkg/nitrovin`: the engine's items equal
the oracle's rows, every decode validates, and a second decoder gives the same
bytes:

```sh
NITROVIN_ARTIFACT=nitrovin.artifact go test ./pkg/contract -run 'TestInfo|TestEngineAnswerKey'
```

## The CLI

`cmd/nitrovin` is the `nitrovin` binary ([the CLI contract](docs/api/README.md#cli-nitrovin-decode)).
Its data is `--data <file>`, then `NITROVIN_DATA`, then the artifact embedded
in the binary: `github.com/joshuasprow/nitrovin-data/pkg/embedded`, the
month `go.mod` pins (ADR 0007). A data month moves only by bumping that pin,
which the monthly refresh does.

```sh
go build -o nitrovin ./cmd/nitrovin
./nitrovin --version                                 # nitrovin v0.0.0-…+dirty (data 2026_10) in a checkout
./nitrovin decode 1HGCM82633A004352                  # pretty on a terminal
./nitrovin decode -o text --model-year 2003 1HGCM82633A004352
printf '1HGCM82633A004352,2003\n5YJ3E1EA2JF000316\n' | ./nitrovin decode -   # JSON lines
printf '{"id":1,"vin":"1HGCM82633A004352"}\nnot json\n' | ./nitrovin stdio      # one {id, decode} or {id, error} line per line
./nitrovin info                                      # data_source "embedded"
./nitrovin --data nitrovin.artifact info             # another month: data_source is its path
```

Exit 0 when every VIN was decoded, whatever its error codes; 1 when it could
not answer (data that will not load, a refused stdin line, a write failure); 2
for a mistake on the command line, a refused argument VIN included. Each
command lives in its own file under `cmd/nitrovin` and registers itself from
that file's `init` (`register(command{…})`), so a new surface adds one file.
`go test ./cmd/nitrovin` runs the built binary: usage, refusal and
unloadable-data cases, and `info`, `--version` and a decode on the embedded
data; with `NITROVIN_ARTIFACT` also `decode -` over the answer key's VINs
against the contract layer's bytes, the data precedence and the `-o text`
transcript (`-update` rewrites it).

## The monthly refresh

`.github/workflows/refresh.yml` runs every day and on dispatch (ADR 0006,
0007, 0008). `scripts/refresh.sh detect` compares NHTSA's newest month with
the one `go.mod` pins: a newer month, or the pinned month's dump re-touched
(another size or SHA-256), is built; otherwise the run stops there. Then:

1. nitrovin-data's `scripts/build.sh` builds the artifact, and
   `nitrovin-import vins` writes the VIN list from it: the committed corpus
   with every witness, mutations, and generated VINs until every WMI and
   VinSchema is reached, in files of at most 5,000.
2. One `oracle.yml` job per file decodes it on stock and freshened Postgres;
   `TestParity -parity-run` gates each file, zero tolerance.
3. `scripts/refresh.sh report` writes the report: per-file parity, coverage
   of `pkg/decoder`, what the list reaches (`coverage.json`), the stale-cache
   class and the witnesses against live vPIC.
4. Green: `TestParity -update-parity` re-freezes `testdata/parity/<month>`
   from file 1, `scripts/refresh.sh contract-key` cuts
   `pkg/contract/testdata/oracle` from it, and `TestText -update` rewrites
   the transcript. The month is pushed to nitrovin-data's `main`, whose
   `release.yml` rebuilds it, requires the same bytes and publishes
   `v<format>.<YYYYMM>.<n>` with the stale-cache list and the frozen key.
5. Once proxy.golang.org serves that tag, a PR bumps `go.mod` with the new
   keys and the report as its body; it is squash-merged when every check on
   it passes, and the next patch tag starts `release.yml`, which the refresh
   waits for.

Red anywhere publishes nothing: the pinned month stays, and the report goes
to the issue "Refresh <month> is red", which the next green run closes.
`gh workflow run refresh.yml -f month=2026_10` rebuilds a month whatever its
dump; `-f dry-run=true` stops before anything is published. It needs the
secrets `NITROVIN_DATA_DEPLOY_KEY` and `NITROVIN_DEPLOY_KEY` (write deploy
keys of nitrovin-data and of this repo: a push or tag made with
`GITHUB_TOKEN` starts no workflow) and Actions allowed to create pull
requests.
