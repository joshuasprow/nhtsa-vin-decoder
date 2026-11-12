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
`GET` and `POST /v1/translate`, `GET` and `POST /v1/fit`, `GET /v1/vocabularies`,
`GET /v1/vocabularies/{name}/entries`, `GET /v1/info`,
`GET /v1/openapi.json`, `GET /healthz`, and `POST /mcp`, the MCP server
below over Streamable HTTP. `GET /` (`HEAD` too) and `/ui/…` are pages for
browsers (HTML, a fragment for htmx, and the whole page for its history
restore: back and forward ask again, htmx keeps no snapshot; ADR 0013):
`/` the one page, which decodes and translates, drawing each translation
as a map of linked cards, and which old links (`/?vin=…`, `/ui/decode`,
`/ui/translate`, `/vocab`, `/ui/vocabularies`) redirect to (`301`); a
vocabulary's entries (`/ui/vocabularies/{name}`), which carries the same
Vocabularies dialog; the Service Worker's panel (`/ui/files`) and
`/sw.js`; and their assets under `/ui/r/<release>/`. All are outside the
contract, not in `openapi.yaml`, and may change.

**Translate, list and fit** ([Translation](#translation-translate),
[Listing](#listing-list), [Fit](#fit-fit)) carry the same request objects:

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
- `POST /v1/fit` takes a JSON array of `FitRequest` (≤ 1,000 items,
  ≤ 1 MiB) and answers an array of `Fit` in the same order, reading the
  clock once. `GET /v1/fit` takes one request as dotted query parameters,
  read as the JSON request they spell, as `GET /v1/translate` is:
  `from.vin`; `from.vocabulary` with one `from.entry.<field>` per field;
  `from.make`, `from.model` or `from.ext`; or `from.fitment.type`,
  `from.fitment.number` and `from.fitment.side`. Then `part.spine` with
  `part.id`, or `part.vocabulary` with one `part.entry.<field>` per field;
  `side`; `to` repeated; `model_year`; `related` (`true` or `false`);
  `limit`; `after`; `now`.
- `GET /v1/vocabularies` is the catalog (`list` with no `of`), and takes no
  parameter. `GET /v1/vocabularies/{name}/entries` is `list` with `of` the
  name (`vpic` included, and the loaded catalog's, which pages its
  fitments): `where.<field>` per field (`where.make=476`;
  `where.model=1850&where.element=34` for a model's values;
  `where.type=mirror` for a catalog's part type), `limit`, `after`.
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
| <a id="invalid-request"></a>`#invalid-request` | 400 | Valid JSON that breaks the request schema: wrong type, unknown field or query parameter, empty or over-long VIN, bad `model_year`, `full`, `shape` or `now`; an empty batch. For translate, list and fit, also what the loaded files refuse (below) |
| <a id="too-large"></a>`#too-large` | 413 | More than 1,000 items, or a body over 1 MiB |
| <a id="not-found"></a>`#not-found` | 404 | No such route |
| <a id="method-not-allowed"></a>`#method-not-allowed` | 405 | The route exists; the method does not (`Allow` lists the methods) |
| <a id="internal"></a>`#internal` | 500 | A fault in nitrovin |

The full URI is
`https://github.com/joshuasprow/nitrovin/blob/main/docs/api/README.md#<fragment>`.

## stdio (`nitrovin stdio`)

JSON lines over stdin and stdout, UTF-8, `\n`-terminated.

- Each input line is a `StdioRequest`: an optional `op`, `decode` (the
  default), `translate`, `list` (ADR 0012) or `fit` (ADR 0018), and an
  optional `id` (any JSON value), beside that op's request object: a decode
  request, a `TranslateRequest`, a `ListRequest` or a `FitRequest`. A field the op's request lacks is
  refused, as everywhere; another `op` is `#invalid-request` at `/op`. A
  line without `op` is a decode request, so a session of decode lines gets
  the bytes it always has.
- Each input line gets exactly one output line, in input order, flushed per
  line, keyed by its op: `{"id": …, "decode": {…}}`,
  `{"id": …, "translation": {…}}`, `{"id": …, "list": {…}}` or
  `{"id": …, "fit": {…}}`, or
  `{"id": …, "error": <problem>}`. `id` is present when the request had one,
  or could be read from a refused line.
- Translations, lists and fits answer from the files `--vocab` or
  `NITROVIN_VOCAB` loads with the data, as for every command; with none,
  from vPIC alone, and a fit is the status `no-catalog`.
- A malformed or refused line gets an error line, and the session continues.
- Blank lines (nothing but spaces, tabs and a `\r`) are skipped and get no reply.
- EOF on stdin ends the session after the last reply; exit 0. Exit 1 only when
  reading stdin or writing stdout fails (or, as for every command, no data loads).

The problem objects are HTTP's, with the same `type` URIs and `status`.

## MCP (`nitrovin mcp`, `POST /mcp`)

An MCP server with four tools over stdio (`nitrovin mcp`) and over
Streamable HTTP (`nitrovin serve`'s `/mcp`): `decode_vin` decodes,
`translate` translates ([Translation](#translation-translate)), `list`
lists ([Listing](#listing-list)) and `fit` fits ([Fit](#fit-fit)). Their definitions (name, title, the
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
  `TranslateRequest`, a `ListRequest` or a `FitRequest`. One VIN,
  translation, page or fit per call.
- **Schemas** are self-contained, because MCP forbids fetching a `$ref` over
  the network. Each is `{"type": "object", "$ref": …, "$defs": …}`, and every
  entry in `$defs` equals the same-named definition in
  [`decode.schema.json`](decode.schema.json): `decode_vin` bundles
  `DecodeRequest` and `Decode`, `translate` `TranslateRequest` and
  `Translation`, `list` `ListRequest` and `List`, `fit` `FitRequest` and
  `Fit`.
- **An answer** (a decode, translation, list or fit) is the result's
  `structuredContent`, and the same JSON, compact, is its one text block. Clients differ in which of the two they show
  the model, so the two must stay identical.
- **A refusal** is a result with `isError: true` and one text block: the
  Problem as compact JSON, with stdio's `type` URIs and `status`, and `pointer`
  into the arguments (`/vin`, `/to/0`). It has no `structuredContent`. JSON-RPC errors
  are only for MCP faults, such as an unknown tool or a malformed request.
- **Annotations:** read-only, not destructive, idempotent, closed-world.
- **The descriptions are static.** They name neither the data month nor
  the loaded vocabularies or catalog, so `--data` and `--vocab` never
  change `tools/list`; every answer carries `data_month`, and `translate`'s,
  `list`'s and `fit`'s descriptions tell the assistant to call `list` with
  no arguments to discover what is loaded. Without `--vocab` every tool is
  still listed: `translate` and `list` answer for vPIC alone, and `fit`
  answers the status `no-catalog`.
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
| `--where field=value` | `where`, repeatable, a field once (`--where make=476`, or `--where model=1850 --where element=34`, for `vpic`) |
| `--limit N` | `limit`, 1 to 1,000 |
| `--after <cursor>` | `after`, the previous page's `next` |

`nitrovin list [of]` prints one `List`, pretty-printed on a terminal and one
compact line elsewhere, exactly as the Go module marshals it. A request the
loaded files refuse (an `of` nothing loaded has, a `where` field the
listing lacks, `--limit` or `--after` beside the catalog, an `after` that is
not a cursor of the listing) is a problem on stderr, nothing on stdout,
exit 2. A malformed flag (`--where` without `=`, a field given twice, a
`--limit` outside 1 to 1,000) or a second argument is a usage error, exit 2.

### `nitrovin fit`

| Argument or flag | Request field |
| --- | --- |
| `<vin>…` | `from`, one request each |
| `--part spine:id` | `part`, an entry of the catalog's part spine |
| `--side left\|right\|both` | `side` |
| `--to a,b` | `to`, comma-separated; repeatable, each use appending |
| `--model-year N` | `model_year` |
| `--related` | `related` |
| `--stock shelf,yard` | `stock`, comma-separated |
| `--location L` | `location`; repeatable, each use appending |
| `--limit N`, `--after <cursor>` | `limit` (1 to 1,000) and `after` |
| `--now <instant\|date>` | `now` |

`nitrovin fit <vin>…` fits each argument VIN against the loaded catalog
([Fit](#fit-fit)): its part types, with `--part` its candidates, with
`--related` the vehicles that share its fitments. `nitrovin fit -` reads one
`FitRequest` object per stdin line (a VIN, an entry, a spine entry or a
fitment key as `from`), and the flags fill what a line leaves out: a line's
own `to`, `side`, `model_year` and `now` beat the flags', and a line
without one takes the flag's. `--part` and `--related` fill a line
whose `from` is a vehicle and that gives neither, and `--limit` a line
without `limit`; `--after` continues one answer, so it is a usage error
beside `-`. Output, refusals and exit codes are
`translate`'s: one argument prints one `Fit`, several or `-` print JSON
lines; a refused argument is exit 2 with nothing on stdout (a VIN before
any data is loaded, a `--part` or `--to` the loaded files refuse after); a
refused stdin line stops the run with exit 1. Nothing to fit, no catalog
loaded included, is an answer with its `status`, exit 0.

## Vocabularies (`--vocab`)

Vocabularies, extension sets, declared spines, catalogs and stock files
(ADR 0009, 0010, 0011, 0016, 0017, 0019) are JSON Lines files: a header,
then one line per entry, extension, spine entry, a catalog's fitment,
redirect or retired number, or a stock source's row, each as [`vocabulary.schema.json`](vocabulary.schema.json)
specifies.

- **Loading.** `--vocab <file|dir>` names them, on the root or any command,
  repeatable; a directory loads every `*.jsonl` directly in it, in name
  order. Without `--vocab`, `NITROVIN_VOCAB` is a path list (`:`-separated,
  `;` on Windows); flags replace it, as `--data` replaces `NITROVIN_DATA`.
  Files load once, with the data, and never change for the life of the
  process: `serve` picks up new files on restart.
- **Compiled catalogs.** A file named on `--vocab` that begins with the
  compiled catalog's magic, `nvcatalg`, loads as a compiled catalog
  (ADR 0021), which `nitrovin vocab compile` writes: it opens in
  milliseconds, is read in place, and answers exactly as its JSON Lines
  source. A directory loads only its `*.jsonl`, so name a compiled file
  itself. It is refused, as a fault about the file as a whole (line 0), in
  a compiled format this release cannot read, cut short, or when its spine
  or vehicle vocabulary is not loaded, or is loaded without a part type or
  vehicle the catalog was compiled with. Its drift is not measured in full
  at load: it is the drift its compiling measured and stored, but for its
  `vehicle` drift, measured again against the vehicle vocabulary loaded
  (one look per vehicle).
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
  the last; qualifiers on an extension mapping, or on a catalog application
  whose vehicle is an extension; a reference to an extension
  or spine id no loaded file defines; a part vocabulary whose spine is not
  loaded; two files with one name, or one named `vpic`. A catalog adds: a
  second catalog; its spine or vehicle vocabulary not loaded (or not a
  spine, or not a vehicle vocabulary); a part type its spine lacks, in a
  fitment or a redirect; a vehicle whose fields are not exactly the
  vehicle vocabulary's, or, for `vehicles: "vpic"`, not exactly one of
  `make`, `model` or `ext`; a side other than `left`, `right` or `both`;
  a missing `options`; a fitment key, an application within one fitment
  (same vehicle, years, options and qualifiers) or a retired number given
  twice; a retired number that is a live fitment or whose `to` is no
  fitment in the file. A stock file adds: a `source` other than `shelf` or
  `yard`; an `as_of` that is no RFC 3339 time or date; a shelf without a
  `catalog`, or whose catalog is not loaded; a yard with a `catalog` or
  `release`; a row without `id` or `location`, or two rows with one `id`;
  a shelf row with both a fitment key and `part`, or neither whole, or
  whose `fields` are not flat; a yard row naming no route; a `vehicle`
  without its `year`, or one no catalog is loaded to name; a `part` the
  catalog's spine, or a part vocabulary on it, lacks; a vehicle named by
  other fields than the catalog's vehicle vocabulary's, or one that
  vocabulary has no entry for, so the row could never match. A connector's
  file adds: any line after its header; an `as_of` or `release` in its
  header (it reads them from its database); a connector other than
  `postgres`; a `relation` or `release_relation` that is not `name` or
  `schema.name` in unquoted lower case; a missing `relation` or `dsn_env`,
  a `dsn_env` that is no environment variable's name, any other member
  (a DSN among them); a yard's `release_relation`; a timeout or
  `max_connections` that is not a positive integer.
- **Drift warns and serves.** Measured against the loaded data month: a
  vPIC make or model id it lacks; a qualifier its decode cannot satisfy (an
  element that is not one of its public elements, a value of the wrong type
  for the element, or an attribute id or text that none of the month's
  pattern, engine-model, vehicle-spec or default-value rows, nor for Make,
  Manufacturer Name and Vehicle Type its WMI tables, produce; values a
  formula pattern computes from a VIN are not known); an extension named,
  after normalization, like a vPIC make, or like a vPIC model under the same
  make; a mapping to a vPIC model whose `first`–`last` fall partly or
  wholly outside the model's years (`model-years`, ADR 0022: the finding
  says which and gives the model's ranges; a model with unknown years, and
  a mapping with neither bound, is never flagged, and an absent bound is
  the model's own); a qualifier value on a mapping to a vPIC model that the
  month decodes but the model never does (`model-qualifier`, ADR 0015:
  tested against the model's values as `list vpic` with `where.model` and
  `where.element` gives them; years are not tested; mappings to a make or
  an extension, and qualifiers on Model Year and the Corrections elements,
  which have no model values, keep only the month-wide test; the browser,
  whose spine bundle has no model values, reports none). A catalog adds,
  against the files loaded with it: a
  vehicle its vehicle vocabulary lacks or lists without a mapping
  (`vehicle`), and a redirect whose `to` part type has no application for
  its vehicle in overlapping years (`redirect`); its vPIC id, qualifier,
  `model-years` and `model-qualifier` drift is measured as a mapping's, the
  last two on applications
  whose vehicle is a vPIC model (`vehicles: "vpic"`). A catalog's drift is
  one finding per vehicle, vPIC id, qualifier value or model-year range, at
  the first line naming it, its detail ending `(and N more)` when other
  applications or redirects name it too, and one per redirect. A shelf
  stock source adds one finding per **orphan** row, whose number its
  catalog lacks, current or retired (`orphan`); any stock source one per
  field name, nested ones and those inside arrays included, that holds
  `email`, `phone`, `name`, `address` or `license` in any case
  (`phonenumber`, `seller.email` and `part_name` alike), once at the first
  row with it (`personal-field`). Each
  finding but `model-qualifier` is one line on stderr at load,
  `{"file", "line", "severity": "drift", "detail"}`, and the files are
  served as written. `model-qualifier` is not measured at load: it builds
  the model values index, about 0.14 s and 80–100 MB of peak memory for an
  EPA-sized vocabulary, which would break load's 100 ms budget (ADR 0011)
  on every start and C handle. `vocab check` always measures it, and
  `/v1/info` on its first request, cached after. A compiled catalog's
  drift, `model-qualifier` included, is the drift its compiling stored,
  against the month it was compiled against, but for its `vehicle` drift,
  measured at load against the vehicle vocabulary loaded.
- **`/v1/info`** (and `nitrovin info`, `nitrovin_info`) lists every loaded
  file in `vocabularies`, in load order: `name`, `kind`, `version`, `blake3`
  (hex, of the file's bytes; a compiled catalog's is the hash its header
  records, of its bytes with the hash's own 32 bytes zero), `path`
  (absolute) and `drift`, a count per kind of drift the file has (`make`,
  `model`, `qualifier`, `extension`, `model-years`, `vehicle`, `redirect`,
  `model-qualifier`, `orphan`, `personal-field`, in that order) with its first three findings as
  `{"line", "detail"}`; the first request measures `model-qualifier`, so it
  may take a fraction of a second longer. A catalog adds its `release`,
  when its header gives one, and its counts of `fitments`, `applications`,
  `redirects` and `retired` numbers; a stock file its `source`, a
  shelf's `catalog` and `release`, its `as_of` and its `rows`; a stock
  connector ([ADR 0020](../adr/0020-request-time-stock-the-postgres-connector.md))
  its `source`, a shelf's `catalog`, `connector: "postgres"`, its
  `relation` and its `status`: `unchecked` until its first lookup connects
  (nothing connects at load), then `available`, or `unavailable` with a
  `reason`. Never a DSN, host or user. No other
  kind has them. A compiled
  catalog adds `compiled`: its `format`, the `source_blake3` of the JSON
  Lines file it was compiled from, the `writer` (the nitrovin version that
  compiled it), the `drift_month` its drift counts were measured against,
  `drift_month_differs`, true when that is not the loaded data month, and
  `links_differ`, true when its spine or vehicle vocabulary as loaded is
  not the file it was compiled against (another blake3), and
  `skipped_drift`, the drift kinds its compiling could not measure: `[]`
  from `nitrovin vocab compile`, `model-years` and `model-qualifier` from a
  compile in a browser ([ADR 0023](../adr/0023-fitment-in-the-browser.md)),
  which `nitrovin vocab check` measures. It is `[]` when
  nothing is loaded, as for every `nitrovin_open` handle.
- **`nitrovin vocab check <file|dir>…`** loads its arguments, and only
  them, beside the data (`--data` as for any command) and prints every
  finding on stdout as a JSON line,
  `{"file", "line", "severity": "fault"|"drift", "detail"}`: the faults
  when there are any, otherwise the drift. Exit 0 with no output when there
  is none, 1 when there is any. Line 0 is a file that cannot be read. A
  catalog is checked like any file, so its spine and vehicle vocabulary
  must be among the arguments, and a stock file's catalog with them. A
  compiled catalog is read in full, its
  bytes checked against its header's hash, then checked line by line and
  its drift measured again against the loaded month, as its source would
  be; its findings name the compiled file and its source's lines.
- **`nitrovin vocab compile -o <file> <file|dir>…`** checks its arguments
  as `vocab check` does and, on a fault, prints the faults the same way,
  exits 1 and writes nothing. Otherwise it writes the one catalog among
  them, compiled, to `-o` (replacing it whole), its drift on stderr one
  finding a line as a load writes it, `model-qualifier` included, which a
  load does not write, and exits 0. The compiled file records its source's
  blake3, the catalog's name, version and `release`, the spine and vehicle
  vocabulary it was linked against, the writer, and the data month and
  drift its check found. The same arguments, data month and nitrovin
  version give the same bytes.

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
| `where` | object | none | Fields matched exactly after the fixed normalization: a vocabulary's fields; for `vpic`, `make`, a make id, which lists that make's models, and with it `year`, one model year; or `model` and `element` together, a model id and a public element id, which list the model's values for the element. Spines and extension sets take none |
| `limit` | int | 100 | 1 to 1,000 entries a page |
| `after` | string | none | The previous page's `next` |

- **The catalog** (no `of`, and then no `where`, `limit` or `after`) is
  vPIC, then every loaded vocabulary, extension set and spine in load
  order, each a `CatalogEntry`: `name`, `kind` (vPIC is a `spine`),
  `version` (vPIC's is the data month), `subject` (`vehicle` or `part`), a
  vocabulary's `fields`, and the `spine` a vocabulary or extension set
  routes through. A loaded catalog (ADR 0017) is in `/v1/info`, not here.
- **A catalog's fitments** (ADR 0018): `of` naming the loaded catalog
  pages through its fitments in key order (part type, side, number), each a
  `FitmentLine`, the fitment line as the file has it (`fitment`, `note`,
  `applications` with `vehicle`, `first`, `last`, `options`,
  `qualifiers`), 1,000 at most a page. `where.type` keeps one part type;
  any other `where` field is `#invalid-request`. `vocabularies` is the
  catalog's name and version.
- **Entries.** A vocabulary's are `VocabularyEntry` values, its entries
  with their mappings as the file has them, ordered by normalized fields.
  vPIC's makes, or one make's models, are `SpineEntry` values ordered by
  normalized name, then id. A model's entry carries `years`, the model
  years its VIN schemas cover (ADR 0022): the union of the year bands of
  every VIN schema with a Model pattern naming it, across all its WMIs,
  merged into ranges, `[{"first": 1999, "last": 2006}, {"first": 2009}]`.
  Gaps are kept, a range with no `last` is still running, and when no VIN
  schema with a Model pattern naming a model has a year band, the model has
  no `years`: unknown, not empty. `where.year` (only
  with `where.make`) keeps the models whose ranges include the year, an
  open range including every later year, and the models with unknown
  years: a superset of what the make built that year. A spine's entries and an extension set's extensions
  are `SpineEntry` values ordered by id.
- **Model values** ([ADR 0015](../adr/0015-model-values-for-an-element.md)).
  `vpic` with `where.model` and `where.element` lists a model's values for
  the element: every value a decode with a model year gives the element for
  a VIN of the model, except a value a formula pattern computes from the
  VIN's characters; it may list values no VIN of the model ends up with.
  Each is a `ModelValueEntry`: `value`, the attribute id of a lookup
  element or the exact text of a text one, and for a lookup element its
  attribute's `name`. Text values are ordered by normalized then exact
  value, so case variants ("3/4 Ton", "3/4 ton") are each listed;
  attribute ids ascending. A value counts when it comes from a pattern
  whose key could match one VIN together with one of the model's Model
  (28) pattern keys, in the same VIN schema or one its WMI code reaches in
  an overlapping year band; from an engine model row through such an
  Engine Model (18) pattern; from a vehicle spec naming the model whose
  make and vehicle type the model's WMIs carry; from a default value for a
  vehicle type they have; or from a conversion of one of the model's
  values of the conversion's from element (Displacement CC, CI and L).
  Make (26) is the model's makes, Manufacturer Name (27) and Vehicle Type
  (39) its WMIs', Model (28) the model. An unknown model id lists nothing.
  These are `#invalid-request`: `model` or `element` alone, either beside
  `make` or `year`, an element that is not public, and one decoded from the
  VIN itself rather than a pattern (Model Year, 29, and the Corrections
  elements 142, 143, 144, 156, 191, 196), refused at `/where/element`. The
  index behind it is built per element on its first ask and kept: 0.14-0.16
  s for Series and Drive Type together on an M5 Pro, 60-90 ms of it a join
  every element shares, which keeps about 31 MB, and about 1.5 MB per
  element after it. Displacement CC, CI and L, which convert one another's
  values, take about 0.4 s and keep about 17 MB together.
- **Paging.** The order is stable for a given set of loaded files. `next`
  is an opaque cursor, present when more entries follow; passing it as
  `after` continues after the last entry returned, so paging end to end
  loses and repeats nothing.
- **The answer** (`List`): `of`, `entries`, `next`, `vocabularies` (every
  file for the catalog, the listed file, none for `vpic`), `data_month`,
  `decoder_version`. An `of` nothing loaded has, a `where` field the
  listing lacks, a `limit` out of range or an `after` that is not a cursor
  of the listing's shape is `#invalid-request`.

## Fit (`fit`)

`FitRequest` reads the loaded catalog (ADR 0016, 0017,
[ADR 0018](../adr/0018-fit-is-a-request-beside-translate.md)); in Go,
`nitrovin.NewTranslator(d, files…)`'s `Fit(ctx, req)`, over
`contract.Fitter` and `fitment.Reader`; `ctx` bounds its stock lookups,
and HTTP and MCP pass the request's. The HTTP, stdio, CLI, MCP and C surfaces carry the same
object. Its inputs shape the answer:

| Field | Type | Default | Rule |
| --- | --- | --- | --- |
| `from` | string \| object | required | A vehicle: a VIN; a vehicle vocabulary's entry, `{"vocabulary", "entry": {…}}`; or `{"make"}`, `{"model"}`, `{"ext"}`. Or a fitment key, `{"fitment": {"type", "number", "side"?}}` |
| `model_year` | int \| null | none | The vehicle's year when no VIN decodes one, and a VIN's decode hint; with a fitment key, it keeps the applications whose years hold it |
| `part` | object | none | A part type: `{"spine", "id"}`, an entry of the catalog's part spine, or `{"vocabulary", "entry"}`, an entry of a part vocabulary on that spine |
| `side` | `left` \| `right` \| `both` | none | The side filter, below |
| `to` | string[] | none | Vehicle vocabularies the answer's vehicles are named in; none names them as spine entries |
| `related` | bool | `false` | The vehicles that share fitments with `from` |
| `stock` | string[] | none | `shelf`, `yard` or both: the stock of the answer's fitments (Stock, below). Only with `part`, `related` (`yard` only) or a fitment key |
| `location` | string[] | none | Keeps the stock rows at one of these locations. Only with `stock` |
| `limit` | int | 100 | 1 to 1,000 items a page |
| `after` | string | none | The previous page's `next` |
| `now` | string | wall clock | A VIN's decode clock, as a translate request's |

| `from` | Also given | Answer's list |
| --- | --- | --- |
| a vehicle | nothing | `part_types`: each part type the vehicle has, with its count of fitments |
| a vehicle | `part` | `candidates` |
| a vehicle | `related` (and `part`, to keep its part types) | `related` vehicles |
| a fitment key | — | `vehicles`: the fitment's applications |

- **Reaching the catalog.** A vehicle reaches the catalog's own vehicles
  as a translation to its vehicle vocabulary would (through the spine, or
  as itself when it is that vocabulary's entry); a `vpic` catalog's are
  the spine entries reached. They are the answer's `models` (`vehicle`,
  as the catalog writes it, with `via` and `unconfirmed` as a translation's
  target entry has them), so a person can be asked which one when there
  are several. A `part` reaches part types the same way.
- **Year and qualifiers.** The year is the VIN's decoded year, else
  `model_year`. An application whose years exclude it, or whose qualifier a
  decoded value fails, is dropped; what could not be checked keeps it and
  is listed in its `unconfirmed`, with what its model and part type left
  unchecked. Without a year every year's applications come back, with
  `"model_year"` unconfirmed where they have years.
- **Candidates** are ordered by part type, side and number. Each has
  `fitment` (`type`, `number`, `side`, `note`), `applications` (each
  `vehicle`, `via`, `first`, `last`, `options` verbatim, `unconfirmed`)
  and `reached`: `direct`, or `redirect` with the `redirects` that led
  there (one hop, never more). A redirect is for a catalog vehicle and
  years: it is followed unless a direct candidate already has an
  application to that vehicle in years that overlap the redirect's, so
  one of several `models`, or one range of years, can be reached through a
  redirect while the others are direct. A fitment reached both ways is
  `direct`, and a part type in `part_types` is `direct` when any of its
  candidates is. The catalog's option
  text is the requirement a person settles; it is never interpreted.
- **The side filter** drops only a fitment whose side excludes the one
  asked for: `left` keeps `left`, `both` and fitments with no side; `right`
  likewise; `both` keeps `both` and fitments with no side. A part for both
  sides goes on either, and a fitment that states no side is never ruled
  out by one, as an unknown value keeps an application (ADR 0009).
- **A fitment key** answers its `fitment` with `reached`: `direct`, or
  `retired` with `retired` (the key asked for and the catalog's note) when
  the number is retired. Its `vehicles` are its applications in file order,
  each named into every `to` vocabulary (`to`, as a translation's) or,
  without `to`, as spine entries (`spine`), translated within the
  application's years.
- **Related vehicles** are the catalog vehicles that share at least one
  fitment with `from` (the vehicle's own applications aside: in its year
  when one is known, else all of them). Each vehicle's years are cut where
  the set of fitments it shares changes: a row is a catalog vehicle and
  the longest year range (`first`, `last`, either open) over which it
  shares the same fitments, with `shared`, how many it shares in every
  year of that range, and `part_types`, those per part type. A vehicle
  whose shared applications all span the same years is one row; years in
  which it shares nothing are in none. Rows are ordered by `shared`, most
  first, then by name and years, and then, among different vehicles of
  equal names, as the catalog first lists them among the shared
  fitments: by part type, fitment and application. They are named as a
  fitment's vehicles are, within the row's years.
- **The answer** (`Fit`): `status`; `catalog` (`name`, `version`,
  `release`, `spine`, the part spine a `part` names, and `vehicles`, its
  vehicle vocabulary or `vpic`: `list` with no `of` leaves the catalog
  out, so a fit answer is where a caller learns its name for `list`'s
  `of`); `vehicle` with a VIN, as a translation's; `models`; the list
  the inputs ask for, always present, empty when nothing fits; `total`,
  the count across every page; `next`, present when a page follows; `now`,
  `data_month`, `decoder_version`. Paging is `list`'s: `next` is an opaque
  cursor of this answer, and passing it as `after` continues, losing and
  repeating nothing.
- **Nothing to fit is never a refusal.** It is a `status`: `found`;
  `unknown` (a VIN that decodes to no make or model, an entry or spine
  entry the files lack, a part type the spine lacks, a fitment key that is
  neither a fitment nor a retired number); `no-vehicle` (the vehicle
  reaches none of the catalog's vehicles); `no-fitment` (nothing fits); or
  `no-catalog` when none is loaded, which is nitrovin.app's case. Only a
  malformed request is `#invalid-request`, with a pointer: a part
  vocabulary (or a spine) that isn't the catalog's; `part` or `related`
  with a fitment key; `stock` other than `shelf` and `yard`, or without
  `part`, `related` or a fitment key, or `shelf` with `related`;
  `location` without `stock`; a side other than `left`, `right` or `both`; an
  unknown vocabulary (or a name of the wrong kind: a part vocabulary as
  `from` or in `to`); entry fields other than the vocabulary's; a fitment
  key without `type` and `number`. Besides those, as for every request:
  a field of the wrong type or unknown, a `limit` out of range, or an
  `after` that is not a cursor of the answer.

The synthetic catalog's examples, tested on every surface, with
`--vocab testdata/vocab --vocab testdata/vocab/fitment`:

```json
{"op":"fit","from":{"vocabulary":"catalog-models","entry":{"make":"Dodge","model":"Ram 1500"}},"model_year":2010}
{"op":"fit","from":"1D7RV1GT5AS123456","part":{"spine":"part-types","id":"mirror"},"side":"left"}
{"op":"fit","from":{"vocabulary":"catalog-models","entry":{"make":"Chevrolet","model":"Silverado 1500"}},"model_year":2010,"part":{"spine":"part-types","id":"brake-rotor-rear"}}
{"op":"fit","from":{"fitment":{"type":"brake-rotor-front","number":"BR-1999"}},"to":["yard"]}
{"op":"fit","from":"1D7RV1GT5AS123456","related":true}
```

- The 2010 Ram's part types: `brake-rotor-front` 1, `engine` 1, `mirror`
  2, each `reached: direct`.
- The VIN's left mirror: `MR-1001` (`side: left`), both of its Ram 1500
  applications for 2009–2012, `via` vPIC model 1938; the decode settles
  the 4WD qualifier, so neither is `unconfirmed`. From `{"model": 1938}`
  and a year instead, the 4WD one is `unconfirmed: [15]`.
- The 2010 Silverado's rear rotor: `BR-2001`, a front rotor, `reached:
  redirect` with the redirect's option text "rear rotors are listed with
  the front", `unconfirmed: [34]` (its Series qualifier). A 2007
  Silverado whose decoded Series fails that qualifier answers `no-fitment`.
- The retired number `BR-1999`: `BR-2001`, `reached: retired`, with the
  Ram 1500 (2009–2012) and the Silverado 1500 (2007–2013), each named in
  `yard`.
- The Ram's related vehicles: the Silverado 1500, 2007–2013, sharing one
  `brake-rotor-front`.

`{"op":"list","of":"parts-catalog","where":{"type":"mirror"}}` pages the
two `MR-1001` lines as the file has them. Without the catalog every fit
answers `{"status":"no-catalog", …}`.

### Stock

`stock` adds what is in stock to a fit
([ADR 0019](../adr/0019-what-stock-says.md)): shelf parts and whole yard
vehicles, from every loaded **stock source** of each kind, in load order.
A source is a stock file (Vocabularies, above) or a request-time
connector, a table or view in Postgres
([ADR 0020](../adr/0020-request-time-stock-the-postgres-connector.md),
[Connectors](../vocabularies.md#connectors)); in Go, a
`stock.Source`, which `contract.Fitter` reads with one lookup per source
and list kind: for a connector, one parameterised `SELECT` answering every
list of that kind in the answer, bounded by the page.

| Where | With | List |
| --- | --- | --- |
| each candidate | `stock: ["shelf"]` | `shelf`: the rows of its number, and of every retired number it replaced, the first 10 |
| each candidate | `stock: ["yard"]` | `yard`: the yard vehicles among all its applications' vehicles and years, the first 10 |
| the answer, with `part` | `stock: ["shelf"]` | `shelf_by_vehicle`: the rows with no number of the part types asked for, for one of `models` in exactly the vehicle's year (any year when none is known), paged with the candidates |
| each related vehicle | `stock: ["yard"]` | `yard`: its count of yard vehicles in its years, `rows` empty |
| a fitment key's answer | `stock` | `shelf` and `yard` of the fitment it reached, paged with `vehicles` |

**Paging.** A list that is the answer's own pages with the answer's list:
`limit` and `after` page `candidates` and `shelf_by_vehicle` together, and
a fitment key's `vehicles`, `shelf` and `yard` together; `next` is present
while any of them has more. `status` and `total` are the candidates' (or
the vehicles') alone: `shelf_by_vehicle` can hold rows, with its own
`total`, when `status` is `no-fitment`. A candidate's lists are its first 10 rows; a
fit from its fitment key with the same `stock` and `location` pages them
all. A related vehicle's `yard` is a count only (ADR 0019): no fit lists
those vehicles by themselves. A fit from one of the fitments it shares, with
`stock: ["yard"]`, lists the yard vehicles of every vehicle that fitment
fits. With `related`, `stock` is `["yard"]`: `shelf` would add nothing and is
refused.

Every list (`StockList`) is `rows`, `total` across every page and source,
and `sources`: each loaded source of its kind, as a block of `name`,
`kind` (`file` or `live`), for a live source its `connector`
(`postgres`) and `relation`, `as_of` (the time the list's rows from it
are as of: a file's header time, or the oldest `as_of` of the list's own
rows from a connector whose relation keeps one, absent when the list has
none of them), for a shelf its
`catalog` and the `release` the lookup read,
`catalog_mismatch: true` when that release is not the loaded catalog's,
and `status`: `available`, or `unavailable` with a `reason` when the
source failed. An unavailable source gives no rows, and its stock is
unknown, not empty; the rest of the answer stands. Its `reason` is the
source's own, which never names a connection detail; `the source
failed`; or `the request ended before the source answered` when the fit's
caller went away first (an HTTP client disconnecting, an MCP call
cancelled). A file source answers from memory and is always asked. A
connector's reasons are fixed text: its relation lacks a column, or has
one of the wrong type (naming it); its database is unreachable, refused
the credentials, or has no connection to spare; the query ran past its
statement timeout; every connection was busy until the lookup's
deadline; the environment variable holding its DSN is not set (naming the
variable). A connector that could not connect, at its first lookup or
after its database went away, answers unavailable at once for 5 s before
it tries again, and nothing it reads is cached.

A row (`StockRow`) is its `source` (the source's name), `id`, `location`,
how it `matched`, what it names, `as_of` when its source keeps a time per
row, and its `fields`, verbatim and never interpreted:

- a shelf row with a number: `matched: "fitment"`, its `fitment` key as
  the row wrote it, and `reached`: `direct`, or `retired` when that key is
  a retired number the fitment replaced;
- a shelf row with no number: `matched: "vehicle"`, its `part` type,
  `vehicle` and `year`;
- a yard row matched by vPIC model, its source's `decoded` model and year
  or its `vin` decoded once at load: `matched: "vin"`. A VIN that decodes
  no model, or a model and no year on a row that names its `vehicle` and
  `year`, falls to them; a VIN that decodes a model and no year, on a row
  that names no vehicle, is in every year of its model;
- a yard row matched by the catalog `vehicle` and `year` it names:
  `matched: "catalog-vehicle"`. A vocabulary catalog's vehicle reaches the
  vPIC models its entry maps to, in the years both hold; a make or an
  extension reaches no model.

**Yard matching checks vehicles and years only.** A yard row carries no
decode to check an application's or a mapping's qualifiers against, and
option text is never interpreted. So a fitment whose only application is
4WD lists the 2WD yard vehicles of that model and years too: settle the
options against the vehicle itself.

A shelf row whose number the catalog lacks, current or retired, is an
**orphan**: it is under no candidate, and `/v1/info` and `vocab check`
count it as drift.

The synthetic stock's examples, tested on every surface, with
`--vocab testdata/vocab --vocab testdata/vocab/fitment --vocab testdata/vocab/stock`:

```json
{"op":"fit","from":{"vocabulary":"catalog-models","entry":{"make":"Dodge","model":"Ram 1500"}},"model_year":2010,"part":{"vocabulary":"part-codes","entry":{"code":"ROT"}},"stock":["shelf","yard"]}
{"op":"fit","from":{"vocabulary":"catalog-models","entry":{"make":"Dodge","model":"Ram 1500"}},"model_year":2010,"part":{"spine":"part-types","id":"mirror"},"stock":["shelf"],"location":["north"]}
{"op":"fit","from":{"fitment":{"type":"brake-rotor-front","number":"BR-1999"}},"stock":["shelf","yard"],"limit":1}
{"op":"fit","from":{"model":1850},"model_year":2010,"related":true,"stock":["yard"]}
```

- The Ram's rotor `BR-2001`: `shelf` holds `A-1` from `shelf-annex`,
  which names no release, and `S-2` (`reached: direct`) and `S-3`, filed
  under the retired `BR-1999` (`reached: retired`), from `shelf-stock`,
  numbered under release 7.01 against the catalog's 7.02, so its block
  says `catalog_mismatch: true`. `yard` holds `Y-1` (a precomputed decode)
  and `Y-2` (its VIN decoded), both `matched: "vin"`, and `Y-3`, a 2010
  Silverado named by the catalog's vehicle, `matched: "catalog-vehicle"`.
- The Ram's mirrors at `north`: the left `MR-1001` holds `S-1`, the right
  none; `shelf_by_vehicle` holds `S-4`, a mirror with no number for the
  2010 Ram 1500, named by the part vocabulary's `MIR`, and drops `A-2`,
  the same at `east`.
- From `BR-1999` a page at a time: `A-1` and `Y-1`, then `S-2` and `Y-2`,
  then `S-3` and `Y-3`, and no `next`.
- The Silverado's related Ram 1500, 2009–2012, has 2 yard vehicles.

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

/* request_json: one StdioRequest of any op (decode, translate, list, fit).
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
  translation, a list or a fit is a `StdioRequest` with its `op`, from the
  files the handle loaded, or vPIC alone for a `nitrovin_open` handle (and
  a fit is then `no-catalog`).
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
