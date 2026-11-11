# Browser vocabularies: the same Go handlers in a Service Worker, behind htmx

A visitor to nitrovin.app, or to any `nitrovin serve`, can load vocabularies
into their own browser and translate there, and the files never leave it
([Specify /vocab and the spine bundle](https://github.com/joshuasprow/nitrovin/issues/148),
on [the vocabulary map](https://github.com/joshuasprow/nitrovin/issues/117),
after [the browser prototype](https://github.com/joshuasprow/nitrovin/issues/147)
and its first review).

The UI is htmx over `/ui/*` routes outside the `/v1` contract (not in
`openapi.yaml`, free to change). Each route answers a fragment when the
request carries `HX-Request` and a full page when it doesn't, so every page
works without JavaScript and htmx only enhances links and forms that already
work. `nitrovin serve` renders the routes from Go `html/template`. For files
held in the browser, a Service Worker runs the same handlers, built from the
same Go code to WebAssembly with Go's toolchain, over an IndexedDB store. A
reader might expect a JavaScript single-page app here, or a server that takes
uploads. The first would mean a second translator to keep in step with
`pkg/vocab`. The second would put private or licensed files (Snyder's
mappings, a user's eBay data) on a public server. TinyGo was measured and
cannot run either template engine: they need reflection it lacks at run time.

**One URL, one meaning.** The catalog is the server's loaded files plus the
browser's, each marked with where it lives, in one namespace (ADR 0011). The
worker answers a `/ui` request only when it names a browser file, and forwards
everything else to the server unchanged. So a link that names only server
files means the same with JavaScript on or off, and in any browser. A link that
names a browser file says where that file lives when it is opened elsewhere. To
translate between the two kinds, the worker links the server's files, which
`serve` publishes at release-versioned URLs. Nothing travels from the browser
to the server except `/v1/decode` for a VIN.

**One translator.** The translation core is decoder-free and lives in
`pkg/vocab`: it takes a `vocab.Month` and a decode's items, or none.
`pkg/nitrovin`'s `Translator` wraps it with the decoder; the worker wraps it
with `/v1/decode` and a **spine bundle**, the month's vPIC data `vocab.Month`
needs (JSON, every model, at most 1 MB brotli). Worker answers carry the
`decoder_version` and `data_month` of the decode they used, and refuse when the
bundle's month and the decode's month differ.

**Release-versioned assets.** `sw.wasm`, `wasm_exec.js`, htmx (vendored, one
pinned file), the spine bundle and published server files live under
`/ui/r/<release>/`, where the release is the Worker's cache key
(`decoder_version/data_month/artifact_hash`), served `immutable` for a year.
Builds are reproducible (`-buildvcs=false`). `/sw.js` alone is `no-cache`, and
it alone carries `'wasm-unsafe-eval'`; every page stays `script-src 'self'`.

**Cold start under 1 s.** Browsers stop idle workers, and the prototype spent
about 5 s re-parsing a 150k-entry file on each start. The store keeps each
file's parsed index beside its text, keyed by the file's blake3 and the
release, and rebuilds it only when either changes.

**Storage.** Picking several files links them as one batch. A file with an
existing name replaces it if the batch still links. Removing a file others
depend on is refused visibly, naming them, with "remove all of them" offered.
A stored file that a newer release refuses is listed with its fault and can be
removed, and is left out of linking. The page asks for persistent storage on
the first load and shows usage.

**Lifecycle and errors.** A page the worker does not control (after a hard
reload) says browser vocabularies are paused for that load. A new release's
worker waits until the old one's tabs close, and the page says an update is
ready. A load with no progress for 60 s fails and stores nothing. Errors carry
real status codes; htmx's `responseHandling` swaps 4xx fragments into an error
slot. One full-page error template serves every error, unknown paths
included, and messages name files and say what went wrong in words.

**The decode form moves onto `/ui/decode`**, `/` staying the entry page and
old `/?vin=` links redirecting. Its JavaScript-off test stays in a fresh
profile, since an installed worker survives JavaScript being disabled. A
JavaScript-on run asserts `HX-Request`, and an axe audit runs in both.

## Considered options

- Translation on the server with per-request uploads: private data on a
  public host, CPU on a 1/16-vCPU container.
- A TypeScript translator in the page: a second engine.
- TinyGo: can't run the templates.
- With JavaScript on, browser files only: the same link means two things.
- Re-parsing on every cold start: ~5 s at 150k entries.
- Updating the worker immediately (`skipWaiting`): swaps code under an open
  page.
- Stable asset URLs with ETags: every cold start revalidates the 3.3 MB
  module.

## Consequences

- `pkg/vocab` keeps building for `js/wasm` and never imports the artifact,
  the decoder or `pkg/contract` (ADR 0011).
- The decode form's script-free guarantee becomes progressive enhancement:
  everything still works without JavaScript; only browser-held vocabularies
  need it, and say so.
- Every release publishes a spine bundle, and the Worker caches `/ui/r/…`
  under the release key.

## Amendment (2026-11-11): one page

**`/` is the one page.** Its controls are tabs, one for each thing a
translation can be from: a VIN, a vehicle vocabulary's entry, each declared
spine's entry, a vPIC make or model id. The decode form no longer moves onto
`/ui/decode`: `/ui/decode`, `/ui/translate`, `/vocab`, `/ui/vocabularies`
and `/?vin=` answer `301` to `/`, their query along (a VIN becomes
`from.vin`). Vocabularies is a dialog on the page, and on each entry list
(`/ui/vocabularies/{name}`). The answer is a map: what it is from, the spine
entries it crosses and the entries it reaches as cards in columns, each
mapping a line, dashed when matched by rule. The server draws the map, so
it needs no script.

**The worker rule is unchanged.** The worker answers only a request that
names a browser-held file (its `from.vocabulary`, its spine or a `to`);
everything else goes to the server, so a bare `/?from.vin=…` is the
server's answer and means the same in every browser. The visitor's
selection is ticked into the forms and travels as an explicit `to=`, never
implied. The worker has no decoder and no decode page of its own: it
renders a VIN's decode from `/v1/decode`, which is still all that travels
from the browser to the server.
