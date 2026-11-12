---
status: accepted
---

# A compiled catalog in the artifact's format family, behind the same reader

A catalog (ADR 0016, ADR 0017) can be compiled into an indexed file that opens in
about a second, with a fraction of the memory JSON Lines needs. This was decided
on [The compiled fitment form](https://github.com/joshuasprow/nitrovin/issues/228),
on [the fitment and stock map](https://github.com/joshuasprow/nitrovin/issues/221).
It amends ADR 0011, which deferred a compiled form "to a measured need". The
measurements in the map's Notes are that need: at catalog scale, JSON Lines takes
503–742 MiB retained, peaks at 972–1,419 MiB, and needs 2–3.3 s on one core.

**Status: accepted on measured numbers** (2026-11-12,
[#247](https://github.com/joshuasprow/nitrovin/issues/247)). A prototype met the
budget below on the catalog-scale fixture by a wide margin (see Measured), and the
format froze as compiled format 1.

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

## Measured

The prototype that became the format, measured by `bench/vocab` on one core
(`GOMAXPROCS=1`, Apple M5 Pro, page cache warm) and
[recorded on #228](https://github.com/joshuasprow/nitrovin/issues/228#issuecomment-6816515002):

| | JSON Lines | Compiled | Budget |
| --- | ---: | ---: | ---: |
| Retained heap | 322 MB | 2.1 MB | 256 MiB |
| Peak while opening | 900 MB | 16 MB | 512 MiB |
| Open and ready | 2.67 s | 3.1 ms | 1 s |
| File | 330 MB | 184 MB | 500 MB |
| Compile | | 3.9 s | recorded |

Retained heap and peak include the Decoder and the spine and vocabulary beside the
catalog. `fitment.Reader`'s lookups, p99, against ADR 0018's request budgets: one
part type for a vehicle and year 17 µs (5 ms), all 205 part types for one vehicle
1.8 ms (10 ms), a number 25 µs (1 ms), a redirect 15 µs, and a 1,000-fitment page
0.7 ms. That is 5–60× slower than the in-memory JSON Lines reader, and at least 50×
inside every budget. Whole `fit` requests (`BenchmarkFit`), p99 on the compiled
form: candidates 37 µs (5 ms), part types 2.6 ms and 5.8 ms with no year (10 ms),
related vehicles 1.5 ms and 4.7 ms with no year (50 ms), and reverse 38 µs (1 ms).

What was built, where the text above leaves room:

- **Rows, not column arrays.** Each table is fixed-width rows sorted by key, so one
  read fetches a row: a reader through `io.ReaderAt` pays per read, where the
  artifact, read in memory, pays nothing.
- **Only the dictionary is held.** Opening reads the header, the part types, the
  vehicles and the distinct qualifier sets. Every lookup reads its index rows,
  fitment and application rows and strings, merging reads of strings that lie
  near each other.
- **The artifact's code is not reused.** Its writer is bound to the vPIC tables'
  schema and its reader reads in place from memory, so the compiled catalog shares
  its conventions instead: a magic, a format version, the file's size, an 8-byte
  aligned section directory, and the blake3 of the file with that hash's own bytes
  zero, read from the header at open and checked by `vocab check`.
- **Compile takes what check takes.** `nitrovin vocab compile -o <file> <file|dir>…`
  loads its arguments as `vocab check` does, so the catalog's spine and vehicle
  vocabulary are among them, and compiles the one catalog. The header stores every
  drift finding the check made, not only the counts, so `/v1/info`'s examples and
  the load's stderr read as for JSON Lines.
- **Links are checked at load by the dictionary.** `--vocab` refuses a compiled
  file whose spine or vehicle vocabulary is not loaded, as for JSON Lines, and also
  one whose loaded spine lacks a part type it names or whose loaded vocabulary no
  longer has its vehicles' fields: each part type and vehicle once, about the file
  as a whole.
- **Vehicle drift is measured at load.** It depends on the vehicle vocabulary
  loaded, not the month, and takes one look per vehicle, so a compiled catalog
  stores each vehicle's first line and count and measures it again at load. The
  rest of its drift is the stored drift. `/v1/info`'s `compiled` says
  `links_differ` when the spine or vehicle vocabulary loaded is not the file it was
  compiled against, by blake3.
- **`model-qualifier` drift is stored too.** Compiling runs `vocab check`'s whole
  check, `model-qualifier` (ADR 0015) included, and stores those findings with the
  rest. As for JSON Lines, load reports none of them and `LoadInfo` counts none;
  `AllDrift` and `/v1/info` serve them, from the file rather than by building the
  model values index.

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

## Amendment (2026-11-12): format 2 and one streaming compiler

[ADR 0023](0023-fitment-in-the-browser.md) (proposed until measured) replaces format 1 with compiled format 2,
clustered by vehicle, because format 1 costs thousands of reads per part-type
summary when read by byte range in a browser. One streaming compiler, peaking at
256 MiB, serves the CLI and the browser. Format 1 files are refused with "recompile".
This ADR's budgets and Measured section remain the bar format 2 must meet.

## Measured: format 2 (2026-11-12)

Format 2 replaced format 1 ([#262](https://github.com/joshuasprow/nitrovin/issues/262)),
measured as above (`bench/vocab`, one core, Apple M5 Pro) and
[recorded on #260](https://github.com/joshuasprow/nitrovin/issues/260):

| | Format 1 | Format 2 | Budget |
| --- | ---: | ---: | ---: |
| Retained heap | 2.1 MB | 2.7 MB | 256 MiB |
| Peak while opening | 16 MB | 19 MB | 512 MiB |
| Open and ready | 3.1 ms | 4.5 ms | 1 s |
| File | 184 MB | 342 MB | 500 MB |
| Compile | 3.9 s | 3.7 s | recorded |
| Compile peak, `nitrovin vocab compile` | about 900 MB | 202 MB | 256 MiB (ADR 0023) |

Whole `fit` requests, p99, against ADR 0018's budgets: candidates 147 µs (5 ms),
part types 614 µs and 1.9 ms with no year (10 ms), related vehicles 729 µs and
2.8 ms with no year (50 ms), and reverse 95 µs (1 ms). Each is one to three reads
of the file; ADR 0023's Measured has them. The file is larger because each
vehicle's block holds a copy of its fitments; it stays inside the budget, at
about 180 bytes an application. The budget's 500 MB is set at ADR 0017's
catalog scale, 1.9 million applications, so bytes per application is the
figure to watch: a catalog of more applications may pass 500 MB at the same
rate, as ADR 0023's wide variants do (595 MB for 4.1 million, about 146 bytes
each). The compiler streams, so it no longer loads the catalog as JSON Lines
first.
