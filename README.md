# nitrovin

An offline VIN decoder that answers like NHTSA's vPIC, as one binary.

Status: **charting**. Nothing decodes yet. The plan is the
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

## The Postgres oracle

`scripts/oracle.sh` restores a month of NHTSA's dump into Postgres 17 and
runs its `spvindecode` over a VIN list, writing an answer key (CSV, every
row the procedure emits per VIN, in a stable order). The database's
collation is `C` (ctype `en_US.utf8`), ultravin's oracle contract: `restore`
and `decode` refuse any other, and CI's `manifest.json` records it beside
the key. CI runs it through the reusable workflow
`.github/workflows/oracle.yml`; on a workstation with Docker:

```sh
scripts/oracle.sh up
scripts/oracle.sh fetch "$(scripts/oracle.sh month)" /tmp/vpic   # prints backup=<path>
scripts/oracle.sh restore /tmp/vpic/vPICList_lite_<YYYY_MM>.backup
scripts/oracle.sh decode testdata/oracle/sample-vins.tsv answer-key.csv
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

## The engine

`pkg/decoder` ports `spvindecode` stage by stage, each tested against
Postgres running the dump's own SQL. Most answer keys are committed under
`pkg/decoder/testdata` (regenerate with `go test ./pkg/decoder -run <Test>
-update-oracle` against a restored dump). The pattern matcher is also checked
on every distinct `pattern.keys` of a month and every VinSchema's index, and
the core pass (stage 5) on a VIN corpus against `vpic.spvindecode_core` called
directly, item for item before value resolution, against the database and an
artifact built from it:

```sh
NITROVIN_PSQL='docker exec -i nhtsa-oracle psql -X -q -U postgres -d vpic' \
NITROVIN_ARTIFACT=nitrovin.artifact go test ./pkg/decoder -run 'TestMatcherPostgres|TestCorePostgres'
```

`.github/workflows/import.yml` runs these on PRs touching the importer or
the engine.
