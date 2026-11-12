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
The format half is met on the catalog-scale fixture (2026-11-12,
[#262](https://github.com/joshuasprow/nitrovin/issues/262); see Measured), and
with a share of fitments of hundreds of applications too, related vehicles
included, since [#267](https://github.com/joshuasprow/nitrovin/issues/267)
(2026-11-12). The browser half was measured on 2026-11-12 and missed in
Firefox, WebKit and Chromium (see The browser half). This ADR stays proposed
until the browser half's ticket,
[#263](https://github.com/joshuasprow/nitrovin/issues/263), lands.

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

- **Compiled format 2, clustered by vehicle.** Measured, it shipped as format
  3: format 2 with its wide section holding only what related vehicles read of
  each fitment ([#267](https://github.com/joshuasprow/nitrovin/issues/267)),
  so a format 2 file, never released, is refused with "recompile".
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

## Measured

### The format half

Compiled format 2 and the streaming compiler, measured by `bench/vocab` on the
catalog-scale fixture on one core (`GOMAXPROCS=1`, Apple M5 Pro, page cache warm),
reads counted through a wrapped `io.ReaderAt`, and
[recorded on #260](https://github.com/joshuasprow/nitrovin/issues/260):

| Request | Reads, p99 / most | Read budget | Server p99 | ADR 0018 budget |
| --- | ---: | ---: | ---: | ---: |
| candidates | 3 / 3 | 10 | 147 µs | 5 ms |
| part types | 2 / 2 | 20 | 614 µs | 10 ms |
| part types, no year | 2 / 2 | 20 | 1.9 ms | 10 ms |
| related vehicles | 2 / 2 | 100 | 729 µs | 50 ms |
| related vehicles, no year | 2 / 2 | 100 | 2.8 ms | 50 ms |
| reverse, one key | 1 / 1 | 2 | 95 µs | 1 ms |
| a 1,000-fitment page | 1 / 1 | 8 | 418 µs | |

| | Format 2 | Budget |
| --- | ---: | ---: |
| Open and ready | 4.5 ms | 1 s (ADR 0021) |
| Retained heap | 2.7 MB | 256 MiB (ADR 0021) |
| Peak while opening | 19 MB | 512 MiB (ADR 0021) |
| File | 342 MB | 500 MB |
| Compile | 3.7 s | recorded |
| Compile peak, `nitrovin vocab compile` (`/usr/bin/time -l`) | 202 MB on 18 cores, 172 MB on one | 256 MiB |

At 0.25 ms a read, the worst request spends under 1 ms reading.

### With wide fitments

The fixture's fitments have one to five applications. Its wide variants add a
share of fitments like universal parts, each of 50 to 500 applications on
vehicles and years at random, over 20 part types. The last has 1% as the third
does, with each application spanning up to 16 years rather than 6, and three
in four with qualifiers rather than one in two, as the review of #262 found
reads differ by. `BenchmarkFit`, compiled, one core: p99, reads p99 / most,
and the most bytes a request read; related vehicles as JSON Lines too, p99.
Related vehicles ran for 10 s each (600–10,000 requests), so their p99 is not
the slowest of about 85 requests
([#267](https://github.com/joshuasprow/nitrovin/issues/267), 2026-11-12).

| Request | 0.1% (790) | 0.3% (2,370) | 1% (7,900) | 1%, other years and qualifiers | ADR 0018 |
| --- | ---: | ---: | ---: | ---: | ---: |
| candidates | 152 µs · 3 / 3 · 8 KB | 152 µs · 3 / 3 · 8 KB | 147 µs · 3 / 3 · 8 KB | 147 µs · 3 / 3 · 8 KB | 5 ms |
| candidates of a wide fitment's type | 141 µs · 3 / 3 · 7 KB | 162 µs · 3 / 3 · 8 KB | 223 µs · 3 / 3 · 12 KB | 236 µs · 3 / 3 · 12 KB | 5 ms |
| part types | 672 µs · 2 / 2 · 221 KB | 795 µs · 2 / 2 · 240 KB | 1.2 ms · 2 / 2 · 308 KB | 1.3 ms · 2 / 2 · 308 KB | 10 ms |
| part types, no year | 2.1 ms · 2 / 2 · 221 KB | 2.4 ms · 2 / 2 · 240 KB | 3.6 ms · 2 / 2 · 308 KB | 3.4 ms · 2 / 2 · 308 KB | 10 ms |
| related vehicles | 3.0 ms · 4 / 4 · 1.6 MB | 4.8 ms · 10 / 10 · 4.0 MB | 8.1 ms · 25 / 25 · 11.8 MB | 13 ms · 26 / 26 · 14.4 MB | 50 ms |
| related vehicles, no year | 6.7 ms · 3 / 3 · 1.7 MB | 10 ms · 6 / 6 · 4.7 MB | 25 ms · 16 / 16 · 15.2 MB | 22 ms · 16 / 16 · 15.2 MB | 50 ms |
| related vehicles, JSON Lines | 4.8 ms | 7.8 ms | 11 ms | 22 ms | 50 ms |
| related vehicles, no year, JSON Lines | 14 ms | 24 ms | 36 ms | 36 ms | 50 ms |
| reverse | 92 µs · 1 / 1 · 15 KB | 95 µs · 1 / 1 · 15 KB | 94 µs · 1 / 1 · 15 KB | 92 µs · 1 / 1 · 15 KB | 1 ms |
| File | 363 MB | 404 MB | 551 MB | 551 MB | 500 MB at the fixture's scale |
| Bytes per application | about 171 | about 158 | about 135 | about 135 | about 180 at the fixture |

Every read budget and every p99 holds here, in both forms. Part types,
candidates and reverse do not move: they read the vehicle's block, which holds
what answers for it of every fitment. Related vehicles read, besides the block,
the vehicles and years of the wide fitments the vehicle shares, from wide,
which holds only those; their reads grow with them, at most 26 of 100 at 1%.
On the fixture they are one read, the block, where #262 measured two, and
729 µs and 2.8 ms (no year) measured 655 µs and 2.4 ms.

A related request allocates about 1–11 MB as JSON Lines and 3–43 MB compiled
at these shares, most of it the reader's answer (compiled, the bytes it read),
where it allocated up to 408 MB and 578 MB before; part types, candidates and
reverse allocate as before. A related request no longer allocates in
proportion to the catalog's vehicles: what it keeps per vehicle is kept between
requests.

Before #267, related vehicles missed both budgets. Wide held the fitments
whole, so their reads were 93 / 94 at 1%, and 109 / 116 on the review's 1%
catalog, of other years and qualifiers; and `fit` built every row of the
answer, 43,000–71,000 vehicles and year ranges, before taking a page: 56 ms
and 208 ms with no year compiled, 85 ms and 220 ms as JSON Lines, at 1%. Both
were in how related vehicles were read and built, not in what they mean: every
`total`, cursor and page's set of rows is as before. Only the order among rows
tied on count, name and years, of different vehicles of one name, changed: it
was whatever an unstable sort left, and is now the order the catalog first
lists them among the shared fitments (ADR 0018's amendment). JSON Lines' no-year
p99 is its closest to the budget, at 36 ms, and its tail is a loaded catalog's
garbage collection, as on the fixture: its p50 is 13 ms.

**The file budget is per application.** ADR 0021's 500 MB is set at the
catalog scale ADR 0017 defines, the fixture's 1.9 million applications: about
180 bytes an application, and the figure to watch as catalogs grow. The 1%
variant's 551 MB holds 4.1 million applications, twice the fixture's, at about
135 bytes each, so it is within that rule.

Format 1 on the same fixture: 33 reads for candidates, 5,625 for part types and
11,620 with no year, 3,366 and 7,650 for related vehicles, and 29 for reverse.

What was built, where the text above leaves room:

- **A vehicle's block is its head and its body.** The head lists its part
  types, each with its count and its bytes in the body, and its redirects; the
  body holds a copy of every fitment with an application to the vehicle, whole,
  by part type, side and number. One part type is two reads, the head and that
  type's run; every part type is one read of the whole block.
- **`fitment.Reader` asks for every part type at once.** `ByVehicle` and
  `Redirect` take part type `""` for all of them, and `fit` asks that way, once,
  for part types and related vehicles, rather than once per part type of the
  spine. A JSON Lines catalog answers `""` too.
- **Part types and candidates read only what matched.** `fitment.Reader`'s
  `Matching` is `ByVehicle` answering with the applications that matched and
  the fitment's key, line and note: all a part-type summary or a candidate
  needs. `fit` asks it for part types and candidates (ADR 0017's amendment).
- **Related vehicles are read, not stored.** Each whole copy in a block carries
  every application of its fitment, so related vehicles come from the block,
  and from the wide section for the wide fitments it shares (below); no table
  per vehicle was needed. They need of a fitment's other applications only
  each one's vehicle and years: `vocab.Catalog`'s `Shared` answers `Matching`'s
  candidates with those, as `fitment.Sharer`, an interface a `Reader` may also
  satisfy, and `fit` falls back to `ByVehicle` for a `Reader` that doesn't.
- **Related vehicles build only their page.** `fit` groups the shared
  applications by vehicle, cuts each vehicle's years in one sweep, and keeps
  each row as a vehicle, years and count; it sorts only the rows of the counts
  the page reaches, and counts each row's part types only once it is on the
  page. Its storage is kept between requests, so a request sharing hundreds
  of wide fitments allocates little beyond what the reader answers: a
  browser's garbage collection shows in its p99. `total`, cursors and each
  page's rows are as building every row gave, and rows that tie on count, name
  and years come in the order the catalog first lists their vehicles among the
  shared fitments ([#267](https://github.com/joshuasprow/nitrovin/issues/267)).
- **Wide fitments are not copied whole.** A fitment of more than 16
  applications would cost the file the square of its applications if every
  vehicle's block held it. Its blocks hold what answers for their vehicle, its
  key, line and note and its applications to that vehicle, whole, and where the
  fitment lies in keyed and in wide. Wide holds only such fitments, each as
  its applications' vehicles and years, about a seventh of its bytes whole.
  `Matching` answers from the block; `Shared`, for related vehicles, reads the
  vehicles and years from wide, and `ByVehicle` reads the fitment whole from
  keyed, nearby ones together either way, within 256 KiB of each other and
  1 MiB in all. Wide held the fitments whole until
  [#267](https://github.com/joshuasprow/nitrovin/issues/267): a vehicle sharing
  hundreds of them then read them in a read for each 1 MiB, or for each one
  lying farther than 256 KiB from the last, up to 116 reads at 1%. The
  threshold was measured on the fixture with 1% of fitments of 6 to 50
  applications, before that (related vehicles, one core):

  | Threshold | File | Related: reads, most · p99 | No year: reads, most · p99 |
  | ---: | ---: | ---: | ---: |
  | 4 | 318 MB | 55 · 3.7 ms | 28 · 15 ms |
  | 8 | 369 MB | 14 · 2.9 ms | 9 · 12 ms |
  | 16 | 374 MB | 14 · 2.9 ms | 9 · 11 ms |
  | 32 | 425 MB | 11 · 2.6 ms | 7 · 11 ms |

  Below 8 the fixture's own fitments go far and reads climb; above 16 the file
  grows a seventh for three reads. 16 it is. Above 50 applications no threshold
  helps: copying a 275-application fitment into each of its vehicles' blocks
  costs about 3.8 MB.
- **Sparse indexes in memory.** Every fitment is also stored once in key order,
  in pages of 64: the dictionary holds each page's first key, so a number is one
  read (two through a retired number), and a 1,000-fitment page is one.
- **The compiler holds no line.** It streams fitments and redirects through
  two external sorts, by key and by vehicle, in runs of 16 MiB, and retired
  numbers through two more, by key and by successor, in runs of 4 MiB, merged
  beside the fitments by key. What it holds grows with the catalog's
  vehicles, part types and distinct sets of qualifiers. With 500,000 retired
  numbers more than the fixture's, `vocab compile` peaked as without them.
- **Skipped drift follows the month.** A compile records `model-years` as skipped
  when its month knows no model years, and `model-qualifier` when it knows no
  model values, so the browser's compile records both without being told.
  `/v1/info`'s `compiled` says so as `skipped_drift`.

### The browser half

Measured on the catalog-scale fixture (2026-11-12,
[#263](https://github.com/joshuasprow/nitrovin/issues/263)) on an Apple M5 Pro,
each browser in a fresh persistent profile: the catalog picked in the
Vocabularies dialog and compiled there, the page reloaded, then BenchmarkFit's
64 fitments asked of the fit page through the Service Worker, each kind three
times (192 answers a kind), and [recorded on #260](https://github.com/joshuasprow/nitrovin/issues/260).
`fit` is the module's own time for the request (`Server-Timing: fit`), the
page drawn around it apart; Firefox and WebKit give WebAssembly a clock of a
millisecond, so their times are whole milliseconds and "≤ 1" is below it.
Every answer, 384 in each browser, was the server's byte for byte (the server
with the same files and the catalog as JSON Lines, the clock pinned), but for
the server's link to the same request on `/v1`, which only the server answers.

`fit` p99 by kind (p50 in brackets), against ADR 0018's budgets; **bold** misses:

| Request | Chrome 157 | Firefox 160 | WebKit (for Safari) | Chromium (Playwright) | ADR 0018 |
| --- | ---: | ---: | ---: | ---: | ---: |
| candidates | 1.3 ms (0.8) | 2 ms (1) | 4 ms (1) | 1.6 ms (0.7) | 5 ms |
| part types | 1.8 ms (1.2) | 3 ms (2) | 3 ms (1) | 2.5 ms (1.1) | 10 ms |
| part types, no year | 3.5 ms (2.5) | **15 ms** (3) | **11 ms** (2) | **11.4 ms** (2.3) | 10 ms |
| related vehicles | 2.7 ms (1.3) | 4 ms (2) | 3 ms (1) | 2.3 ms (1.2) | 50 ms |
| related vehicles, no year | 7.1 ms (3.2) | 10 ms (4) | 6 ms (3) | 6.6 ms (3.0) | 50 ms |
| reverse, one key | 0.9 ms (0.4) | **11 ms** (1) | **7 ms** (1) | **8.4 ms** (0.4) | 1 ms |

| | Chrome 157 | Firefox 160 | WebKit | Chromium | Budget |
| --- | ---: | ---: | ---: | ---: | ---: |
| Catalog open (in place) | 16 ms | 21 ms | 131 ms | 14 ms | |
| Retained: Go heap / WebAssembly memory | 30 / 63.1 MiB | 30 / **64.6 MiB** | 30 / 63.1 MiB | 30 / **64.6 MiB** | 64 MiB |
| Compile (330 MB source) | 15.5 s | 21.0 s | 15.7 s | 14.4 s | recorded |
| Compile peak (WebAssembly memory) | 189 MiB | 191 MiB | 191 MiB | 191 MiB | 256 MiB |
| Quota: kept / peak while compiling | 396 / 988 MB | 1,114 / 1,124 MB | not counted | 396 / 783 MB | |

**The browser half misses, and the work stops here for a person to decide.**
The medians are a millisecond or two, and the slow answers are single:
the p99 of part types with no year and of reverse is one or two answers in
192 taking 7–15 ms, in every browser but Chrome, consistent with a
stop-the-world collection of Go's heap, which in WebAssembly has one thread.
Retained WebAssembly memory is 63.1–64.6 MiB, at the budget's edge: the module
holds a 30 MiB heap, and Go's collector grows its memory to about twice that.

- **Open:** the module starts in 45–65 ms; the stored catalog opens in place in
  13–29 ms (its dictionary, one read of a few MB); the cold open of everything,
  spine bundle and the server's files included, is under 0.4 s.
- **Retained:** after the 1,152 answers, the Go heap in use is 30 MiB and the
  WebAssembly memory the module took 63.1–64.6 MiB.
- **Compile:** 14–21 s for the 330 MB source, its peak the WebAssembly memory
  the compile worker took, 189–191 MiB, within 256 MiB.
- **Quota:** the compiled file keeps 342.5 MB, about 396 MB of the site's
  storage with the rest; while it compiles, its scratch storage takes as much
  again and more, about 1 GB at the peak in Chrome. Firefox's estimate stays
  at its peak until it compacts, and WebKit's counts no Blobs at all.
- **Safari is unmeasured.** Safari 27.2 would not start a WebDriver session
  on the reference machine (it timed out connecting, with remote automation
  allowed and `safaridriver --enable` run), so WebKit (Playwright 1.62's)
  stands in for it. WebKit stores no Blob in IndexedDB in an ephemeral
  profile, as Safari's private browsing is: a compile there fails in words,
  storing nothing.

### A Service Worker's lifetime

**Confirmed: a browser may stop a Service Worker during a long event.** A
message event whose `waitUntil` settles after ten minutes, the worker telling
its page every 5 s how long it had run: Chrome 157 stopped the worker at
5 minutes (its last word at 295 s), Firefox 160 at about a minute (55 s), and
WebKit did not stop it in ten. So the compile runs in the dedicated worker the
page starts, as decided, and a Service Worker's events stay short.

### What was built in the browser

- **The compile:** `compile.js`, the dedicated worker, runs `sw.wasm`'s
  `compile`, which opens the browser's files as the Service Worker does and
  calls the streaming compiler with the release's spine bundle as its month.
  The picked `File` is read 4 MiB at a time by `slice().arrayBuffer()`.
- **IndexedDB keeps files in parts.** Scratch files and the compiled file are
  written in Blobs of a MiB in a database of their own, `nitrovin-catalog`
  (so an older release still opens `nitrovin`), and each is read back as one
  Blob made of its parts, so nothing copies them. A compile writes its
  catalog's parts under a key of its own and commits them in one transaction,
  which drops the parts of any catalog it replaces; a compile that stopped
  half way leaves parts no record keeps, which the next compile drops. A
  refused quota aborts it, and the parts written go.
- **One compile at a time:** a Web Lock; another tab's is refused in words.
- **Stale is more than unopenable.** A stored catalog whose spine or vehicle
  vocabulary as loaded is another file than the one it was compiled against
  (its blake3) is left out with the reason, though Link would accept it: the
  server measures again what it can, the browser could not. Removing the
  vehicle vocabulary it links to is refused unless the catalog goes too;
  replacing it with another version leaves the catalog listed, stale.
- **The server's catalog is named, never published:** `/sw.js` carries its
  name, so the browser refuses a catalog of its own while the server has one,
  and the worker sends the fit page to the server.

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
