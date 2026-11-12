---
status: proposed
---

# A compiled catalog in the artifact's format family, behind the same reader

A catalog (ADR 0016, ADR 0017) can be compiled into an indexed file that opens in
about a second, with a fraction of the memory JSON Lines needs. This was decided
on [The compiled fitment form](https://github.com/joshuasprow/nitrovin/issues/228),
on [the fitment and stock map](https://github.com/joshuasprow/nitrovin/issues/221).
It amends ADR 0011, which deferred a compiled form "to a measured need". The
measurements in the map's Notes are that need: at catalog scale, JSON Lines takes
503–742 MiB retained, peaks at 972–1,419 MiB, and needs 2–3.3 s on one core.

**Status: proposed until measured.** The format freezes only when a prototype meets
the budget below on the catalog-scale fixture. Until then the format may change,
and this ADR then flips to accepted. If it misses, a person decides whether to
change the format or the budget.

## The format

The compiled catalog is in the artifact's family (ADR 0003): fixed-width columns
sorted by key, with string pools, read in place by binary search.

- **Indexes.** The four lookups of `fitment.Reader` (ADR 0017) each get a sorted
  index table:
  - by part type and vehicle;
  - by number;
  - redirects by part type and vehicle;
  - retired numbers.
- **Reading.** Reads go through an `io.ReaderAt`, never a memory map, so the same
  reader could run by byte range in a browser's file store. That case is not built,
  and no user needs it today, but nothing in the format rules it out.
- **Code.** It reuses the artifact's writer and reader code where it fits, adds no
  dependency, and builds for js/wasm.
- **Interface.** It implements `fitment.Reader`, so `fit` (ADR 0018) never knows which
  form is loaded.

## Building and identifying it

`nitrovin vocab compile <catalog.jsonl> -o <file>` runs the full `vocab check`
first and refuses on any fault. The compiled file's header records:

- a magic and the compiled-format version;
- the source file's blake3, and the catalog's name, version and `release`;
- the spine and vehicle vocabulary it links to;
- the nitrovin version that wrote it;
- the data month it was checked against, with that check's drift counts.

Rebuilding from the same source with the same release gives the same bytes,
as ADR 0003 requires of the artifact.

`--vocab` recognises a compiled file by its magic. It refuses:

- a compiled-format version it cannot read;
- a spine or vocabulary the file links to that is not loaded, exactly as for JSON
  Lines.

## What stays as it is

- **JSON Lines** stays loadable directly, and stays the published, checked and
  diffable form. Compiling is a deployment's choice.
- **Only catalogs are compiled.** Vocabularies, spines, extension sets and stock
  files load in milliseconds: EPA's 5,747-entry vocabulary takes under 15 ms.

## Drift

Loading a compiled file does not scan every application, because that would spend
the open budget.

- **`/v1/info`** reports the drift counts the compile stored, with the data month
  they were measured against, and says when the loaded month differs.
- **`nitrovin vocab check`** on a compiled file re-runs the full drift check against
  the loaded month. It is slower, and meant for CI.
- **Requests** that touch a vPIC id the month lacks still mark it, as with JSON Lines.

## Budget

Measured on the catalog-scale synthetic fixture (ADR 0017: about 790,000 fitments,
1.9 million applications, half with qualifiers, and 100,000 redirects), on one core:

| | Budget |
| --- | --- |
| Retained heap | at most 256 MiB |
| Peak while opening | at most 512 MiB |
| Open and ready | at most 1 s |
| Compiled file | at most 500 MB |
| `fit` requests | ADR 0018's budgets still met |

Compile time is unbounded but recorded. The CI benchmark records both forms side by
side. The decoder's artifact, by comparison, opens in 1 ms with 1 MiB.

## Considered options

- **bbolt:** an embedded B+tree, 2.0 MB. It does not build for js/wasm.
- **Pebble:** an LSM store, 14.4 MB, 126 modules. It does not build for js/wasm, and
  it is a directory, not one file.
- **A memory-mapped reader:** simpler in Go, but it closes off reading by byte range
  in a browser.
- **A compiled file that opens only under the release that wrote it:** stricter than
  a format version needs, and a burden on anyone who keeps compiled files between
  releases.
- **Compiling every file kind:** they already load in milliseconds.
- **Re-checking drift at every load:** it spends the budget the compiled form exists
  to save.

## Consequences

- A build ticket prototypes the format, measures it on the fixture, and records the
  numbers on [#228](https://github.com/joshuasprow/nitrovin/issues/228). This ADR then
  becomes accepted, or a person reopens the format or the budget.
- The fitment file's schema (ADR 0017) is unchanged. The compiled form is not a
  contract document; it belongs to the release family that reads it.
