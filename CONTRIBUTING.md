# Contributing

Issues and pull requests are welcome. This file is for working on nitrovin
itself; using it is the [README](README.md).

## Ground rules

- **Parity is not negotiable.** A change that alters any decode must keep
  nitrovin equal to NHTSA's procedure, freshened, on every row of every VIN
  ([docs/parity.md](docs/parity.md)). A difference is a decoder bug, unless
  a PR registers a deviation class with a root cause in the dump, a
  mechanical detector and a live-vPIC witness
  ([docs/deviations/](docs/deviations/README.md)). Nothing gets a per-VIN
  excuse.
- **The API contract is `/v1`, additive only.** [docs/api/](docs/api/README.md)
  is the contract every surface follows; change it first, in the same PR.
  Renaming, retyping or removing anything is v2.
- **Hard-to-reverse choices are ADRs** ([docs/adr/](docs/adr)), and words
  are [GLOSSARY.md](GLOSSARY.md)'s. Use its terms (data month, artifact,
  oracle, item, pass) and update it when you add one.
- **Go code lives under `pkg/`, never `internal/`**, and every `pkg/*`
  package is v1 public API. Binaries are under `cmd/`.
- **ultravin is a reference, not a source.** Read it, cite it, credit it in
  [NOTICE](NOTICE) and in the file that adopts an idea; never copy its code.
- **Corpora:** commit VINs only with their source credited in the file's
  header. The refresh's generated VINs are never committed.

## Build and test

Go 1.27 or newer (`go.mod`).

```sh
go build -o nitrovin ./cmd/nitrovin
./nitrovin --version            # nitrovin <Go pseudo-version> (data 2026_10); +dirty with local changes
go test ./...                   # no data needed: tests that need an artifact or Postgres skip
```

