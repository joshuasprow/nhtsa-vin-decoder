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
([the engine](#the-engine)).

CI runs what a change can affect. Every test workflow cancels its run on a
branch when a newer push supersedes it (`release.yml` and the refresh never
cancel), and none runs for Markdown, `GLOSSARY.md`, `NOTICE` or `docs/`
alone, except the API documents the contract tests read and the deviation
witnesses the oracle reads.

- `contract.yml` (schemas, the no-data tests and the `js/wasm` build):
  `pkg/contract`, `pkg/nitrovin`, `pkg/decoder`, `pkg/vocab`, `pkg/ui`,
  `cmd/nitrovin`, `cmd/nitrovin-epa`, `testdata/vocab/`, `docs/api`'s four
  schema documents, `go.mod`, `go.sum`.
- `import.yml`, **Tests**, on `pkg/`, `cmd/`, `go.mod`, `go.sum`,
  `testdata/parity/`, `testdata/vocab/`, `scripts/oracle.sh`, in three
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
  full=true` runs Postgres anyway.
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

`testdata/vocab` is the synthetic fixture every vocabulary test shares,
built against 2026_10: a vehicle vocabulary (`yard`: Dodge's Ram 1500
split by year across vPIC models 1938 and 13620, with Series and Drive
Type qualifiers, extension mappings, make mappings and an unmapped entry),
an extension set (`classics`), a declared spine (`parts`) and a part
vocabulary on it (`listing`, with Vehicle Type qualifiers and an unmapped
entry). It loads with no drift; `testdata/vocab/drift/` has one drift of
each kind. `go test ./pkg/vocab` refuses one file per fault class and
validates the fixture against the schema; `go test ./pkg/nitrovin` loads
the fixture and its drift variant against the embedded data.

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
tonnage in the name, narrowed to the values the model's VIN schemas emit.
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

`go test ./cmd/nitrovin-epa` builds a 78-row sample of vehicles.csv
(`testdata/`) twice, from the CSV and from a zip, against its golden file
(`-update` rewrites it), loads it against the embedded data with no drift,
and checks the built-in overrides for drift. Every data month publishes the
vocabulary: the monthly refresh builds it against the new month and its
data release carries `epa-vehicles.jsonl` (below).

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
data; `TestStdioOps` a stdio session mixing decode, translate and list
lines with `testdata/vocab`; `TestForm` drives the decode form (`/` and
`/ui/decode`) on the embedded data, over HTTP and then in a browser (below),
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

`pkg/ui` is the pages for browsers, the entry page at `/` and everything
under `/ui`
([ADR 0013](docs/adr/0013-browser-vocabularies-in-a-service-worker.md)),
which `serve` hands those paths to (and `/vocab` and `/sw.js`) and the
browser's Service Worker runs too, so it reaches its data through
interfaces (`ui.Pages`, the server's `ui.Backend`) and builds for
`js/wasm`. Each route answers a fragment of its templates (`ui.html`,
`decode.html`) for a request with `HX-Request`, its page's title first, and
the same fragment in the one layout for any other, with
`Vary: HX-Request` and the CSP (`ui.CSP`, `script-src 'self'`). Every error
has its real status: the one error page, or the error fragment that the
layout's htmx configuration swaps into its error slot; a page that refuses
with its own content, as the decode form does beside a field, sends htmx
to its target instead (`HX-Retarget`). The layout loads its stylesheet,
htmx and `copy.js` (copy buttons) from `/ui/r/<release>/` (`ui.Release`:
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
assets, the published files and the decode form's routes; `TestUI` and
`TestServePublished` in
`cmd/nitrovin` the same through `serve`.

The vocabulary pages (`vocab.go`, `vocab.html`: `/vocab`, `/ui/translate`,
`/ui/vocabularies[/{name}]`, the browser's `/ui/files`) answer the same on
the server and in the browser's Service Worker
([ADR 0013](docs/adr/0013-browser-vocabularies-in-a-service-worker.md)).
In the browser they answer from a `ui.Library`: the release's spine
bundle, the server's files linked from their published URLs, and the
browser's own files in IndexedDB, each with its parsed index
(`vocab.WriteIndex`, read back by `vocab.ReadIndex` instead of parsing the
file on every cold start), keyed by blake3 and release. Its rules (batch
loads that link or change nothing, replace by name, refused removes with
dependants, stored files that fault left out and listed, the decode's data
month checked) are plain Go, tested with an in-memory `ui.Disk`
(`library_test.go`). `cmd/nitrovin-sw` is the module: `ui.NewInBrowser`
over a `Library`, given IndexedDB and `fetch` by `pkg/ui/static/worker.js`,
the worker script that `/sw.js` (its release and the server's files, never
cached without asking, the only response allowed `'wasm-unsafe-eval'`)
imports. `scripts/sw.sh [<dir>]` builds the module reproducibly
(`-trimpath -buildvcs=false`) into `cmd/nitrovin/sw`, which git ignores
and `serve` embeds; goreleaser's before hook runs it, so release builds
serve the worker and others serve `/vocab` without it. `vocab.js` is
`/vocab`'s script: it registers the worker, says its state (ready, paused
after a hard reload, an update waiting for the old release's tabs to
close) and hands picked files to it by `postMessage`, never in a request,
giving up on a load after a minute without progress.

`TestForm` then runs the form in headless Chrome or Chromium
(`browser_test.go`, over the DevTools protocol with chromedp), twice, each
in a browser with a fresh profile: an installed Service Worker survives
JavaScript being turned off, so a profile that had JavaScript would answer
the JavaScript-off run from the worker. Without JavaScript every step is a
page load; with it, every step must reach the server with `HX-Request` and
never reload the page, and back and forward restore each state. axe-core
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
and holds every translation and list page and fragment to a server that
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
beside ns/op. `release.yml`'s bench job runs them on its runner, and
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
