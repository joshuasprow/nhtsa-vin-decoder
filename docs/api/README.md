# The nitrovin API contract (v1)

Every surface decodes through one contract. This directory holds it:

| File | What it fixes |
| --- | --- |
| [`decode.schema.json`](decode.schema.json) | JSON Schema 2020-12 for every wire type: the decode, requests, stdio lines, problems, info, translations and lists |
| [`openapi.yaml`](openapi.yaml) | OpenAPI 3.1 for `nitrovin serve`, referencing the schema |
| [`mcp-tools.json`](mcp-tools.json) | The tools `nitrovin mcp` lists: `decode_vin`, the description an assistant reads, its bundled schemas |
| [`vocabulary.schema.json`](vocabulary.schema.json) | One line of a vocabulary, extension set or declared spine file, as `--vocab` loads it |
| this file | what none of them can express: layouts, item order, the clock, stdio framing, MCP results, the CLI mapping, loading vocabularies, translating and listing, the C ABI, versioning |

Words are `GLOSSARY.md`'s. The why behind the surprising choices is
[ADR 0004](../adr/0004-decode-contract.md).

## The decode

A decode is a header, the attributes in one of two layouts, and on request the
items.

| Field | Type | Meaning |
| --- | --- | --- |
| `vin` | string | The VIN as decoded: trimmed, uppercased, non-ASCII → `&` |
| `wmi` | string | Positions 1-3, plus 12-14 when position 3 is `9` |
| `model_year` | int \| null | Element 29 of the winning pass |
| `error_codes` | int[] | Element 143, ascending; `[0]` is clean |
| `check_digit_valid` | bool | ≥ 17 characters and position 9 equals the computed digit |
| `corrected_vin` | string \| null | Element 142; null when empty |
| `model_year_hint` | int \| null | The request's `model_year`, echoed |
| `now` | string | The clock used, UTC whole seconds |
| `data_month` | string | `YYYY_MM` of the loaded artifact |
| `decoder_version` | string | Module version, `v1.2.0`; `(devel)` untagged |
| `shape` | `vpic` \| `nitrovin` | Layout of `attributes` |
| `attributes` | object | The decoded vehicle |
| `items` | Item[] | Only with `full` |

Every value is the oracle's exact text. Numbers are never retyped:
`Displacement (CC)` is `"2000.0"` on one VIN and `"2000.00"` on another, and
parity is byte-for-byte. `data_type` in the info element table says which
elements parse as numbers.

The Corrections elements (142 Suggested VIN, 143 Error Code, 144 Possible
Values, 156 Additional Error Text, 191 Error Text, 196 Vehicle Descriptor)
stay in `attributes` as vPIC emits them. The header fields are their typed
reading, not a replacement.

### Reproducibility

A decode is a function of five things, and carries all five:

```
(vin, model_year_hint, now) + (data_month, decoder_version)
```

