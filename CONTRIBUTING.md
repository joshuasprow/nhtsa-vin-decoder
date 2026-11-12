# Contributing

Issues and pull requests are welcome. This file is for working on nitrovin
itself; using it is the [README](README.md).

## Ground rules

- **Compatibility and interoperability are the goals.** Compatibility is
  answering exactly as NHTSA's vPIC does: its procedure, its output
  shapes. Interoperability is working with other systems: the surfaces,
  vocabularies translating through vPIC's ids, standard formats. When a
  change trades speed or convenience against either, they win; speed has
  a budget to meet, not a race to win.
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
  package is v1 public API but `pkg/ui`, whose Go API, like its `/ui`
  routes, is free to change. Binaries are under `cmd/`.
- **ultravin is a reference, not a source.** Read it, cite it, credit it in
  [NOTICE](NOTICE) and in the file that adopts an idea; never copy its code.
- **Corpora:** commit VINs only with their source credited in the file's
  header. The refresh's generated VINs are never committed.

## Tools

Every tool the scripts and workflows run is pinned once, in
[`mise.toml`](mise.toml), with each platform's download URL and sha256 in
`mise.lock`. With [mise](https://mise.jdx.dev), whose shell hook activates
the pins inside the checkout (direnv is not needed):

```sh
mise install --locked           # refuses a download whose checksum is not mise.lock's
(cd deploy/cloudflare/worker && npm ci)   # wrangler, pinned by its package.json
scripts/tools.sh check          # every tool on PATH reports its pin
```

Without mise, install by hand the versions `mise.toml`'s `[tools]` names,
for what you work on:

| Tool | For |
| --- | --- |
| `go` | everything; `go.mod`'s `go 1.27` is only the floor for `go install` |
| `jq` | the scripts and workflows: jq itself, not a reimplementation such as jaq |
| `zig` | the C library (`scripts/libnitrovin.sh`) and release snapshots |
| `goreleaser` | release snapshots (`goreleaser release --snapshot --clean --skip=sign`) |
| `cosign` | signing and verifying releases |
| `yq` | `release.yml`'s prerelease check |
| `mcp-publisher` (`github:modelcontextprotocol/registry`) | validating and publishing `server.json` |
| `terraform`, `node` | `deploy/cloudflare` (wrangler is the Worker's npm dependency); `node` also runs the MCP Inspector |
| `uv` | Python tooling, such as `uvx openapi-spec-validator docs/api/openapi.yaml` |
| Docker | the oracle (`postgres:17`, by digest), the release image, `wrangler dev`; any current version |

`mise.toml`'s header says what is left unpinned and why, and how to bump a
pin. A weekly check lists newer releases in one issue and bumps nothing.

## Build and test

Go at `mise.toml`'s pin ([Tools](#tools)).

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
([the engine](#the-engine)), and the stock connector's
`NITROVIN_STOCK_PG_DSN` ([Vocabularies](#vocabularies)).

CI runs what a change can affect. Every test workflow cancels its run on a
branch when a newer push supersedes it (`release.yml` and the refresh never
cancel), and none runs for Markdown, `GLOSSARY.md`, `NOTICE` or `docs/`
alone, except the API documents the contract tests read, the deviation
witnesses the oracle reads, and `README.md`, whose install, MCP and verify
text `TestFormREADME` holds to the form's.

- `contract.yml` (schemas, the no-data tests and the `js/wasm` build):
  `pkg/contract`, `pkg/nitrovin`, `pkg/decoder`, `pkg/vocab`, `pkg/ui`,
  `cmd/nitrovin`, `cmd/nitrovin-epa`, `testdata/vocab/`, `docs/api`'s four
  schema documents, `README.md`, `go.mod`, `go.sum`. The tests that run
  the vocabulary fixture or the EPA sample against the embedded month must
  pass, not skip: on the refresh's bump PR they fail it, naming the month,
  if the new month breaks them ([the fixture](#vocabularies)).
- `import.yml`, **Tests**, on `pkg/`, `cmd/`, `go.mod`, `go.sum`,
  `testdata/parity/`, `testdata/vocab/`, `scripts/oracle.sh`, in four
  jobs. *Changed paths* decides whether Postgres is needed. *Artifact*
  always runs, without Postgres, on the artifact `go.mod` pins (read from
  the module cache): the
  contract, CLI, HTTP, stdio, MCP and form tests, `TestParity` offline and
  `TestVinsCoversTheMonth`, each required to pass, not skip, in about two
  minutes. *Postgres* restores the dump and runs the importer's
  `TestPostgres` and every `pkg/decoder` Postgres test, only when the
  branch changes `pkg/decoder`, `pkg/contract`, `cmd/nitrovin-import`,
  `scripts/oracle.sh` or `import.yml`; otherwise it is skipped, which counts
  as passed. `go.mod` and `testdata/parity` alone (the refresh's bump PR)
  take the short path. `gh workflow run import.yml --ref <branch> -f
  full=true` runs Postgres anyway. *Stock connector* starts an empty
  Postgres 17 (the oracle's digest, no dump) and runs every
  `pkg/stock/pg` Postgres test, each required to pass, in about a minute,
  for every path the workflow watches.
- `libnitrovin.yml` (the C library on six native runners, on the pinned
  artifact): `cmd/libnitrovin`, the packages it links (`pkg/stdio`,
  `pkg/nitrovin`, `pkg/contract`, `pkg/decoder`, `pkg/vocab`), `go.mod`,
  `scripts/libnitrovin.sh`, the `testdata/vocab` fixture it loads.
- `oracle-smoke.yml` (the Postgres oracle end to end): `oracle.yml`,
  `scripts/oracle.sh`, `testdata/oracle/`, `docs/deviations/`.
- `release.yml`'s snapshot and benchmarks when release files or
  `bench/vocab` change, and on dispatch (`gh workflow run release.yml --ref
  <branch>`).
- `deploy.yml` (nitrovin.app): `deploy/cloudflare/`, the workflow itself. On
  a pull request, Terraform `validate` and the Worker under `wrangler dev`;
  after each green release, the deploy and its smoke test; weekly, the drift
  check ([deploy/cloudflare/README.md](deploy/cloudflare/README.md)).
- `tools.yml` (the pins): `mise.toml`, `mise.lock`, `.github/actions/mise`,
  any workflow, `scripts/tools.sh`, `scripts/oracle.sh`, the Worker's
  `package.json`. On Linux x64 and macOS arm64, every tool installed afresh
  with `mise install --locked`, `scripts/tools.sh check`, and
  `scripts/tools.sh refuse` (a copy of `mise.lock` with one checksum changed
  must fail to install). Every Monday, the pins with a newer release, in the
  one issue "Tools: newer releases upstream".

A workflow's own file triggers it, and every job that runs a tool installs
it with `.github/actions/mise` (mise, pinned there, installing from
`mise.lock` with `--locked`), so `mise.toml`, `mise.lock` and that action
also trigger every workflow above that installs tools (not the Postgres
job, which follows its own paths). `import.yml` and `libnitrovin.yml` also
run on pushes to `main`, which keep the Go and zig caches warm: an entry a
pull request saves is visible to that pull request alone. The Go cache is
named by the Go version, `go.sum` and the Go sources, and restores the
nearest entry on a miss; zig's global cache by the zig version and
`scripts/libnitrovin.sh`; mise's installs by `mise.toml`, `mise.lock` and
the tools a job asks for.

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

## Vocabularies

`pkg/vocab` reads, validates and indexes the files `--vocab` loads
([ADR 0011](docs/adr/0011-vocabularies-are-json-lines-loaded-once.md),
`docs/api/vocabulary.schema.json`): `Read` checks one file, `Open` files and
directories and `Link` them against each other, every fault a `Finding`
naming the file and line. `Drift` measures files against a data month
through the `Month` interface, which `pkg/nitrovin` implements on the
artifact (`NewTranslator`). `pkg/vocab` imports no artifact, decoder or
nitrovin-data, so it also builds for a browser.

Translation's core is decoder-free
([ADR 0013](docs/adr/0013-browser-vocabularies-in-a-service-worker.md)):
a `vocab.Set` over a `Month` routes a translation, and `vocab.NewDecoded`
reads a decode's items, when there is a decode, against the same `Month`.
`contract.Translator` turns its answers into the contract's, given a
decoder: `pkg/nitrovin`'s `Translator` gives it the engine, the browser's
Service Worker `/v1/decode`. So every surface answers through one
translator. `GOOS=js GOARCH=wasm go build ./pkg/vocab ./pkg/contract
./pkg/ui ./cmd/nitrovin-sw` must keep compiling (`contract.yml` builds it).

The worker's `Month` is the **spine bundle**: `vocab.WriteBundle` writes a
`vocab.BundleSource` (a `Month` that also lists its model ids, elements and
element values) as one JSON document, columns per table, and
`vocab.ReadBundle` reads it back as a `*vocab.Bundle`, itself a
`BundleSource`. `Decoder.WriteSpineBundle` writes the artifact's month,
every model included, those under no make too; `serve` publishes it at
`/ui/r/<release>/spine.json`. Its JSON is read only by the release that
wrote it, so it may change in any release (its `format` guards a mix-up).
`TestSpineBundle` holds the embedded month's to 1 MB brotli and checks it
answers every `Month` question as the artifact does, over every make,
model and element and every value against every element;
`TestSpineBundleTranslates` checks drift, translations and lists through
it on the fixture, and `TestBundle` the cases 2026_10 lacks (a model with
no `make_model` row, one under a make the lookup lacks).

`testdata/vocab` is the synthetic fixture every vocabulary test shares: a
vehicle vocabulary (`yard`: Dodge's Ram 1500
split by year across vPIC models 1938 and 13620, with Series and Drive
Type qualifiers, extension mappings, make mappings and an unmapped entry),
an extension set (`classics`), a declared spine (`parts`) and a part
vocabulary on it (`listing`, with Vehicle Type qualifiers and an unmapped
entry). It loads with no drift; `testdata/vocab/drift/` has one drift of
each kind. `testdata/vocab/fitment/` is the synthetic catalog
(`parts-catalog`) with its declared spine (`part-types`) and vehicle
vocabulary (`catalog-models`), using every line kind: fitments, one number
on a left and a right fitment, a qualifier, a redirect and a retired
number, and a part vocabulary on its spine (`part-codes`); it loads clean
beside `testdata/vocab`, or alone. Its fit examples (docs/api/README.md's
Fit section) are answered on every surface: `go test ./pkg/nitrovin`
(`TestFit`, statuses, the side filter, refusals, paging, list of a
catalog), `TestServeContract`, `TestStdioOps`, `TestMCPVocabSession`,
`cmd/nitrovin`'s `TestFit`, and the C library's `vocab-requests.jsonl`. `go test
./pkg/vocab` refuses one file per fault class, catalogs' included,
measures every catalog drift class, reads the catalog through its reader,
and validates the fixtures against the schema; `go test ./pkg/nitrovin`
loads the fixture, its drift variant and the catalog against the embedded
data.

`testdata/vocab/stock/` is the synthetic stock on that catalog
([ADR 0019](docs/adr/0019-what-stock-says.md)): two shelves, `shelf-stock`
(numbered under release 7.01 against the catalog's 7.02, so every answer
marks it `catalog_mismatch`) and `shelf-annex` (no release, at another
location), with rows by number, by a retired number, and with no number by
a part vocabulary's entry and by a spine entry; and a yard (`yard-stock`)
with a precomputed decode, a VIN nitrovin decodes, and the catalog's own
vehicles. It loads clean beside the catalog; `testdata/vocab/stock/drift/`
adds a third shelf with an orphan. A fit reads stock through
`stock.Source` (`pkg/stock`), the interface a connector (ADR 0020)
implements too: three lookups, one per list kind, each taking a context
and returning the release and `as_of` it read; `stock.NewFile` is the
file's. Its examples (docs/api/README.md's Stock section) are answered on
every surface: `go test ./pkg/nitrovin` (`TestFitStock`, location,
paging, unavailable sources, `Info`), `TestServeContract`,
`TestServeStockUnavailable`, `TestStdioOps`, `TestMCPVocabSession`,
`cmd/nitrovin`'s `TestFit`, and the C library's `vocab-requests.jsonl`;
`go test ./pkg/stock` checks the yard routes, and `go test ./pkg/vocab`
refuses each stock fault and measures orphans and personal-looking fields.
On a compiled catalog, `TestCompiledAnswersAsJSONLines` asks `RetiredTo`
of every key, `TestFitCompiled` fits the synthetic stock against both
forms alike, and `TestVocabCompile` checks the stock's orphan and fits
with it through the CLI. `TestReadBudget` (`pkg/nitrovin`) counts each fit
request's reads of a compiled catalog through its `io.ReaderAt` and fails
past [ADR 0023](docs/adr/0023-fitment-in-the-browser.md)'s budget;
`TestCatalogFaults` refuses each fault through the streaming compiler as
through `Open`, `TestCompileAcrossBatches` and `TestCompileAcrossRuns`
compile in tiny batches and sort runs to the same bytes, and
`FuzzOpenCompiled` feeds the reader damaged files.

`testdata/vocab/stock/live/` reads the same three sources through
connectors ([ADR 0020](docs/adr/0020-request-time-stock-the-postgres-connector.md)):
a header each, and `views.sql`, a synthetic parts system's and yard
system's tables with the views the connectors read. `go test
./pkg/stock/pg` checks the connector with no database (an unset or
malformed DSN, a refused connection and its 5 s retry, each error's fixed
reason) and that no package the C library or the browser builds links the
driver. Its `*Postgres` tests need a database they own, by URL in
`NITROVIN_STOCK_PG_DSN`: `TestParityPostgres` fills the views with the
synthetic rows and holds every fit to the same rows loaded as files; the
others check missing and mistyped columns, credentials, as_of, the
`READ ONLY` transaction, a cancelled and a timed-out query, a full pool,
a relation that changes, a database lost after its check (and `Close`),
a yard with no catalog, and a vpic catalog:

```sh
docker run -d --name nitrovin-stock -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=stock -p 55432:5432 \
  postgres:17@sha256:ff87138949ad9a46f32ff23bd6852b52ce024a13dffe9b4087be98292cdd3b4f
NITROVIN_STOCK_PG_DSN='postgres://postgres:postgres@127.0.0.1:55432/stock?sslmode=disable' go test -v -run Postgres ./pkg/stock/pg
```

`cmd/nitrovin`'s `TestConnectorSurfaces` loads the connectors on the CLI,
stdio, `serve` and MCP against a port nothing listens on.

Its headers say 2026_10, but the ids, values and VINs it relies on are
long-lived ones (2026_06 loads it the same), so its tests never skip on
another month: they assert against the embedded month, and one that fails
names it. A month that retires one of them fails the refresh's bump PR in
`contract.yml`; fix the fixture, never skip. A test whose expectation
truly belongs to one month (the answer keys, `pkg/contract`'s element
table) skips on any other, and is not a vocabulary test.

### The EPA vocabulary

`cmd/nitrovin-epa` is a vocabulary builder: it writes `epa-vehicles`
(fields `make`, `model`) from EPA's
[vehicles.csv](https://www.fueleconomy.gov/feg/epadata/vehicles.csv.zip)
and a data month, by the rules the EPA research chose
([#144](https://github.com/joshuasprow/nitrovin/issues/144)): exact,
stripped model, longest prefix (guarded by baseModel), baseModel,
punctuation-insensitive, make-name prefix, slash alternatives, each named in
its mapping's note. Every mapping holds for the years of the entry's EPA
rows. Drive Type and Series qualifiers come from EPA's `drive` column and the
tonnage in the name, narrowed to the model's own model values (ADR 0015):
a drive keeps the ids it can decode to ("Front-Wheel Drive": FWD or 4x2)
that the model decodes, and Series the spellings the model itself decodes.
`cmd/nitrovin-epa/curated.jsonl`, built in, is the curated overrides: a
vehicle vocabulary whose entries replace the derived ones (`origin:
curated`), taking the EPA years and derived qualifiers where they state
none. The version is the newest `createdOn`/`modifiedOn` date in the file
and the data month, so the same inputs give the same bytes.

```sh
curl -sSLO https://www.fueleconomy.gov/feg/epadata/vehicles.csv.zip
go run ./cmd/nitrovin-epa -o epa-vehicles.jsonl vehicles.csv.zip   # coverage on stderr
go run ./cmd/nitrovin vocab check epa-vehicles.jsonl
```

`go test ./cmd/nitrovin-epa` builds a 80-row sample of vehicles.csv
(`testdata/`) twice, from the CSV and from a zip, against its golden file
(`-update` rewrites it; on another month than the one its header names, the
build must differ in that name alone), loads it against the embedded data
with no drift but the model-years findings it pins, checks every qualifier
value is the mapped model's own, and checks the built-in overrides for
drift. Every data
month publishes the vocabulary: the monthly refresh builds it against the
new month and its data release carries `epa-vehicles.jsonl` (below).

## The surfaces

`cmd/nitrovin` is the binary. Each command lives in its own file and
registers itself from that file's `init` (`register(command{…})`), so a new
surface adds one file and leaves `main.go` alone. Its data is `--data
<file>`, then `NITROVIN_DATA`, then the artifact embedded from
`github.com/joshuasprow/nitrovin-data/pkg/embedded`, the month `go.mod`
pins (ADR 0007); a data month moves only by bumping that pin, which the
monthly refresh does. Beside the data, every command loads `--vocab`, or
else `NITROVIN_VOCAB` (`env.load`).

`go test ./cmd/nitrovin` runs the built binary: usage, refusal and
unloadable-data cases, and `info`, `--version` and a decode on the embedded
data; `TestStdioOps` a stdio session mixing decode, translate, list and fit
lines with `testdata/vocab`; `TestForm` drives the page's VIN form (`/`)
on the embedded data, over HTTP and then in a browser (below),
and `TestServeCORS` `serve --cors` on it. With
`NITROVIN_ARTIFACT` it adds `decode -` over the answer key's VINs
against the contract layer's bytes, the data precedence, the `-o text`
transcript (`-update` rewrites it), every HTTP operation and status in
`openapi.json` (`TestServeContract`, `/mcp` included), a scripted stdio
session, and MCP sessions on two protocol revisions over stdio and over
serve's Streamable HTTP `/mcp` (`TestMCPSession`). `cmd/nitrovin/openapi.json`
and `cmd/nitrovin/mcp-tools.json` are generated copies of the files in
`docs/api/`; tests fail when they drift (`-update-openapi`,
`go generate ./cmd/nitrovin`).

`pkg/ui` is the pages for browsers, the one page at `/` and everything
under `/ui`
([ADR 0013](docs/adr/0013-browser-vocabularies-in-a-service-worker.md)),
which `serve` hands those paths to (and `/sw.js`) and the
browser's Service Worker runs too, so it reaches its data through
interfaces (`ui.Pages`, the server's `ui.Backend`) and builds for
`js/wasm`. Each route answers a fragment of its templates (`ui.html`,
`page.html`, `decode.html`, `vocab.html`, `fit.html`) for a request with `HX-Request`, its page's title first, and
the same fragment in the one layout for any other, with
`Vary: HX-Request` and the CSP (`ui.CSP`, `script-src 'self'`). Every error
has its real status: the one error page, or the error fragment that the
layout's htmx configuration swaps into its error slot; a page that refuses
with its own content, as the page's controls do beside a field, sends htmx
to its target instead (`HX-Retarget`). A fragment fills the layout's
main (`hx-history-elt`), and htmx keeps no snapshot of it
(`hx-history="false"`): back and forward ask the server for each page
again, so a page put back never carries what a script or an extension
left on it, which CSP would refuse. The header shows the running
release's version beside the brand, linked to its GitHub release when it
is a tagged one, and the navigation (`ui.html`'s `nav`), one template on
the server and in the worker: Translate (`/`), Fit (`/ui/fit`), the
Vocabularies button and API docs (`docs/api/README.md` at the release's
tag, else on `main`), the page shown marked `aria-current`. Its links
are page loads, never htmx's, so the mark is always the page's. The
Vocabularies button's dialog sits beside the main, and both are the
whole page's; every page draws them but an error page, whose dialog
would read the drift an unknown path must never cost
(`TestPagesReadLoadInfo`). Every response names its
release in the `Nitrovin-Release` header (`ui.ReleaseHeader`). The layout
loads its stylesheet,
htmx, `copy.js` (copy buttons) and `map.js` (the translation map's
highlighting) from `/ui/r/<release>/` (`ui.Release`:
decoder version, data month and artifact hash, the Worker's cache key),
immutable for a year. htmx is vendored, one file
(`pkg/ui/static/htmx.min.js`, npm's `htmx.org` at `htmxVersion`), held to
its sha256 by `TestHTMXPin`; the weekly tools issue reports a newer release
([Tools](#tools)). Beside them `/ui/r/<release>/` publishes what the
Service Worker needs of the server: the spine bundle (`ui.SpinePath`,
asked of the `Backend` once) and each loaded file, byte for byte
(`ui.FilePath`: `vocab/<blake3>/<name>.jsonl`, since the release does not
name the files a server loads). `serve` reads a file again from disk when
asked and refuses bytes whose blake3 is not the one loaded.
`go test ./pkg/ui` walks a stub route as page and fragment, the errors, the
assets, the published files and the page's routes; `TestUI` and
`TestServePublished` in
`cmd/nitrovin` the same through `serve`.

The page (`page.go`, `page.html`) is everything a visitor does. Its
controls (Translate from) are a box with a tab for each thing to
translate from: VIN, which is decoded and translated; Year, make and
model, an entry of the vehicle vocabularies; one for each declared
spine, named by what its vocabularies name (Part (parts spine)), an
entry of its vocabularies, since a translation crosses exactly one spine
(ADR 0010); and vPIC id, a make or model id. A tab is a link (`/?tab=…`)
and holds one form, which asks the page itself: its query is
`GET /v1/translate`'s, with a VIN's decode options (`shape`, `full`)
beside it, and a query's `from` says which tab it is of. A from that no
form asks for, which a map's card links to, is shown in its tab's form
too: an extension in a field of the vPIC id's form, a declared spine's
entry read-only in its tab's. Below the controls the answer is a VIN's
decode (`decode.go`, `decode.html`) and, for every translation, its map,
with the JSON of both behind a button, in a dialog. A refusal is the page
still, said in the controls beside the field holding the value its
pointer names (`controls.field`), on every tab, else above the form.
Both dialogs, the JSON's and the vocabularies', are popovers, so they
open without a script; the vocabularies' is on each entry list too. Old
links (`/?vin=…`, `/ui/decode`, `/ui/translate`, `/vocab`,
`/ui/vocabularies`) move to the page with a `301`.

The fit page (`fit.go`, `fit.html`: `/ui/fit`,
[ADR 0023](docs/adr/0023-fitment-in-the-browser.md)) answers a fit from
the catalog loaded where it is answered. Its query is `GET /v1/fit`'s, so
a page link and an API call say the same thing: a form's empty fields,
which `/v1/fit` refuses, move (`303`) to the same query without them. Its
JSON dialog holds the fit and, on the server, links the same request on
`/v1`, a document of its own that htmx never boosts. Its controls are
the translate page's inputs as tabs (VIN; Year, make and model, the
vehicle vocabularies' entries; vPIC id) and a fitment's key, each with
its year, side and the vocabularies to name vehicles in. A vehicle's
answer is its part types with their counts, each a link to its
candidates; a candidate shows each application's `via` and
`unconfirmed`, and its number links to the fitment's vehicles; related
vehicles are a section of their own, which each vehicle's answer links.
Every status is a sentence (`statusText`), `no-catalog` included; every
list pages with the API's `after` and `next`. The links the page draws to
candidates, a fitment's vehicles and related vehicles ask for every kind
of stock loaded that each takes (`stock`), so stock lists show whenever
stock is loaded; a list with an unavailable source says its stock is
unknown, never empty. The examples on the empty page ask no stock source. The server and the Service Worker answer it through the
same `Handler`, over `ui.Pages`' `Fit`. `go test ./pkg/ui` walks every
kind of answer, the statuses, paging, refusals and the header on the
synthetic catalog; `TestFitPageServer` holds every documented fit example
to `/v1/fit`'s answer, `TestFitPageUnavailableStock` serves the
connectors with no DSN, and `TestFitPageBrowser` runs the page in a browser
with and without JavaScript under axe.

The map (`map.go`) is from, the spine and the targets as three columns of
cards, each mapping a line, each card a link to the page from it, the
From card to the answer's own URL. Its key names the lines it draws: a
mapping, one matched by rule (dashed), a model under its make, a VIN to
the model it decodes to; and a marked card, not every qualifier or the
model year checked. What a card's lines draw is in its text too (its
make, what it was reached through and how): visually hidden and read
with its name on a wide window, shown when a narrow one (a phone) stacks
the columns and hides the lines and their key. A name shows on two
lines, and whole while its card is under the pointer or focused,
spilling over the cards below without moving them. The server draws the
lines, an SVG behind the cards stretched across the columns, from the
cards' places, which it knows because every card is one height:
`map.go`'s geometry constants and `ui.css`'s map rules say the same
sizes, and `translationMap` (`cmd/nitrovin/map_test.go`, run by
`TestVocabTranslate`) holds every line to its cards as the browser lays
them out, and checks a narrow window stacks the columns. So the map
needs no script; `map.js` only lights the routes through the card under
the pointer or focus, which the server works out too (each card's
`data-lit`).

The page and a file's entries (`vocab.go`, `vocab.html`:
`/ui/vocabularies/{name}`) answer the same on
the server and in the browser's Service Worker
([ADR 0013](docs/adr/0013-browser-vocabularies-in-a-service-worker.md)),
which answers only a request naming a file the browser holds (what it is
from, or a target `to=`), and the entry lists of the files it holds, and
leaves everything else to the server: a bare `/?from.vin=…` is the
server's in every browser.
In the browser they answer from a `ui.Library`: the release's spine
bundle, the server's files linked from their published URLs, and the
browser's own files in IndexedDB, each with its parsed index
(`vocab.WriteIndex`, read back by `vocab.ReadIndex` instead of parsing the
file on every cold start), keyed by blake3 and release. Its rules (batch
loads that link or change nothing, replace by name, refused removes with
dependants, stored files that fault left out and listed, the decode's data
month checked, each vocabulary loaded selected as a target and each file
removed no longer) are plain Go, tested with an in-memory `ui.Disk`
(`library_test.go`). The worker has no decoder: for a VIN it answers, the
Library asks the server's `GET /v1/decode/{vin}` once, with the decode's
own query alone (no file's name goes with it), lays it out as the server
does and translates the same decode; no route under `/ui` answers JSON.
`cmd/nitrovin-sw` is the module: `ui.NewInBrowser`
over a `Library`, given IndexedDB and `fetch` by `pkg/ui/static/worker.js`,
the worker script that `/sw.js` (its release and the server's files, never
cached without asking, the only response allowed `'wasm-unsafe-eval'`)
imports. `scripts/sw.sh [<dir>]` builds the module reproducibly
(`-trimpath -buildvcs=false`) into `cmd/nitrovin/sw`, which git ignores
and `serve` embeds; goreleaser's before hook runs it, so release builds
serve the worker and others serve the page without it. Beside it,
`scripts/epa.sh [<dir>]` fetches `epa-vehicles.jsonl` from the
nitrovin-data release `go.mod` pins, checked against that release's
`checksums.txt`; goreleaser's before hook runs it too, and `serve` offers
the file in the dialog as a one-click load when it is of the server's data
month (`ui.Offer`). The worker answers only while the server runs its
release: it reads `Nitrovin-Release` on the server's answers, asks
`HEAD /sw.js` for it before answering anything itself, and once the
server runs another release it is outdated and answers nothing, every
request going to the server unchanged, until a newer worker replaces it
(ADR 0013's amendment). `vocab.js` is the dialog's script: it registers the
worker when the dialog first opens or a file is dropped on the page, and
at page load in a browser that has it registered already (so a visitor who
never opens the dialog never downloads the module), says its state (ready,
paused after a hard reload, outdated when the worker in control is of
another release than the page's `data-release` or the server's, an update
waiting for the old release's tabs to close, with Update now, which hands
the page to the new worker and reloads), swaps the worker's panel
(`/ui/files`: the merged catalog, each file marked by where it lives, with
its drift) over the
server's catalog, and hands picked or dropped files, EPA's on a click, and each
target ticked or cleared to it by `postMessage`, never in a request,
giving up on a load after a minute without progress, and saying in words
why a load cannot be taken. After a load or a
removal it asks for the page's main again: its forms and its answer are
over the files loaded.

Each tab's form comes with
examples, each a link to the page with the form filled in: VINs and the
vPIC model one decodes to, which need no file, and on an entry tab one from
each of its vocabularies, its first mapped entry in a year its mapping holds
for, built from the file, so a private file gets one without its name in
the repository. An example goes to the targets
ticked on its spine, else to another vocabulary on it. Targets are
always explicit: the ticked ones travel in the URL (`to=`), and a link
naming none means the same in every browser. With JavaScript the forms
tick the visitor's selection, which
IndexedDB keeps beside the files, by spine, in a database of its own
(`nitrovin-state`, so an older release still opens `nitrovin`): a file
loaded joins it, a file removed leaves it, `vocab.js` changes it as the
visitor ticks, and each translation asked for, a link's own `to=`
included, sets it on its spine to its targets (the worker's pages for the
ones they answer, `worker.js` for those it sends to the server,
by the spines `/sw.js` gives the server's files).

`TestForm` then runs the VIN's form in headless Chrome or Chromium
(`browser_test.go`, over the DevTools protocol with chromedp), twice, each
in a browser with a fresh profile: an installed Service Worker survives
JavaScript being turned off, so a profile that had JavaScript would answer
the JavaScript-off run from the worker. Without JavaScript every step is a
page load; with it, every step must reach the server with `HX-Request` and
never reload the page, and back and forward ask the server for each state
again, never a snapshot, with nothing refused by CSP. axe-core
audits every state of both runs and fails on any violation; it is vendored
as `cmd/nitrovin/testdata/axe.min.js` (npm's `axe-core` at `axeVersion`,
held to its sha256 by `TestAxePin`, reported by the weekly tools issue
too). The browser is `$NITROVIN_CHROME`, else Chrome or Chromium on `PATH`
or in `/Applications`; without one the browser runs skip, except in CI
(`CI` set), where they fail.

`TestWorker` (`worker_test.go`) builds the module twice with
`scripts/sw.sh` (the builds must be the same bytes) and runs it in a fresh
profile against a server that loaded `classics` alone: it loads `listing`,
`yard` and `parts` as one batch, in an order that links only as a whole,
and holds the page, with each tab and each translation, and each list page, as
page and fragment, to a server that
loaded the same files; stops the worker and checks it answers again; checks
a hard reload says the files are paused; stops the worker during a load,
which the page gives up on (after `#browser`'s `data-stall`, here 3 s, in
place of the minute) with nothing stored; loads a 150,000-entry synthetic
vocabulary and times cold starts (the worker stopped, then a translation
naming the file), logging the worker's `Server-Timing` (module start, spine
bundle, indexes, link); serves a changed `/sw.js` and checks the new
worker waits beside the old one, which keeps answering, and the page says
so; and checks from the server's side that it got no
request with a body and none naming a browser file. Set
`NITROVIN_COLD_START_BUDGET=1s` to hold the cold start to a budget, on the
reference machine.

`TestWorkerUpdate` (`workerupdate_test.go`) installs the worker from one
release (the same build reporting v0.3.2) and then serves the next
(v0.3.3) at the same address, as a deploy does: the old worker passes the
page through, so the next release's page renders where it would have drawn
its own; the server gets no request for the old release's assets; the page
says the translator is outdated, refuses a load in words, and offers
Update now once the next worker waits, which axe audits; the click hands
the page to the next worker and reloads; the stored file survives and the
next worker answers for it; and the worker's pages and the server's show
the same versioned header.

`TestVocabPage` (`vocabpage_test.go`) runs the page's Vocabularies dialog in a fresh profile
without JavaScript and then in another with it, against a server that
loaded the drift fixture's `classics` and offers the EPA sample
(`cmd/nitrovin-epa/testdata/vehicles.jsonl`) as EPA's vocabulary, and
audits every state with axe. Each finding of the browser prototype's
[first review](https://github.com/joshuasprow/nitrovin/issues/147#issuecomment-6779155104)
is a step: a hard reload is paused (1); a record IndexedDB holds that the
release cannot read is listed with its fault and removed (2); a refused
remove shows, names its dependants and changes nothing, and "remove all"
removes them (3); a batch links whatever its order (4); the server's
catalog rows and a link through its file are the same with JavaScript on
and off (6); cold starts fetch the module and spine bundle no more than
once (7); and errors carry real status codes and file names, Remove
clicked the moment the panel shows goes by htmx, and an unreachable server
is said in words (8). `TestWorker` covers the stalled load (5). It also
loads EPA's vocabulary in one click and drops a file on the page.

`TestVocabTranslate` (`vocabtranslate_test.go`) clicks every example of
every tab without JavaScript, against a server that loaded the fixture and
the EPA sample, and with it, the files in the browser, and checks that
each fills its form; with JavaScript, that a file loaded is ticked, that
the selection survives a second load, a removal, a reload and a cold start
of the worker, and that a translation's or a link's `to=` replaces it on
its spine alone. axe audits both runs.

`TestVocabPartTargets` (`vocabparts_test.go`) adds, beside the fixture,
`shelf`, a second part vocabulary on `parts`, and `bin-labels` on another
spine, `bins`, both written from the fixture's files at run time. Without
JavaScript it checks that each spine has its tab, whose form offers its own vocabularies
alone, and that a `shelf` entry translates to a ticked `listing`; with it,
that a batch loaded is ticked on each spine and the selection survives a
tick, a reload, a cold start, a translation and a removal. axe audits both
runs.

`cmd/libnitrovin` is the C library (`-buildmode=c-shared`, no data).
`scripts/libnitrovin.sh <dir> [<goos>/<goarch>…]` builds it for all six
targets, or the ones named, from any host with `mise.toml`'s `zig` as the C
cross-compiler (`scripts/libnitrovin.sh build/libnitrovin` after `mise install`).

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
   `TestParity -parity-run` gates each file, zero tolerance. Beside them,
   `scripts/refresh.sh epa` downloads EPA's vehicles.csv, builds
   `epa-vehicles.jsonl` against the new artifact with `cmd/nitrovin-epa`,
   and runs `nitrovin vocab check` on it: a fault is red, drift is
   reported.
3. `scripts/refresh.sh report` writes the report: per-file parity, coverage
   of `pkg/decoder`, the EPA vocabulary's check (its drift, one row per
   finding) and coverage, what the list reaches (`coverage.json`), the
   stale-cache class and the witnesses against live vPIC.
4. Green: `TestParity -update-parity` re-freezes `testdata/parity/<month>`
   from file 1, `scripts/refresh.sh contract-key` cuts
   `pkg/contract/testdata/oracle` from it, and `TestText -update` rewrites
   the transcript. The month is pushed to nitrovin-data's `main`, whose
   `release.yml` rebuilds it, requires the same bytes and publishes
   `v<format>.<YYYYMM>.<n>` with the stale-cache list, the frozen key and
   `epa-vehicles.jsonl`.
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
(`.goreleaser.yaml`) with `mise.toml`'s Go, zig, goreleaser and cosign, so
a rebuild of a tag runs the same toolchain: the six binaries, the six C
library archives, the six Linux packages (`.deb`, `.rpm`, `.apk` for amd64
and arm64), `server.json`, `checksums.txt`, a cosign keyless bundle per
asset, the `ghcr.io/joshuasprow/nitrovin` image signed by digest, the
Homebrew cask in
`joshuasprow/homebrew-tap` and the Scoop manifest in
`joshuasprow/scoop-bucket`. It verifies every signature, checksum and
version stamp, then publishes `server.json` to the MCP Registry as
`io.github.joshuasprow/nitrovin` (GitHub OIDC, no secret) in a step of its
own. A pull request that touches the release builds a snapshot and installs
its packages in Debian, Fedora and Alpine. The monthly refresh tags patch
releases itself; a code change ships with the next tag. Until v1.0.0 every
tag is a prerelease (ADR 0007), and the workflow refuses a tag whose
prerelease setting disagrees.

## Benchmarks in the release

`bench/vocab` holds the vocabulary benchmarks for
[the performance budget](https://github.com/joshuasprow/nitrovin/issues/142):
load (the embedded data, `vocab.Open` and `NewTranslator`, with the peak RSS
of a process that loads once), translate (from a VIN, a name and a spine id)
and list (first page, next page, where on the first field, `vpic` where
make), on the fixture, the EPA vocabulary and a synthetic 1,000,000-entry
vocabulary at the scale of eBay's Master Vehicle List, which the run writes
to a temporary directory; and decode alone. Requests report p50 and p99
beside ns/op. `BenchmarkLoadCatalog` loads a catalog at the scale of ADR
0017's budget (790,000 fitments, 1,896,000 applications, half with
qualifiers and six in ten with option text of their own, 100,000
redirects, 1,326 vehicles, 448 of them unmapped), written to the same
temporary directory, as JSON Lines and compiled (ADR 0021), and reports
each file's size, the heap the load retains and a loading process's peak
RSS: against 15 s on one core, 1.5 GiB peak and a 500 MB file for JSON
Lines, and 1 s, 512 MiB peak and 256 MiB retained for the compiled form.
On the reference machine, on one core, JSON Lines measured 2.7 s, 910 MB
peak, 324 MB retained and a 330 MB file; compiled (format 2), 4.5 ms,
19 MB peak, 2.7 MB retained and a 342 MB file, about 180 bytes an
application: the 500 MB budget is set at this scale, so bytes per
application is the figure to watch (ADR 0021). `BenchmarkCompileCatalog`
records how long `vocab compile` takes on it (3.7 s) and the peak RSS of a
process compiling it (168 MB; `/usr/bin/time -l nitrovin vocab compile`
measures the CLI at 172 MB on one core and 202 MB on 18, against ADR 0023's
256 MiB), and `BenchmarkCatalogReader` times
`fitment.Reader`'s lookups in both forms, p50 and p99, with the compiled
form's reads per lookup (a 1,000-fitment page is one).
`BenchmarkFit` asks `fit` of that catalog on one core, in both forms,
cycling through 64 vehicles and fitment keys: candidates, part types and
related vehicles (each in a year and, the expensive path, with none) and
reverse, each reporting p50, p99 and its answers' average `total`
(`items`), against ADR 0018's p99 budget of 5 ms, 10 ms, 50 ms and 1 ms
per key, and on the compiled form its reads and bytes read per request,
against ADR 0023's 10, 20, 100 and 2. On the reference machine, p99, JSON
Lines measured 25 µs; 348 µs, and 7.8 ms with no year; 434 µs, and 9.4 ms
with no year; and 25 µs. The compiled form measured 147 µs; 614 µs, and
1.9 ms with no year; 729 µs, and 2.8 ms with no year; and 95 µs, in 3, 2,
2 and 1 reads: slower by its reads, but without the garbage collection of a
loaded catalog's heap that makes JSON Lines' no-year tail. It also asks
every kind of the fixture's wide variants, in both forms: the fixture with
0.1%, 0.3% and 1% of fitments of 50 to 500 applications, like universal
parts, and 1% again with longer year ranges and more qualifiers; and
candidates of such a fitment's part type (ADR 0023's Measured has them).
Related vehicles are their costliest request: at 1%, 25 ms compiled and
36 ms as JSON Lines with no year (`-benchtime 10s`; at the default 1 s a p99
is about the slowest of 85 requests), in at most 26 reads. `TestRelatedReadsSpread`
holds their reads in CI, on a small catalog laid out as the full-scale one
reads. `release.yml`'s bench job runs them on its runner, and
`bench/vocab/notes` turns the output into the table the release notes end
with, beside the budgets, which are stated for the reference machine (an
Apple M5 Pro). Nothing gates on it: runner timings are too noisy, and a
failed bench job leaves the table out rather than holding the release.
Dispatched, or on a pull request, the table is the bench job's summary.

To reproduce the table, with the EPA vocabulary of the data month `go.mod`
pins:

```sh
gh release download "$(go list -m -f '{{.Version}}' github.com/joshuasprow/nitrovin-data)" \
  -R joshuasprow/nitrovin-data -p epa-vehicles.jsonl -D /tmp
export NITROVIN_EPA_VOCAB=/tmp/epa-vehicles.jsonl
GOMAXPROCS=1 go test -run '^$' -bench . ./bench/vocab | tee bench.txt   # requests on one core
go test -run '^$' -bench Load ./bench/vocab | tee -a bench.txt          # load on every core
go run ./bench/vocab/notes bench.txt
```

Set GOMAXPROCS in the environment: `-cpu` is not followed by every
sub-benchmark.

## The README's benchmarks

The README's [Benchmarks](README.md#benchmarks) table is measured by hand
on one workstation, not in CI: nitrovin, ultravin, corgi and NHTSA's
procedure in Postgres and SQL Server over one 20,000,000-VIN corpus from
ultravin's generator. `bench/decode` is nitrovin's Go harness (throughput
in process, the CLI's timing, agreement with the oracle) and
`scripts/bench.sh` runs every row, one trial per command, and writes
`docs/benchmarks.json`. [docs/benchmarks.md](docs/benchmarks.md) has the
method, every trial and the command for each row. It needs Docker, an
ultravin checkout and about three hours for both rounds.
