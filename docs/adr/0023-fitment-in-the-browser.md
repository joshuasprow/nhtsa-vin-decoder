---
status: proposed
---

# Fitment in the browser: a compiled format clustered by vehicle, compiled and read there

A visitor can load a catalog into their own browser, compile it there, and run `fit`
on a `/ui/fit` page that the Service Worker answers. The file never leaves that
browser. This was decided on
[Fitment in the browser](https://github.com/joshuasprow/nitrovin/issues/260), on
[the fitment and stock map](https://github.com/joshuasprow/nitrovin/issues/221).

It amends four ADRs:

- **ADR 0013:** the browser no longer refuses catalogs and stock files.
- **ADR 0017:** one catalog per nitrovin now counts the server and its browsers
  together.
- **ADR 0018:** `fit` gains a page.
- **ADR 0021:** compiled format 2 replaces format 1, and one streaming compiler
  replaces the in-memory one.

**Status: proposed until measured.** A prototype must meet the budgets below. The
format half is measured in Go on the catalog-scale fixture. The browser half is
measured in current stable Chrome, Safari and Firefox. If either half misses, the
work stops and a person decides whether to change the design or the budget.

## Who it is for

Two people:

- **The author:** someone with a catalog as JSON Lines who wants to see what it
  answers, with no install.
- **The receiver:** someone given a compiled file, who opens it the same way.

`nitrovin serve` on their own machine covers both, but needs an install.
nitrovin.app ships no catalog (the map's scope), so this is the only way it can
answer a `fit`.

## Why format 1 cannot serve the browser

In a browser, the compiled file is read by byte range through an asynchronous
store. A read costs 0.2–0.4 ms from a `Blob` in IndexedDB, measured in Chrome on
[#228](https://github.com/joshuasprow/nitrovin/issues/228#issuecomment-6802709630).

Format 1 is ordered by part type. Counted with a wrapped `ReaderAt` on the
catalog-scale fixture (790,000 fitments, 184 MB), at 0.25 ms a read:

| Request | Reads, p99 | At 0.25 ms | With a 64 KB-block cache | ADR 0018 budget |
| --- | ---: | ---: | ---: | ---: |
| candidates | 33 | 8 ms | 2.2 ms | 5 ms |
| part types | 5,625 | 1,406 ms | 149 ms | 10 ms |
| part types, no year | 11,620 | 2,905 ms | 612 ms | 10 ms |
| related vehicles | 3,366 | 842 ms | 146 ms | 50 ms |
| related vehicles, no year | 7,650 | 1,913 ms | 534 ms | 50 ms |
| reverse, one key | 29 | 7 ms | 1.5 ms | 1 ms |

The reads come from the layout:

- A part-type summary binary-searches once per part type (205 of them), reading
  one row per probe.
- Each hit then costs about six more reads, scattered across 136 MB.
- A cache can't fix it: with 64 KB blocks, a request pulls 40–235 MB.

## The decision

- **Compiled format 2, clustered by vehicle.**
  - A vehicle's part types and counts, its postings, and enough of each fitment to
    answer are stored together, so a request costs a few reads, not thousands.
  - Related vehicles may be a table per vehicle, computed when compiling.
  - Sparse indexes (every Nth key) may sit in memory.
  - Format 2 replaces format 1 on the server and in the browser alike. Format 1
    shipped in v0.3.5 with no users. A release opening a format 1 file refuses it,
    saying to recompile.
- **One streaming compiler.** The CLI and the browser run the same compiler. It
  reads JSON Lines once and sorts externally through scratch storage: a temporary
  directory, or IndexedDB. Its peak is at most 256 MiB, which also lowers the
  CLI's from about 900 MB.
- **Compiling in the browser.**
  - **Where it runs:** in a dedicated worker the page starts, running the same
    `sw.wasm`, because browsers may stop a Service Worker that spends minutes on
    one event. The page shows progress and must stay open.
  - **What it reads:** the picked JSON Lines, read in place from its `File` and
    never stored. The compiled file is written to IndexedDB as a `Blob`, streamed,
    so neither Go nor the page holds all of it.
  - **What it checks:** every fault, and every drift kind the spine bundle can
    measure. The spine bundle carries no model years and no model values
    (ADR 0022, ADR 0015), so a browser compile skips `model-years` and
    `model-qualifier`. The header records the kinds it skipped, the Vocabularies
    dialog says so, and `vocab check` on a server runs them all.
- **Reading in the browser.**
  - The Service Worker opens the stored `Blob` and reads it by range through an
    `io.ReaderAt`. Go's goroutine waits on each read's promise, as it does for
    IndexedDB today.
  - The catalog's spine and vehicle vocabulary are linked as for any browser file
    (ADR 0013): the server's published files, or the visitor's own.
  - A release that can't open the stored file, or a changed spine or vehicle
    vocabulary, leaves it listed with the reason and "pick the source again to
    recompile". It is not compiled again in the background.
- **One catalog per nitrovin.** A browser refuses to load a catalog when its server
  has one, mirroring ADR 0017's fault. The Service Worker answers the fit page only
  when the browser holds the catalog, and forwards it to the server otherwise.
  `/v1/fit` is always the server's.
- **Stock files load in the browser** as shelf and yard sources (ADR 0019).
  Connectors never do (ADR 0020).
- **The fit page.**
  - `/ui/fit` takes the translate page's inputs: a VIN, or a vocabulary entry with
    a year.
  - It shows the part types with their counts. Picking one shows its candidates,
    with how each was reached and what a decode couldn't confirm.
  - A fitment's number links to its vehicles. Related vehicles are a section, and
    stock lists appear when stock is loaded.
  - Every list pages with the API's cursors, and the page works without
    JavaScript.
  - Its URLs are `/v1/fit`'s parameters, so a page link and an API call say the
    same thing.
  - The server and the Service Worker answer it with the same Go handlers.
  - Every page is reachable from a navigation header.
- **Links and privacy.** ADR 0013's rule holds:
  - A browser's files never leave it.
  - A link naming them answers only in that browser.
  - The browser offers a download of a compiled file it built, to the visitor's
    own disk.

## Budget

| | Budget |
| --- | --- |
| Reads per request (CI, a counting `ReaderAt`) | candidates ≤ 10; part types ≤ 20, with or without a year; related ≤ 100, with or without a year; reverse ≤ 2 per key; a 1,000-fitment page ≤ 8 |
| `fit` p99, server and the three browsers | ADR 0018's, unchanged |
| Retained in the browser | ≤ 64 MiB |
| Compile peak, CLI and browser | ≤ 256 MiB |
| Compiled file | ≤ 500 MB (ADR 0021) |
| Open, retained and peak on the server | ADR 0021's, unchanged |

The read budgets are set so reads at 0.25 ms take at most half of each request's
budget, leaving the rest for wasm compute. Browsers can't run in CI, so CI holds
the read counts and the release notes record the browser numbers.

## Considered options

- **A browser-only layout beside format 1:** two writers and two readers kept in
  step, for a server that format 2 serves as well.
- **Format 1 read synchronously from the origin private file system, in a
  dedicated worker:** each read is under 0.1 ms, but that is unmeasured below the
  timer's resolution. It is still thousands of reads, and the Service Worker would
  have to route every `fit` through the page.
- **A block cache over format 1:** cuts the reads 3–4× and pulls megabytes per
  request.
- **A looser budget in the browser:** format 1 would still miss it by 15× or more.
- **Opening compiled files only, compiling with the CLI:** the person with JSON
  Lines would still need an install.
- **Fetching per-model data from the server so a browser compile runs every
  check:** a bigger spine bundle or a new endpoint, for two drift kinds that
  `vocab check` runs anyway.
- **Keeping the JSON Lines source in the browser:** about 330 MB more quota, to
  save picking the source again.
- **Letting the browser's catalog shadow the server's:** two answers to one link.
- **The origin private file system:** faster to write (179 ms against 668 ms for
  200 MB), but a second store beside IndexedDB to keep consistent.

## Consequences

- ADR 0021's format 1 is retired before anyone keeps a file in it. Its measured
  server numbers are the bar format 2 must not miss.
- [#260](https://github.com/joshuasprow/nitrovin/issues/260) files four build
  tickets:
  1. format 2 and the streaming compiler, with the Go half of the gate;
  2. the browser side, with the browser half of the gate;
  3. the fit page;
  4. stock files in the browser.
- The map's open question on the `/ui` pages for fitment is settled for pages; MCP
  already has `fit`.