Most tests want an artifact. Download the one `go.mod` pins from
[nitrovin-data](https://github.com/joshuasprow/nitrovin-data/releases), or
build it ([the importer](#the-importer)), and pass it by absolute path:
`go test` runs each package in its own directory.

```sh
gh release download v1.202610.1 -R joshuasprow/nitrovin-data -p nitrovin-2026_10.artifact
NITROVIN_ARTIFACT=$PWD/nitrovin-2026_10.artifact go test ./...
```

That adds, among others, `TestParity` (the engine against the frozen
answer key, in a fraction of a second), the contract's answer-key tests,
and the CLI, HTTP, stdio and MCP tests against the built binary. The tests
that compare against Postgres also need `NITROVIN_PSQL`
([the engine](#the-engine)).

CI runs what a change can affect. Every test workflow cancels its run on a
branch when a newer push supersedes it (`release.yml` and the refresh never
cancel), and none runs for Markdown, `GLOSSARY.md`, `NOTICE` or `docs/`
alone, except the API documents the contract tests read and the deviation
witnesses the oracle reads.

- `contract.yml` (schemas and the no-data tests): `pkg/contract`,
  `pkg/nitrovin`, `pkg/decoder`, `cmd/nitrovin`, `docs/api`'s three
  documents, `go.mod`, `go.sum`.
- `import.yml`, **Tests**, on `pkg/`, `cmd/`, `go.mod`, `go.sum`,
  `testdata/parity/`, `scripts/oracle.sh`, in three jobs. *Changed paths*
  decides whether Postgres is needed. *Artifact* always runs, without
  Postgres, on the artifact `go.mod` pins (read from the module cache): the
  contract, CLI, HTTP, stdio, MCP and form tests, `TestParity` offline and
  `TestVinsCoversTheMonth`, each required to pass, not skip, in about two
  minutes. *Postgres* restores the dump and runs the importer's
  `TestPostgres` and every `pkg/decoder` Postgres test, only when the
  branch changes `pkg/decoder`, `pkg/contract`, `cmd/nitrovin-import`,
  `scripts/oracle.sh` or `import.yml`; otherwise it is skipped, which counts
  as passed. `go.mod` and `testdata/parity` alone (the refresh's bump PR)
  take the short path. `gh workflow run import.yml --ref <branch> -f
  full=true` runs Postgres anyway.
- `libnitrovin.yml` (the C library on six native runners, on the pinned
  artifact): `cmd/libnitrovin`, the packages it links (`pkg/stdio`,
  `pkg/nitrovin`, `pkg/contract`, `pkg/decoder`), `go.mod`,
  `scripts/libnitrovin.sh`.
- `oracle-smoke.yml` (the Postgres oracle end to end): `oracle.yml`,
  `scripts/oracle.sh`, `testdata/oracle/`, `docs/deviations/`.
- `release.yml`'s snapshot when release files change.

A workflow's own file triggers it. `import.yml` and `libnitrovin.yml` also
run on pushes to `main`, which keep the Go and zig caches warm: an entry a
pull request saves is visible to that pull request alone. The Go cache is
named by the Go version, `go.sum` and the Go sources, and restores the
nearest entry on a miss; zig's global cache by the zig version and
`scripts/libnitrovin.sh`.

## The Postgres oracle

`scripts/oracle.sh` restores a month of NHTSA's dump into Postgres 17 and
runs its `spvindecode` over a VIN list, writing an answer key (CSV, every
row the procedure emits per VIN, in a stable order). It decodes in two
modes (ADR 0006): `stock`, the dump as restored, and `freshened`, a copy
with `vpic.wmiyearvalidchars` emptied, which the gates compare against.
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

The container is `nhtsa-oracle`; `ORACLE_CONTAINER` names another. NHTSA's
downloads page lists only the newest month. An older month's dump is an
asset of its nitrovin-data release (`vPICList_lite_<YYYY_MM>.custom.zip`);
unzip it and `restore` the `.backup` inside.

## The importer

`cmd/nitrovin-import` turns a restored dump into the artifact the decoder
reads (format: `github.com/joshuasprow/nitrovin-data/pkg/artifact`, ADR
0003). It prints a psql script that extracts every table as one COPY stream,
then builds the artifact and a manifest (format, data month, size, blake3,
rows per table) from that stream. The same dump always gives the same bytes:
2026_10 builds to the `nitrovin-2026_10.artifact` of nitrovin-data
`v1.202610.1`, byte for byte.

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
extracts.

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
`oracle.yml` job each: 2026_10 has 13,014 WMIs and needs 18,318 VINs, so it
gets four files. The coverage summary goes to stderr, and `-report` writes
the full report, including what the corpus alone misses.

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
NITROVIN_ARTIFACT=$PWD/nitrovin.artifact go test ./pkg/decoder -run 'TestMatcherPostgres|TestCorePostgres|TestWrapperPostgres|TestProjectionPostgres'
```

`TestParity` checks the engine offline against the committed frozen key in
`testdata/parity/<month>/`; `-parity-run <oracle run dir>` checks a whole
oracle run instead (the refresh gate), `-parity-report <dir>` writes the
per-(VIN, element) report, and `-update-parity <oracle run dir>` re-freezes
the key.

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
NITROVIN_ARTIFACT=$PWD/nitrovin.artifact go test ./pkg/contract -run 'TestInfo|TestEngineAnswerKey'
```

## The surfaces

`cmd/nitrovin` is the binary. Each command lives in its own file and
registers itself from that file's `init` (`register(command{…})`), so a new
surface adds one file and leaves `main.go` alone. Its data is `--data
<file>`, then `NITROVIN_DATA`, then the artifact embedded from
`github.com/joshuasprow/nitrovin-data/pkg/embedded`, the month `go.mod`
pins (ADR 0007); a data month moves only by bumping that pin, which the
monthly refresh does.

`go test ./cmd/nitrovin` runs the built binary: usage, refusal and
unloadable-data cases, and `info`, `--version` and a decode on the embedded
data; `TestForm` drives the browser form at `/` on the embedded data. With
`NITROVIN_ARTIFACT` it adds `decode -` over the answer key's VINs
against the contract layer's bytes, the data precedence, the `-o text`
transcript (`-update` rewrites it), every HTTP operation and status in
`openapi.json` (`TestServeContract`), a scripted stdio session, and MCP
sessions on two protocol revisions (`TestMCPSession`). `cmd/nitrovin/openapi.json`
and `cmd/nitrovin/mcp-tools.json` are generated copies of the files in
`docs/api/`; tests fail when they drift (`-update-openapi`,
`go generate ./cmd/nitrovin`).

`cmd/libnitrovin` is the C library (`-buildmode=c-shared`, no data).
`scripts/libnitrovin.sh <dir> [<goos>/<goarch>…]` builds it for all six
targets, or the ones named, from any host with `zig` 0.17.1 as the C
cross-compiler (`mise exec zig@0.17.1 -- scripts/libnitrovin.sh build/libnitrovin`).

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

## Releases

A `v*` tag runs `.github/workflows/release.yml`, which runs goreleaser
(`.goreleaser.yaml`): the six binaries, the six C library archives, the
six Linux packages (`.deb`, `.rpm`, `.apk` for amd64 and arm64),
`server.json`, `checksums.txt`, a cosign keyless bundle per asset, the
`ghcr.io/joshuasprow/nitrovin` image signed by digest, the Homebrew cask in
`joshuasprow/homebrew-tap` and the Scoop manifest in
`joshuasprow/scoop-bucket`. It verifies every signature, checksum and
version stamp, then publishes `server.json` to the MCP Registry as
`io.github.joshuasprow/nitrovin` (GitHub OIDC, no secret) in a step of its
own. A pull request that touches the release builds a snapshot and installs
its packages in Debian, Fedora and Alpine. The monthly refresh tags patch
releases itself; a code change ships with the next tag. Until v1.0.0 every
tag is a prerelease (ADR 0007), and the workflow refuses a tag whose
prerelease setting disagrees.
