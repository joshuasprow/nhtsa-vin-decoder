# Translation and listing are requests beside decode

Loaded vocabularies (ADR 0009–0011) are reached through two requests, each
the same object on every surface as decode's is
([Translation contract](https://github.com/joshuasprow/nitrovin/issues/123),
on [the vocabulary map](https://github.com/joshuasprow/nitrovin/issues/117)).
All of it is additive under `/v1` (ADR 0004, ADR 0007): decode's request,
answer and defaults do not change.

**translate** takes `from` (a VIN; a vocabulary entry,
`{"vocabulary", "entry": {…}}`; or a spine entry written as a mapping target
is: `{"make"}`, `{"model"}`, `{"ext"}`, `{"spine", "id"}`), and optionally
`to` (target vocabulary names), `model_year`, a context `vin` (a decode for
qualifiers and the year when `from` is an entry) and `now`. VIN → entries,
entry → spine, spine → entries and entry → entry are all this one request;
without `to` it answers the spine alone. Without a model year, year-scoped
mappings stay candidates and carry their ranges. A VIN's decoded year wins
over `model_year`, as decode itself decides between a VIN and a hint.

The answer always shows the route. It has `from` with a `status`: `mapped`,
`unmapped` (listed without mappings) or `unknown` (not in the vocabulary).
It has `vehicle` (the decode's make id, model id and year) when a VIN was
given, never the whole decode. Then `spine`: the spine entries reached, named
from the data month or the spine file. A spine entry the month lacks is
served with `"drift": true` and no name. Then `to`: per target vocabulary, a
flat, deduplicated list of entries, each with `via` (the spine entries that
led to it), its mapping's `origin` and `note`, and `unconfirmed`. Then
`vocabularies` (name and version of each file used), `now`, `data_month` and
`decoder_version`.

`unconfirmed` lists the qualifier element ids, and `"model_year"`, that
applied but could not be checked: no decode, or an element the decode left
empty (ADR 0009: unknown keeps). It is absent when everything was checked, so
a client can keep only fully confirmed results. Nothing to translate is never
an error, just as a VIN never is: an unknown or unmapped entry, or a target
with no entries, answers with a status and empty lists. Only a malformed
request is refused, as `#invalid-request` with a pointer: an unknown
vocabulary, entry fields other than the vocabulary's `fields`, or `vin` given
both as `from` and as context.

**list** with no `of` is the catalog of what is loaded: vPIC, then each
vocabulary, extension set and spine with its kind, version, subject, fields
and spine. With `of` it pages through one of them: a vocabulary's entries
with their mappings as the file has them, vPIC's makes (`of: "vpic"`) or one
make's models (`where: {"make": 476}`), a spine's entries, or a set's
extensions. `where` matches fields exactly after the fixed normalization. The
order is by normalized fields and is stable for a given set of loaded files.
`limit` defaults to 100 and may be up to 1,000, and `after` takes the
answer's `next` cursor. vPIC models get no year filter, because vPIC records
no year span per model. `vpic` is therefore a reserved file name (ADR 0011,
amended).

Each surface carries both requests:

| Surface | translate | list |
| --- | --- | --- |
| HTTP | `POST /v1/translate` (array, ≤ 1,000 items, ≤ 1 MiB, answers in order); `GET /v1/translate` with the request's paths as dotted parameters (`from.vocabulary`, `from.entry.make`, `to` repeated…) | `GET /v1/vocabularies` (the catalog); `GET /v1/vocabularies/{name}/entries?where.<field>=…&limit=…&after=…` |
| stdio, C | a `StdioRequest`'s new optional `op`: `decode` (default), `translate`, `list`; replies keyed `decode`, `translation`, `list`. C answers every op through `nitrovin_decode` and gains `nitrovin_open_vocab(artifact_path, vocab_paths, n, err)` | same |
| CLI | `nitrovin translate --to a,b [--model-year N] <vin>…`, or `-` for JSON request lines | `nitrovin list [of] [--where field=value]… [--limit N] [--after cursor]` |
| MCP | tool `translate`, always listed | tool `list`, always listed |
| Go | `pkg/vocab` reads, validates and indexes files; `nitrovin.NewTranslator(d, files…)` gives an immutable `*Translator` with `Translate`, `List` and `Info`; types in `pkg/contract` | same |

`/v1/info` (and `nitrovin_info`, `nitrovin info`) gains `vocabularies`: each
file's name, kind, version, blake3 hash, path, and drift counts with examples.
`nitrovin vocab check <file|dir>…` prints findings as JSON lines
(`{"file", "line", "severity": "fault"|"drift", "detail"}`) and exits 1 on
any. The MCP tools' descriptions are static, as `decode_vin`'s is (ADR 0005),
and tell an assistant to call `list` without arguments to discover what is
loaded, so `tools/list` never depends on flags. With nothing loaded, which is
nitrovin.app's case, `list` returns vPIC alone and `translate` still resolves
a VIN or a vPIC id to the spine.

## Considered options

- An operation per direction (resolve, reverse, translate): three request
  shapes for what one `from`/`to` pair says.
- Nesting target entries under each spine entry: the route is plainer, but a
  target reached two ways appears twice.
- An unknown entry as a refusal: makes "not in this vocabulary", a normal
  answer, look like a client mistake.
- A boolean `confirmed`: hides which condition went unchecked.
- `POST /v1/list` beside the GETs: a read that is never batched needs no body.
- `/v1/vpic/makes/{id}/models` aliases: a second spelling of
  `/v1/vocabularies/vpic/entries?where.make=…`.
- A general `nitrovin_request` in C: `nitrovin_decode` already takes any
  `StdioRequest`, and the ABI is frozen within v1.
- Listing the MCP tools only when vocabularies are loaded: `tools/list` would
  then depend on flags.
- Methods on `Decoder`: it stays vocabulary-free (ADR 0011).

## Consequences

- The contract documents (`docs/api/README.md`, `openapi.yaml`,
  `decode.schema.json`'s `$defs`, `mcp-tools.json`) change with the code that
  serves them, because tests bind them (`TestOpenAPIBundle`, the
  `mcp-tools.json` copy check).
- `nitrovin_decode` now answers more than decodes. Its name stays and the
  contract says so.
- Once v1.0.0 is tagged with these routes, every field above is frozen for
  `/v1`.
