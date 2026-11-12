# `fit` is a request beside translate and list

A loaded catalog (ADR 0016, ADR 0017) is reached through one new request,
`fit`, on every surface. This decision comes from
[Fitment requests](https://github.com/joshuasprow/nitrovin/issues/225), on
[the fitment and stock map](https://github.com/joshuasprow/nitrovin/issues/221).
It is additive under `/v1` (ADR 0004, ADR 0007) and amends ADR 0012, which named
only `translate` and `list`.

## The request

Like `translate`, `fit` is shaped by its inputs rather than split by direction:

| Field | Meaning |
| --- | --- |
| `from` | A vehicle (a VIN; a vocabulary entry, `{"vocabulary","entry":{…}}`; or a spine target, `{"make"}`, `{"model"}`, `{"ext"}`), or a fitment key `{"fitment":{"type","number","side"?}}` |
| `model_year` | The vehicle's year when no VIN gives one; a decoded year wins, as in translate |
| `part` | A part type: an entry of the catalog's part spine, `{"spine","id"}`, or of a part vocabulary on that spine, `{"vocabulary","entry":{…}}` |
| `side` | `left`, `right` or `both`, to filter on |
| `to` | Vehicle vocabularies to name vehicles in, as in translate; without it, vehicles come back as spine entries |
| `related` | `true` for vehicles that share fitments with `from` |
| `limit`, `after` | Paging, as in `list`: 100 by default, at most 1,000, with a cursor |

The inputs decide the answer:

| `from` | Also given | Answer |
| --- | --- | --- |
| a vehicle | nothing | its **part types**, each with a count of fitments |
| a vehicle | `part` | **candidates** |
| a fitment | — | its **vehicles** |
| a vehicle | `related` | **related vehicles** |

A vehicle reaches the catalog the way `translate` would reach the catalog's
vehicle vocabulary: from a VIN or a spine target through the spine, and from an
entry of another vocabulary through the spine and back. A vocabulary entry of the
catalog's own vehicle vocabulary is used as it is.

## Candidates

The answer has, at the top level:

- `vehicle`: the decode's make, model and year, when a VIN was given;
- `models`: the catalog entries the vehicle reached, so a person can be asked which
  one when there are several;
- the catalog's name, version and `release`;
- `candidates`, `total` and `next`.

Each candidate carries:

- `fitment`: type, number, side and `note`;
- `applications`: those that matched, each with
  - the catalog's vehicle entry;
  - `via`, the spine entries that led to it;
  - `first` and `last`;
  - `options`, verbatim;
  - `unconfirmed`: the qualifier element ids, and `"model_year"`, that applied but
    could not be checked, absent when everything was checked (as in translate);
- `reached`: `direct`, `redirect` (with the type asked for), or `retired` (with the
  number asked for).

An application whose qualifier fails against a decode is dropped. An unknown value
keeps it (ADR 0009). Candidates are ordered by part type, side and number, which is
stable. The catalog's own order is not kept, because not every catalog has one.

A request with no part never returns candidates. A whole vehicle measured 1,090
fitments and 148 KB; with a part type and a year it is about 5. Without a part, the
answer is the part-type summary.

A request with no year (and no VIN) is allowed. Every year's applications come
back, paged, with `"model_year"` in `unconfirmed`. Nothing is ever cut short
silently: every list pages, and `total` says how many there are.

## Vehicles of a fitment, and related vehicles

From a fitment key, the answer is the fitment and its applications. Each vehicle
is the catalog's entry translated into every `to` vocabulary, or into the spine
without `to`. A retired number answers its successor, marked `reached: retired`.

Related vehicles are the vehicles (a catalog entry and a year range) that share at
least one fitment with `from`. Each comes with its count of shared fitments per
part type. They are ordered by shared count, then name, and paged.

## Bulk

`list` with `of: "<catalog>"` pages through the catalog's fitments, 1,000 at most at
a time, as ADR 0012 pages files. It takes `where.type`. Each row is a fitment line
as the file has it.

`POST /v1/fit` takes an array of up to 1,000 requests and answers them in order, as
translate's batch does. Existence checks and lookups of the newest model, one
number at a time, go there.

## Nothing to fit is never an error

- An unknown vehicle, a vehicle the catalog has no entry for, or a part type with
  no fitment for that vehicle answers with a status and empty lists.
- With no catalog loaded, which is nitrovin.app's case, `fit` answers status
  `no-catalog`. `list` returns vPIC alone the same way.

Only a malformed request is refused, as `#invalid-request` with a pointer:

- a part vocabulary that isn't on the catalog's spine;
- `part` or `related` given with a fitment key;
- a side outside `left`, `right` or `both`;
- an unknown vocabulary;
- entry fields other than the vocabulary's;
- a fitment key without `type` and `number`.

## Surfaces

| Surface | `fit` |
| --- | --- |
| HTTP | `POST /v1/fit` (an array of up to 1,000, at most 1 MiB, answered in order); `GET /v1/fit` with dotted parameters (`from.vocabulary`, `from.entry.<field>`, `part.spine`, `part.id`, `side`, `to` repeated, `model_year`, `related`, `limit`, `after`) |
| stdio and C | `op: "fit"`, reply keyed `fit`; C answers it through `nitrovin_decode` |
| CLI | `nitrovin fit [--part spine:id] [--side s] [--to a,b] [--model-year N] [--related] <vin \| ->` |
| MCP | the tool `fit`, always listed, with a static description |
| Go | `Fitter.Fit`, beside `Translator`, over `fitment.Reader` (ADR 0017) |

The contract documents change in the same PR as the code that serves them.

## Budget

These are measured at the server on the catalog-scale fixture (ADR 0017), on the
reference machine, by the same CI benchmark as the load. All are p99:

| Request | Budget |
| --- | --- |
| Candidates for a vehicle, a year and a part type | at most 5 ms |
| The part-type summary | at most 10 ms |
| Related vehicles | at most 50 ms |
| Reverse | at most 1 ms per key |

A whole vehicle measured 2.7 ms p99 on today's engine, and one fitment to its
vehicles 1 µs. The first user asked only to beat today's 0.1–0.9 s p50. These
budgets leave a wide margin and still catch regressions.

## Considered options

- **Four requests, one per answer.** Each shape is plainer, but there are four shapes
  on six surfaces: the per-direction split ADR 0012 rejected for translate.
- **A kind of `translate`.** Translate's answer has no room for candidates, option
  text or paging.
- **Flat rows, one per application.** The same fitment would repeat across rows.
- **Whole-vehicle candidates.** About a thousand entries per vehicle. The part-type
  summary answers the question a caller actually has.
- **Refusing a request with no year.** Callers who know a model but not its year
  could then not page through its fitments.
- **Reverse only, leaving related vehicles to callers.** They would rebuild it from
  thousands of reverse answers.
- **`serve` only.** Every other request is on every surface, and a static MCP tool
  list depends on that.

## Consequences

- Stock answers ([#226](https://github.com/joshuasprow/nitrovin/issues/226)) attach to
  candidates and to related vehicles. That ticket decides how.
- [#239](https://github.com/joshuasprow/nitrovin/issues/239) builds the reader, and a
  request ticket builds `fit` on it.
