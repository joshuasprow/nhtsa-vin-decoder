# PROTOTYPE: artifact format bake-off (throwaway)

Throwaway code for wayfinder ticket "Artifact format bake-off: custom indexed
binary vs SQLite vs flatbuffers" (joshuasprow/nitrovin#17). Lives on the
`prototype/artifact-format` branch only; never merged. Not the importer, not the
decoder: a rough importer for every table the port plan names
(`docs/research/ultravin-engine.md` §2 on `research/ultravin-engine`) and a stub
hot path (sanitize, WMI lookup, year band, pattern scan + match, per-element
dedup, lookup resolve) run identically over three storage candidates.

```sh
scripts/oracle.sh up && scripts/oracle.sh restore <backup>   # repo root
cd prototypes/artifact-format && ./measure.sh                # writes results/
```

- `bake/tables.go` the logical schema: 17 tables + one `lookup` union of every
  `element.lookuptable` that exists in the dump, plus optional `wmiyearvalidchars`
- `bake/extract.go` Postgres → TSV via `COPY ... TO STDOUT` in the oracle container
- `bake/custom.go` custom indexed binary: column arrays + one string arena, read in place
- `bake/sqlite.go` SQLite via `modernc.org/sqlite`, `WITHOUT ROWID` clustered tables
- `bake/fb.go` flatbuffers: schema generated from the same model, generic builder,
  flatc-generated accessors in `nvfb/` for the hot path
- `bake/decode.go` the shared stub decoder; every candidate must produce the same checksum
- `cmd/bakeoff` build / bench / decode / dump / genvins
- `cmd/embedded` the `go:embed` shape: one binary per (format, raw|zstd)