`data_month` plus `decoder_version` identify the artifact only for released
builds; `/v1/info`'s `artifact_hash` identifies it exactly. Decode again with
the same `now` and the same build and the bytes are identical. `now` decides
which WMIs are published (`PublicAvailabilityDate <= now`) and the newest model
year a VIN can decode to (`now`'s year + 2).

### Layout `vpic` (the default)

vPIC's `DecodeVinValues` layout, derived from the data month:

- One key per public element (141 in 2026_10), named by its element code
  (`BodyClass`, `ErrorCode`, `CAN_AACN`), always present, `""` when undecoded.
- Plus `MakeID`, `ManufacturerId` and `ModelID` (vPIC's casing): the attribute
  ids of Make, Manufacturer Name and Model, `""` when undecoded.
- A multi-valued element's distinct values, joined with `", "` in byte order.

It differs from live `DecodeVinValues` in three known ways: no `VIN` key (the
header has `vin`), `PlantAddress` is present, and vPIC's ten always-empty
legacy keys (`NCSAMake`, `NCSAModel`, `NCSABodyType`, `NCSANote`,
`NCSAMapExcApprovedBy`, `NCSAMapExcApprovedOn`, `NCSAMappingException`,
`CashForClunkers`, `DriverAssist`, `AdaptiveHeadlights`) are absent. vPIC
joins multi-valued elements in its own unstable row order; nitrovin's byte
order can differ from it on the same set.

### Layout `nitrovin`

Keyed by variable name. Only decoded elements are present.

```json
"Make": { "element_id": 26, "value": "HONDA", "attribute_id": 474 },
"Model Year": { "element_id": 29, "value": "2003" },
"Note": { "element_id": 114, "values": ["Curb Weight (lbs): 2840-2885", "Manual Transmission / CVT"] }
```

- `attribute_id` is present only on lookup elements whose id is an integer,
  so never on Error Code (143), whose "id" is the code list.
- A multi-valued element always carries `values`, never `value`, however many
  it has. An element is multi-valued when the procedure can emit it more than
  once for one VIN: the elements its dedup keeps every row of, and Other
  Battery Info (1), which its Vehicle Specs step adds even when a pattern
  already set it. Which elements are multi-valued is fixed per data month and
  listed in `/v1/info` (2026_10: 1, 114, 121, 129, 150, 154, 155, 169).
  `values` keeps duplicates (one VIN carries `Seat Belt (Rr center position)`
  four times, from four patterns) in byte order.

### Items (`full`)

`items` is every row the decoder emitted for the VIN, all 15 of
`spvindecode`'s columns in snake_case, SQL NULL as `null`:

| Item field | `spvindecode` column | Type |
| --- | --- | --- |
| `group_name` | `groupname` | string \| null |
| `variable` | `variable` | string |
| `value` | `value` | string \| null |
| `pattern_id` | `itempatternid` | int \| null |
| `vin_schema_id` | `itemvinschemaid` | int \| null |
| `keys` | `itemkeys` | string \| null |
| `element_id` | `itemelementid` | int |
| `attribute_id` | `itemattributeid` | string \| null (a lookup id, a code list, or the raw attribute) |
| `created_on` | `itemcreatedon` | string \| null, as Postgres renders a timestamp (`2015-04-21 11:37:26.607`) |
| `wmi_id` | `itemwmiid` | int \| null |
| `code` | `code` | string \| null |
| `data_type` | `datatype` | string \| null |
| `decode` | `decode` | string \| null |
| `source` | `itemsource` | string \| null, verbatim (`Conversion 2: 183 * 16.387064 `) |
| `to_be_qced` | `itemtobeqced` | bool \| null |

**Item order** is total and independent of the procedure's (which is not
stable): `element_id` ascending, then `value`, then the remaining fields in
the order of the table above. Strings compare bytewise, integers numerically,
`false` before `true`, and `null` before any value.

Parity is gated on items: they are the oracle's answer, compared as a multiset
per VIN. Both layouts are pure functions of the items. The `vpic` layout has no
oracle (NHTSA's flattening lives in its web service, not in the dump), so it is
unit-tested and smoke-checked against live `DecodeVinValues` on the sample VINs
in each refresh, reported but never gating.

## Requests

One request object everywhere: an element of the `POST /v1/decode` array, a
GET's path and query, a stdio line, `nitrovin_decode`'s argument.

| Field | Type | Default | Rule |
| --- | --- | --- | --- |
| `vin` | string | required | 1-17 characters after trimming spaces: `spvindecode` holds the trimmed VIN in a `varchar(17)` and raises on a longer one, so a longer string has no answer. Every string within it decodes |
| `model_year` | int \| null | none | The model-year hint; any 32-bit integer, as the oracle takes |
| `full` | bool | `false` | Add `items` |
| `shape` | `vpic` \| `nitrovin` | `vpic` | Layout of `attributes` |
| `now` | string | wall clock | RFC 3339 instant, or `YYYY-MM-DD` meaning 00:00:00Z; truncated to whole seconds |

Unknown fields are refused, so a client never silently loses an option an older
server does not know. On GET, `full` is `true` or `false`, and an unknown query
parameter is refused the same way.

**The clock** is read once per request when `now` is absent: once per CLI
invocation, once per HTTP request (one reading for a whole batch), once per
stdio line, once per `nitrovin_decode` call. A CLI run over millions of VINs
that crosses midnight on 31 December keeps its starting instant.

## HTTP

Routes, bodies and status codes are in [`openapi.yaml`](openapi.yaml):
`GET /v1/decode/{vin}`, `POST /v1/decode` (≤ 1,000 items, ≤ 1 MiB),
`GET` and `POST /v1/translate`, `GET /v1/vocabularies`,
`GET /v1/vocabularies/{name}/entries`, `GET /v1/info`,
`GET /v1/openapi.json`, `GET /healthz`, and `POST /mcp`, the MCP server
below over Streamable HTTP. `GET /` (`HEAD` too) and `/ui/…` are pages for
browsers (HTML, a fragment for htmx, ADR 0013): `/` the entry page,
`/ui/decode` the decode form, which `/?vin=…` redirects to (`301`), and
their assets under `/ui/r/<release>/`; all are outside the contract, not
in `openapi.yaml`, and may change.

**Translate and list** ([Translation](#translation-translate),
[Listing](#listing-list)) carry the same request objects:

- `POST /v1/translate` takes a JSON array of `TranslateRequest` (≤ 1,000
  items, ≤ 1 MiB) and answers an array of `Translation` in the same order,
  reading the clock once, as `POST /v1/decode` does.
- `GET /v1/translate` takes one request as query parameters named by its
  paths with dots: `from.vin`; `from.vocabulary` with one
  `from.entry.<field>` per field; `from.make`, `from.model`, `from.ext`, or
  `from.spine` with `from.id`; `to` repeated; `model_year`, `vin`, `now`.
  The parameters are read as the JSON request they spell, so a GET answers
  exactly as the equivalent `POST` item, refusals and their `pointer`
  (`/from/entry/model`) included.
- `GET /v1/vocabularies` is the catalog (`list` with no `of`), and takes no
  parameter. `GET /v1/vocabularies/{name}/entries` is `list` with `of` the
  name (`vpic` included): `where.<field>` per field (`where.make=476`),
  `limit`, `after`.
- Every parameter but `to` is given at most once, and an unknown one is
  refused, as on `GET /v1/decode/{vin}`.

**CORS** is off unless `serve --cors <origin>` (or `*`) turns it on. Then
every response under `/v1/` and on `/mcp`, refusals included, carries
`Access-Control-Allow-Origin: <origin>`, and `OPTIONS` there is a preflight:
`204` with `Access-Control-Allow-Methods` (the route's methods),
`Access-Control-Allow-Headers` (`Accept, Content-Type, Last-Event-ID,
Mcp-Protocol-Version, Mcp-Session-Id`) and `Access-Control-Max-Age: 86400`.
`/`, `/ui/…` and `/healthz` never carry CORS headers.

A VIN is never an error: whatever is wrong with it is in `error_codes`, and the
status is 200. A refusal is an RFC 9457 `application/problem+json` body whose
`type` is one of these, `pointer` naming the offending value when there is one:

| `type` (fragment of this file's URL) | Status | When |
| --- | --- | --- |
| <a id="malformed-json"></a>`#malformed-json` | 400 | The body is not JSON |
| <a id="invalid-request"></a>`#invalid-request` | 400 | Valid JSON that breaks the request schema: wrong type, unknown field or query parameter, empty or over-long VIN, bad `model_year`, `full`, `shape` or `now`; an empty batch. For translate and list, also what the loaded files refuse (below) |
| <a id="too-large"></a>`#too-large` | 413 | More than 1,000 items, or a body over 1 MiB |
| <a id="not-found"></a>`#not-found` | 404 | No such route |
| <a id="method-not-allowed"></a>`#method-not-allowed` | 405 | The route exists; the method does not (`Allow` lists the methods) |
| <a id="internal"></a>`#internal` | 500 | A fault in nitrovin |

The full URI is
`https://github.com/joshuasprow/nitrovin/blob/main/docs/api/README.md#<fragment>`.

## stdio (`nitrovin stdio`)

JSON lines over stdin and stdout, UTF-8, `\n`-terminated.

- Each input line is a `StdioRequest`: an optional `op`, `decode` (the
  default), `translate` or `list` (ADR 0012), and an optional `id` (any JSON
  value), beside that op's request object: a decode request, a
  `TranslateRequest` or a `ListRequest`. A field the op's request lacks is
  refused, as everywhere; another `op` is `#invalid-request` at `/op`. A
  line without `op` is a decode request, so a session of decode lines gets
  the bytes it always has.
- Each input line gets exactly one output line, in input order, flushed per
  line, keyed by its op: `{"id": …, "decode": {…}}`,
  `{"id": …, "translation": {…}}` or `{"id": …, "list": {…}}`, or
  `{"id": …, "error": <problem>}`. `id` is present when the request had one,
  or could be read from a refused line.
- Translations and lists answer from the files `--vocab` or
  `NITROVIN_VOCAB` loads with the data, as for every command; with none,
  from vPIC alone.
- A malformed or refused line gets an error line, and the session continues.
- Blank lines (nothing but spaces, tabs and a `\r`) are skipped and get no reply.
- EOF on stdin ends the session after the last reply; exit 0. Exit 1 only when
  reading stdin or writing stdout fails (or, as for every command, no data loads).

The problem objects are HTTP's, with the same `type` URIs and `status`.

## MCP (`nitrovin mcp`, `POST /mcp`)

An MCP server with three tools over stdio (`nitrovin mcp`) and over
Streamable HTTP (`nitrovin serve`'s `/mcp`): `decode_vin` decodes,
`translate` translates ([Translation](#translation-translate)) and `list`
lists ([Listing](#listing-list)). Their definitions (name, title, the
description an assistant reads, schemas and annotations) are
[`mcp-tools.json`](mcp-tools.json), whose `tools` array is exactly what
`tools/list` returns on both, whatever `--vocab` loads. The why is
[ADR 0005](../adr/0005-mcp-tool-is-the-decode-contract.md).

- **Streamable HTTP is stateless**: every POST is a session of its own, no
  `Mcp-Session-Id` is issued or read, and every response is one
  `application/json` message, never an event stream (`GET` and `DELETE`
  are 405). Any instance answers any request.
- **Arguments** are one request object, with the same fields, defaults and
  refusal of unknown fields as everywhere else: a `DecodeRequest`, a
  `TranslateRequest` or a `ListRequest`. One VIN, translation or page per
  call.
- **Schemas** are self-contained, because MCP forbids fetching a `$ref` over
  the network. Each is `{"type": "object", "$ref": …, "$defs": …}`, and every
  entry in `$defs` equals the same-named definition in
  [`decode.schema.json`](decode.schema.json): `decode_vin` bundles
  `DecodeRequest` and `Decode`, `translate` `TranslateRequest` and
  `Translation`, `list` `ListRequest` and `List`.
- **An answer** (a decode, translation or list) is the result's
  `structuredContent`, and the same JSON, compact, is its one text block. Clients differ in which of the two they show
  the model, so the two must stay identical.
- **A refusal** is a result with `isError: true` and one text block: the
  Problem as compact JSON, with stdio's `type` URIs and `status`, and `pointer`
  into the arguments (`/vin`, `/to/0`). It has no `structuredContent`. JSON-RPC errors
  are only for MCP faults, such as an unknown tool or a malformed request.
- **Annotations:** read-only, not destructive, idempotent, closed-world.
- **The descriptions are static.** They name neither the data month nor
  the loaded vocabularies, so `--data` and `--vocab` never change
  `tools/list`; every answer carries `data_month`, and `translate`'s and
  `list`'s descriptions tell the assistant to call `list` with no arguments
  to discover what is loaded. Without `--vocab` both tools are still listed
  and answer for vPIC alone.
- **Protocol revisions** are whatever the official Go SDK serves, not part of
  this contract.
- stdout carries only MCP messages; diagnostics go to stderr.

## CLI (`nitrovin decode`)

| Flag | Request field |
| --- | --- |
| `--model-year N` | `model_year`, for every VIN without its own |
| `--full` | `full` |
| `--shape vpic\|nitrovin` | `shape` |
| `--now <instant\|date>` | `now` |

`nitrovin decode <vin>…` decodes its arguments; `nitrovin decode -` reads one
VIN per stdin line, optionally followed by a comma or a tab and that VIN's
model year (`1HGCM82633A004352,2003`). A line's own year beats
`--model-year`; an empty one (`1HGCM82633A004352,`) is none. A trailing `\r`
is dropped, and blank lines are skipped but counted. One argument VIN prints
one decode (pretty-printed on a terminal, one compact line elsewhere); several,
or `-`, print JSON lines in input order, each the decode's compact JSON
exactly as the Go module marshals it.

- A refused argument VIN: a problem on stderr, nothing on stdout, exit 2.
  Arguments are checked before any data is loaded.
- A refused stdin line: a problem on stderr naming the line, the run stops,
  exit 1. Lines already printed are complete, valid decodes.
- A problem on stderr is one line of compact JSON, stdio's. Its `detail`
  starts with the input it is about (`argument 2: `, `line 7: `), and its
  `pointer` points into that request (`/vin`, `/model_year`).
- Data that will not load (a `--data` or `NITROVIN_DATA` file that will not
  open): an `#internal` problem naming the flag or variable and the file,
  exit 1. With neither, the binary's embedded artifact is the data.
- A mistake in the command line itself (unknown command or flag, a bad flag
  value, `-` beside other VINs, `--full` with `-o text`): a message and the
  usage on stderr, as text, nothing on stdout, exit 2.
- `-o text` ignores `--shape`: one block per VIN, blank-line separated, of
  two aligned columns: a `VIN` row with the decode's `vin`, then variable name
  and value per row, by element id, one row per value of a multi-valued
  element, non-empty attributes only. `--full` has no text form.

`--data` is accepted before the command or after it; after wins. `--vocab`
is accepted in both places too, and every use adds to the files loaded.
`nitrovin info` prints the `/v1/info` object, pretty-printed on a terminal.

### `nitrovin translate`

| Flag | Request field |
| --- | --- |
| `--to a,b` | `to`, comma-separated; repeatable, each use appending |
| `--model-year N` | `model_year` |
| `--now <instant\|date>` | `now` |

`nitrovin translate <vin>…` translates each argument VIN, as `from`, through
the spine to the `--to` vocabularies; without `--to` it answers the spine
alone. `nitrovin translate -` reads one `TranslateRequest` object per stdin
line (a VIN, an entry or a spine entry as `from`), and the flags fill what a
line leaves out: a line's own `to`, `model_year` and `now` beat the flags',
and a line without one (an empty `to`, a null `model_year`) takes the
flag's. Output follows `decode`'s: one argument prints one `Translation`
(pretty-printed on a terminal, one compact line elsewhere); several, or `-`,
print JSON lines in input order, each the answer's compact JSON exactly as
the Go module marshals it. A trailing `\r` is dropped, and blank lines
(nothing but spaces and tabs) are skipped but counted.

- A refused argument: a problem on stderr naming the argument, nothing on
  stdout, exit 2. A VIN is checked before any data is loaded; a `--to`
  the loaded files refuse (a name nothing loaded has, a target on another
  spine) after, and before anything is printed.
- A refused stdin line (not JSON, or a request `translate` refuses): a
  problem on stderr naming the line, the run stops, exit 1. Lines already
  printed are complete, valid answers.
- Problems, unloadable data and vocabularies, and mistakes on the command
  line (a `--to` with an empty name among others) are as for `decode`.

### `nitrovin list`

| Argument or flag | Request field |
| --- | --- |
| `[of]`, at most one | `of`; none asks for the catalog |
| `--where field=value` | `where`, repeatable, a field once (`--where make=476` for `vpic`) |
| `--limit N` | `limit`, 1 to 1,000 |
| `--after <cursor>` | `after`, the previous page's `next` |

`nitrovin list [of]` prints one `List`, pretty-printed on a terminal and one
compact line elsewhere, exactly as the Go module marshals it. A request the
loaded files refuse (an `of` nothing loaded has, a `where` field the
listing lacks, `--limit` or `--after` beside the catalog, an `after` that is
not a cursor of the listing) is a problem on stderr, nothing on stdout,
exit 2. A malformed flag (`--where` without `=`, a field given twice, a
`--limit` outside 1 to 1,000) or a second argument is a usage error, exit 2.

## Vocabularies (`--vocab`)

Vocabularies, extension sets and declared spines (ADR 0009, 0010, 0011) are
JSON Lines files: a header, then one line per entry, extension or spine
entry, each as [`vocabulary.schema.json`](vocabulary.schema.json) specifies.

- **Loading.** `--vocab <file|dir>` names them, on the root or any command,
  repeatable; a directory loads every `*.jsonl` directly in it, in name
  order. Without `--vocab`, `NITROVIN_VOCAB` is a path list (`:`-separated,
  `;` on Windows); flags replace it, as `--data` replaces `NITROVIN_DATA`.
  Files load once, with the data, and never change for the life of the
  process: `serve` picks up new files on restart.
- **Matching.** An entry's fields match after trimming, collapsing every
  run of white space to one space and Unicode lower-casing, nothing more.
  Qualifier values match exactly.
- **Faults refuse.** A fault stops every command before it answers: exit 1
  and an `#internal` problem whose `detail` names the flag or variable, the
  file and line, and a JSON Pointer into the line where there is one, then
  counts any further faults (`--vocab: /srv/yard.jsonl:7: /mappings/0:
  first year 2014 is after last year 2012 (and 2 more)`). Faults are a file
  that cannot be read; a malformed line; an unknown format, kind, subject or
  field; a missing or ill-typed field; entry fields other than the header's;
  two entries equal after normalization, or two extensions or spine entries
  with one id; a mapping naming no spine entry, or two; a first year after
  the last; qualifiers on an extension mapping; a reference to an extension
  or spine id no loaded file defines; a part vocabulary whose spine is not
  loaded; two files with one name, or one named `vpic`.
- **Drift warns and serves.** Measured against the loaded data month: a
  vPIC make or model id it lacks; a qualifier its decode cannot satisfy (an
  element that is not one of its public elements, a value of the wrong type
  for the element, or an attribute id or text that none of the month's
  pattern, engine-model, vehicle-spec or default-value rows, nor for Make,
  Manufacturer Name and Vehicle Type its WMI tables, produce; values a
  formula pattern computes from a VIN are not known); an extension named,
  after normalization, like a vPIC make, or like a vPIC model under the same
  make. Each finding is one line on stderr at load,
  `{"file", "line", "severity": "drift", "detail"}`, and the files are
  served as written.
- **`/v1/info`** (and `nitrovin info`, `nitrovin_info`) lists every loaded
  file in `vocabularies`, in load order: `name`, `kind`, `version`, `blake3`
  (hex, of the file's bytes), `path` (absolute) and `drift`, a count per
  kind of drift the file has (`make`, `model`, `qualifier`, `extension`,
  in that order) with its first three findings as `{"line", "detail"}`. It
  is `[]` when nothing is loaded, as for every `nitrovin_open` handle.
- **`nitrovin vocab check <file|dir>…`** loads its arguments, and only
  them, beside the data (`--data` as for any command) and prints every
  finding on stdout as a JSON line,
  `{"file", "line", "severity": "fault"|"drift", "detail"}`: the faults
  when there are any, otherwise the drift. Exit 0 with no output when there
  is none, 1 when there is any. Line 0 is a file that cannot be read.

## Translation (`translate`)

One request object, `TranslateRequest` in the schema, resolves a VIN or an
entry through exactly one spine to the entries of target vocabularies
([ADR 0012](../adr/0012-translation-and-listing-are-requests-beside-decode.md),
with ADR 0009's and ADR 0010's semantics). In Go it is
`nitrovin.NewTranslator(d, files…)`'s `Translate`; the HTTP, stdio, CLI,
MCP and C surfaces carry the same object.

| Field | Type | Default | Rule |
| --- | --- | --- | --- |
| `from` | string \| object | required | A VIN (as a decode's `vin`); a vocabulary entry, `{"vocabulary", "entry": {…}}`, with exactly the vocabulary's fields; or a spine entry as a mapping target names it: `{"make"}`, `{"model"}`, `{"ext"}` or `{"spine", "id"}` |
| `to` | string[] | none | Target vocabularies, each loaded and on the same spine as `from`. None answers the spine alone |
| `model_year` | int \| null | none | The VIN's decode hint, and the year when no VIN decodes one. A VIN's decoded year wins over it |
| `vin` | string | none | A context VIN when `from` is an entry: its decode checks qualifiers and gives the year |
| `now` | string | wall clock | The VIN's decode clock, as a decode request's |

- **One spine.** A VIN, a vPIC id, an extension and a vehicle vocabulary
  route through vPIC (`vpic`); a declared spine's ids and its part
  vocabularies through that spine. A target on another spine is refused:
  nothing chains from one spine to another.
- **The spine reached.** From a VIN: its decoded model, then its make.
  From an entry: each of its mappings that holds, in file order. From a
  spine entry: itself. A vPIC model, or an extension model, also reaches
  the make it sits under, so a make vocabulary answers for a model; a make
  never reaches its models. Each spine entry appears once, named from the
  data month or its file, with `under` (its make) for a model. A vPIC id
  the data month lacks is served with `"drift": true` and no name.
- **Year scope and qualifiers** (ADR 0009). The year is the VIN's decoded
  model year, else `model_year`. A known year outside a mapping's range
  rules it out; a decoded value outside a qualifier's set does too, any
  decoded value of a multi-valued element satisfying it. An unknown year,
  no decode, or an element the decode left empty keeps the mapping, and
  what went unchecked is listed in `unconfirmed`: qualifier element ids
  ascending, then `"model_year"`. A translation from a name never
  checks a qualifier, so it returns the union of the entry's mappings.
- **`via`.** A spine entry reached from an entry carries `via`, the
  entry's mappings that reached it; a target entry carries `via`, its own
  mappings that hold, each naming the spine entry that led to it. A via is
  that spine entry plus the mapping's `first`, `last`, `origin` and `note`
  as the file has them, and `unconfirmed` for the route through it (both
  mappings' unchecked conditions). A target entry appears once per target
  vocabulary however many ways it is reached, ordered by its normalized
  fields, with its own `note`. Its `unconfirmed` is absent when one via
  checked everything, else the union of its vias', so keeping entries
  without `unconfirmed` keeps only fully confirmed results.
- **The answer** (`Translation`): `from`, echoing the request's (the VIN as
  decoded; the entry as its vocabulary writes it, with the entry's `note`)
  with `status`: `mapped`, `unmapped` (listed without mappings) or
  `unknown` (not in the vocabulary or spine, or a VIN that decodes to no
  make or model); `vehicle` (`vin`, `make`, `model`, `model_year`) when a
  VIN was given, never the whole decode; `spine`; `to`, one
  `{"vocabulary", "entries"}` per target in request order, each named once;
  `vocabularies`, the name and version of every file used, in load order;
  `now`, `data_month`, `decoder_version`.
- **Nothing to translate is never a refusal.** An unknown or unmapped
  entry, or a target with no entries, answers with a status and empty
  lists. `#invalid-request`, with a pointer into the request, is only for
  a malformed request: a `vocabulary`, `to` name, `spine` or extension set
  nothing loaded has (or a name of the wrong kind: `vpic` and spines are
  not vocabularies), entry fields other than the vocabulary's, a target on
  another spine, or `vin` given both as `from` and as context.

## Listing (`list`)

`ListRequest` pages through what is loaded; in Go, the Translator's `List`.

| Field | Type | Default | Rule |
| --- | --- | --- | --- |
| `of` | string | none | `vpic` or a loaded file's name. None asks for the catalog |
| `where` | object | none | Fields matched exactly after the fixed normalization: a vocabulary's fields; for `vpic` only `make`, a make id, which lists that make's models. Spines and extension sets take none |
| `limit` | int | 100 | 1 to 1,000 entries a page |
| `after` | string | none | The previous page's `next` |

- **The catalog** (no `of`, and then no `where`, `limit` or `after`) is
  vPIC, then every loaded file in load order, each a `CatalogEntry`:
  `name`, `kind` (vPIC is a `spine`), `version` (vPIC's is the data
  month), `subject` (`vehicle` or `part`), a vocabulary's `fields`, and the
  `spine` a vocabulary or extension set routes through.
- **Entries.** A vocabulary's are `VocabularyEntry` values, its entries
  with their mappings as the file has them, ordered by normalized fields.
  vPIC's makes, or one make's models, are `SpineEntry` values ordered by
  normalized name, then id; vPIC records no year span per model, so there
  is no year filter. A spine's entries and an extension set's extensions
  are `SpineEntry` values ordered by id.
- **Paging.** The order is stable for a given set of loaded files. `next`
  is an opaque cursor, present when more entries follow; passing it as
  `after` continues after the last entry returned, so paging end to end
  loses and repeats nothing.
- **The answer** (`List`): `of`, `entries`, `next`, `vocabularies` (every
  file for the catalog, the listed file, none for `vpic`), `data_month`,
  `decoder_version`. An `of` nothing loaded has, a `where` field the
  listing lacks, a `limit` out of range or an `after` that is not a cursor
  of the listing's shape is `#invalid-request`.

## C ABI (`libnitrovin`, `nitrovin.h`)

Lean only: the library carries no data. The artifact is its own release asset,
loaded by path, the same file `--data` takes; vocabularies, extension sets and
declared spines load beside it by path, as `--vocab` takes them.

```c
typedef struct nitrovin nitrovin_t;

/* Load an artifact. NULL on failure, with *err set to a message the caller
   frees with nitrovin_free. */
nitrovin_t *nitrovin_open(const char *artifact_path, char **err);

/* nitrovin_open, then load the n paths in vocab_paths (files, or directories
   of *.jsonl) beside the artifact, as --vocab does. vocab_paths may be NULL
   when n is 0. */
nitrovin_t *nitrovin_open_vocab(const char *artifact_path,
                                const char *const *vocab_paths, size_t n,
                                char **err);

/* request_json: one StdioRequest of any op (decode, translate, list).
   Returns one StdioReply, exactly the line `nitrovin stdio` would write,
   without the newline. Never NULL. */
char *nitrovin_decode(nitrovin_t *h, const char *request_json);

/* The /v1/info object, the handle's vocabularies included. */
char *nitrovin_info(nitrovin_t *h);

/* Free any string this library returned; never use the caller's free(). */
void nitrovin_free(char *s);

/* Release a handle. Using it afterwards is undefined. */
void nitrovin_close(nitrovin_t *h);
```

- `nitrovin_decode` answers every op, despite its name (ADR 0012): a
  translation or a list is a `StdioRequest` with its `op`, from the files
  the handle loaded, or vPIC alone for a `nitrovin_open` handle.
- `nitrovin_open_vocab` loads as `--vocab` does (Vocabularies, above): a
  fault fails the open, NULL with `*err` naming the file and line; drift
  is served, and since a library has no stderr, `nitrovin_info` is where
  it shows, in each file's `drift`.
- Strings are UTF-8 and NUL-terminated in both directions.
- Handles are safe to use from several threads at once. Two handles may hold
  two data months in one process.
- The library starts the Go runtime when it loads and **cannot be unloaded**
  (golang/go#11100): never `dlclose`/`FreeLibrary`/`NativeLibrary.Free` it.
- Memory the library returns comes from its own allocator; with a Windows
  host's different CRT, `free()` on it corrupts the heap.

## Versioning

- The API's major is the module's: `/v1` is `github.com/joshuasprow/nitrovin`
  v1.x, and this contract is v1's.
- Within v1, changes are additive only: new fields in responses, new routes,
  new optional request fields, new problem types. Renaming, retyping or
  removing anything, or changing a default, is nitrovin v2 with `/v2`.
- Data is not contract. Element names and codes, and the names of lookup
  attributes, change between data months (NHTSA renamed Body Class attribute 7
  between 2026_06 and 2026_10); elements appear and disappear, and the `vpic`
  layout's key set follows them. Pin `element_id` and `attribute_id`.
- Clients should ignore response fields they do not know. Servers refuse
  request fields they do not know.
