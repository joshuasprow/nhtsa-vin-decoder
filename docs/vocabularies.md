# Vocabularies

A **vocabulary** is one system's names for vehicles or part types: a
yard's inventory, a parts catalog's codes, a marketplace's categories.
nitrovin loads vocabularies as data, beside the vPIC data month, and
**translates** between them through a **spine**: vPIC's own makes and
models for vehicles, a spine you declare for part types. A VIN translates
too, through its decode. A **catalog** is a fourth kind of file, loaded
the same way: which parts fit which vehicles. This page is how to write
the files, load and check them, and translate and list on every surface.
The rules are the
[API contract](api/README.md#vocabularies---vocab); the file format is
[`vocabulary.schema.json`](api/vocabulary.schema.json); the words are
[GLOSSARY.md](../GLOSSARY.md#translation)'s; the reasons are ADRs
[0009](adr/0009-what-a-vocabulary-mapping-says.md),
[0010](adr/0010-part-types-translate-through-a-declared-spine.md),
[0011](adr/0011-vocabularies-are-json-lines-loaded-once.md),
[0012](adr/0012-translation-and-listing-are-requests-beside-decode.md),
and for catalogs [0016](adr/0016-what-a-fitment-says.md) and
[0017](adr/0017-the-catalog-file.md).

Matching is exact. nitrovin compares names after trimming them, collapsing
white space and lower-casing, and nothing else: no fuzzy matching, no
rules. Deciding that your `Chevy Silverado` is vPIC's Chevrolet Silverado
is the vocabulary author's work, done before the file is written.

The examples run from a checkout of this repository, with `nitrovin` built
from it on `PATH`, against the synthetic vocabularies in
[`testdata/vocab/`](../testdata/vocab) (a yard, a parts listing, its part
spine, an extension set of classics), the synthetic catalog in
[`testdata/vocab/fitment/`](../testdata/vocab/fitment) and EPA's
published vocabulary:

```sh
go build -o nitrovin ./cmd/nitrovin && export PATH="$PWD:$PATH"
```

## The four kinds

Every file is JSON Lines: a header line naming its `kind`, then one line
per entry. A file is one of four kinds:

| Kind | What it holds | Lines after the header | Fixture |
| --- | --- | --- | --- |
| `vocabulary` | A system's names (`entry`), each mapped to spine entries (`mappings`). `subject` `vehicle` maps onto vPIC; `subject` `part` onto the declared spine its header names | `{"entry": {…}, "mappings": […]}` | `yard.jsonl` (vehicle), `listing.jsonl` (part) |
| `extension-set` | Vehicles vPIC lacks, minted so vocabularies can map to them: a make, or a model under a vPIC make or an extension make | `{"id", "make_name"}` or `{"id", "make", "name"}` | `classics.jsonl` |
| `spine` | A declared part-type spine: every id, flat, names optional | `{"id", "name"}` | `parts.jsonl` |
| `catalog` | Which parts fit which vehicles: fitments with their applications, redirects and retired numbers ([below](#a-catalog)) | `{"fitment", "applications"}`, `{"redirect"}` or `{"retired", "to"}` | `fitment/catalog.jsonl` |

Names (`name` in the header) share one namespace across all four, and
`vpic` is reserved for the vehicle spine. Every object is closed: a field
the schema does not name refuses the file, so a typo never passes
silently.

## A vehicle vocabulary

[`testdata/vocab/yard.jsonl`](../testdata/vocab/yard.jsonl) begins:

```json
{"format":1,"kind":"vocabulary","name":"yard","version":"2026.11","data_month":"2026_10","subject":"vehicle","fields":["make","model"]}
{"entry":{"make":"Dodge","model":"Ram 1500"},"mappings":[{"model":1938,"last":2012,"qualifiers":{"34":["1500","DS (1500)"]},"origin":"curated"},{"model":13620,"first":2013,"origin":"curated","note":"Ram is its own make from 2013"}]}
{"entry":{"make":"Dodge","model":"Other"},"mappings":[{"make":476}],"note":"any Dodge"}
{"entry":{"make":"Studebaker","model":"Lark"},"mappings":[],"note":"listed, not yet placed"}
```

- **The header.** `format` is 1. `version` is yours, free text; every
  answer carries it beside the name. `data_month` is the vPIC month you
  built against, used only to explain drift. `fields` are the fields that
  name an entry, in order: a make and a model here, one code in a catalog.
  A model year is never a field.
- **An entry** has exactly the header's fields. Two entries equal after
  normalization refuse the file.
- **A mapping** names exactly one spine entry: `{"make": <vPIC make id>}`,
  `{"model": <vPIC model id>}` (it implies its make) or
  `{"ext": "ext:<set>/<id>"}`. An entry has as many mappings as it
  resolves to; `"mappings": []` lists an entry you have not placed yet,
  which translates as `unmapped` rather than `unknown`.
- **`first` and `last`** bound the model years a mapping holds for,
  inclusive; either may be absent.
- **`qualifiers`** narrow a mapping in vPIC's own terms: an element id and
  the values of it the mapping holds for, attribute ids for lookup
  elements (Drive Type `15`: `[2]`), exact text for text elements (Series
  `34`: `["DS (1500)"]`). Only a decode can satisfy one, so they narrow a
  translation from a VIN and never one from a name.
- **`origin`** (`derived`, `curated` or `seed`) and **`note`** are passed
  through to answers, never interpreted.

The ids and values come from the data. A decode in the `nitrovin` shape
carries every element id and attribute id:

```sh
nitrovin decode --shape nitrovin 1D7RV1GT5AS123456 | jq -c '.attributes | {Make, Model, Series, "Drive Type"}'
```

```json
{"Make":{"element_id":26,"value":"DODGE","attribute_id":476},"Model":{"element_id":28,"value":"Ram","attribute_id":1938},"Series":{"element_id":34,"value":"DS (1500)"},"Drive Type":{"element_id":15,"value":"4WD/4-Wheel Drive/4x4","attribute_id":2}}
```

and `list` pages through vPIC's makes, or one make's models:

```sh
nitrovin list vpic --limit 3
nitrovin list vpic --where make=476 --limit 3 | jq -c '.entries[]'
```

```json
{"model":14766,"name":"400","under":{"make":476}}
{"model":14416,"name":"600","under":{"make":476}}
{"model":14415,"name":"ARIES","under":{"make":476}}
```

A qualifier's values come from its model's own: `list` with a model and an
element gives every value a VIN of that model can decode the element to
([ADR 0015](adr/0015-model-values-for-an-element.md)), not every value of
the VIN schemas it shares with other models:

```sh
nitrovin list vpic --where model=1938 --where element=34 --limit 4 | jq -c '.entries[]'
```

```json
{"value":"1500"}
{"value":"2500"}
{"value":"3500"}
{"value":"3500W"}
```

### Extension sets

vPIC lacks vehicles from before 1981 and some marques. An extension set
mints the missing makes and models once, so that every vocabulary mapping
to them meets on the same spine entry.
[`testdata/vocab/classics.jsonl`](../testdata/vocab/classics.jsonl):

```json
{"format":1,"kind":"extension-set","name":"classics","version":"2026.11","data_month":"2026_10"}
{"id":"packard","make_name":"Packard"}
{"id":"packard-clipper","make":"ext:classics/packard","name":"Clipper"}
{"id":"corvair","make":467,"name":"Corvair"}
```

A vocabulary maps to one as `{"ext": "ext:classics/packard-clipper"}`.
No decode lands on an extension, so an extension mapping takes no
qualifiers and is reached from names only. When vPIC gains the vehicle,
map to its id instead; until then `vocab check` warns if an extension is
named like a vPIC make, or like a vPIC model under the same make.

## A part vocabulary and its spine

vPIC names no parts, so part types route through a spine you declare:
every part type, flat, by id.
[`testdata/vocab/parts.jsonl`](../testdata/vocab/parts.jsonl) is one;
[`testdata/vocab/listing.jsonl`](../testdata/vocab/listing.jsonl) maps a
listing's categories onto it:

```json
{"format":1,"kind":"spine","name":"parts","version":"2026.11"}
{"id":"brake-pads","name":"Brake Pads"}
{"id":"brake-pads-motorcycle","name":"Brake Pads, Motorcycle"}
```

```json
{"format":1,"kind":"vocabulary","name":"listing","version":"2026.11","data_month":"2026_10","subject":"part","fields":["category"],"spine":"parts"}
{"entry":{"category":"Brake Pads"},"mappings":[{"id":"brake-pads","qualifiers":{"39":[2,3,7]},"origin":"curated"},{"id":"brake-pads-motorcycle","qualifiers":{"39":[1]},"origin":"curated"}]}
```

A part vocabulary names exactly one spine, which must be loaded beside
it, and a translation never crosses from one spine to another. Where a
part type depends on the vehicle, say so with qualifiers in vPIC's terms,
as above (Vehicle Type `39`); a translation given a VIN as context checks
them. A spine's named entries are also a vocabulary of their names, under
the spine's name. Add an id to the spine rather than minting an
extension.

## A catalog

A catalog is one author's statement of which parts fit which vehicles. It
names the declared spine its part types come from and the vehicle
vocabulary its vehicles are entries of, and both must be loaded beside
it. A deployment loads at most one catalog.
[`testdata/vocab/fitment/`](../testdata/vocab/fitment) holds one, with
its spine (`part-types`) and its vehicle vocabulary (`catalog-models`).
`catalog.jsonl`:

```json
{"format":1,"kind":"catalog","name":"parts-catalog","version":"2026.11","release":"7.02","data_month":"2026_10","spine":"part-types","vehicles":"catalog-models"}
{"fitment":{"type":"mirror","number":"MR-1001","side":"left"},"note":"power, heated","applications":[{"vehicle":{"make":"Dodge","model":"Ram 1500"},"first":2009,"last":2012,"options":"w/o towing package"},{"vehicle":{"make":"Dodge","model":"Ram 1500"},"first":2009,"last":2012,"options":"4WD, w/ towing package","qualifiers":{"15":[2]}}]}
{"fitment":{"type":"mirror","number":"MR-1001","side":"right"},"note":"power, heated","applications":[{"vehicle":{"make":"Dodge","model":"Ram 1500"},"first":2009,"last":2012,"options":"w/o towing package"}]}
{"redirect":{"vehicle":{"make":"Chevrolet","model":"Silverado 1500"},"first":2007,"last":2013,"from":"brake-rotor-rear","to":"brake-rotor-front","options":"rear rotors are listed with the front"}}
{"retired":{"type":"brake-rotor-front","number":"BR-1999"},"to":{"type":"brake-rotor-front","number":"BR-2001"},"note":"7.01 → 7.02"}
```

- **The header.** `spine` names the declared spine. `vehicles` names a
  vehicle vocabulary, or is `vpic` when the catalog's vehicles are vPIC
  makes and models and extensions. `release` is the publisher's own
  release, separate from your `version`; it is optional, as is
  `data_month`.
- **A fitment** is one part: its key is the part type (an id of the
  spine), the catalog's `number` and an optional `side` (`left`, `right`
  or `both`). One number on two sides is two fitments, as `MR-1001` is
  above. A `note` is passed through.
- **An application** is one vehicle and year range the fitment fits.
  `vehicle` is an entry of the vehicle vocabulary by exactly its fields,
  matched after the same normalization as entries, or for `vpic` one of
  `{"make"}`, `{"model"}` or `{"ext"}` as a mapping names one. `first` and
  `last` are optional and inclusive. `options` is the catalog's own option
  text, verbatim, and required (`""` for none). `qualifiers` are as on a
  mapping, and checked only from a VIN. One vehicle in several year
  ranges, or with other option text, is several applications; two that
  are the same in every field refuse the file.
- **A redirect** says that for a vehicle and years the `from` part type
  is listed under the `to` part type. A request follows it one hop.
- **A retired number** points at the fitment that replaced it. It must
  not be a live fitment, and its `to` must be a fitment in the file: the
  author resolves chains.

The catalog's vehicles are its own names, translated at request time, so
a model your vocabulary has not mapped yet stays listed by name, and
curating the vocabulary never forces a catalog rebuild. A catalog that
knows vPIC ids declares `vpic` and skips the translation.

Beyond the faults every file has, a catalog is refused for: a second
catalog; its spine or vehicle vocabulary not loaded; a part type the spine
lacks, in a fitment or a redirect; a vehicle whose fields are not exactly
the vocabulary's, or an extension no loaded set defines; qualifiers on an
application whose vehicle is an extension, which no decode lands on; a
side other than `left`, `right` or `both`; a fitment key, an application
within a fitment, or a retired number given twice; a retired number that
is live or points at no fitment. Its drift, which is served: a vehicle the
vocabulary lacks or lists without a mapping (kind `vehicle`); a vPIC id
the month lacks (`make`, `model`); for a `vpic` catalog, an application
to a vPIC model in years outside the model's (`model-years`, ADR 0022); a
qualifier the month's decode cannot satisfy (`qualifier`); a redirect
whose `to` part type has no application for its vehicle in overlapping
years (`redirect`). A catalog names each vehicle on many lines, so its
drift is one finding per vehicle, vPIC id, qualifier value or model and
year range, at the first application or redirect naming it, with
`(and N more)` counting the other applications or redirects that do; and
one per redirect.

`/v1/info` lists a catalog with its `release` and its counts:

```sh
nitrovin --vocab testdata/vocab/fitment info | jq -c '.vocabularies[] | select(.kind == "catalog") | del(.path, .blake3)'
```

```json
{"name":"parts-catalog","kind":"catalog","version":"2026.11","release":"7.02","drift":[],"fitments":5,"applications":7,"redirects":1,"retired":1}
```

Translate reads only vocabularies, extension sets and spines: a catalog
is not in `list`'s catalog of what is loaded, and naming it as a `to` is
refused. **`fit`** reads the catalog
([the API's Fit section](api/README.md#fit-fit), ADR 0018): from a vehicle
its part types with counts, with a `part` its candidates, with `related`
the vehicles that share its fitments, and from a fitment key its vehicles.
Nothing to fit is a status, `no-catalog` included. `list` with `of` the
catalog's name pages its fitment lines, `where.type` keeping one part
type. The synthetic catalog's `part-codes.jsonl` is a part vocabulary on
its spine, whose `ROT` code files both rotors under one code, so a fit
can name a part type in a parts system's words:

```sh
nitrovin --vocab testdata/vocab --vocab testdata/vocab/fitment fit --part part-types:mirror --side left 1D7RV1GT5AS123456
echo '{"from":{"model":1850},"model_year":2010,"part":{"vocabulary":"part-codes","entry":{"code":"ROT"}}}' |
  nitrovin --vocab testdata/vocab --vocab testdata/vocab/fitment fit -
```

The browser neither loads a catalog nor is
offered the server's, and `serve` never publishes one. In Go, a loaded
catalog is a `fitment.Reader` ([`pkg/fitment`](../pkg/fitment)), the one
interface every consumer reads it through (ADR 0017): `ByVehicle`,
`ByNumber`, `Redirect`, `Fitments` (a page of fitments in key order) and
`Info`. Its vehicles are the catalog's own; `fitment.Vehicles` turns a
translation to the catalog's vehicle vocabulary into them:

```go
t, _ := nitrovin.NewTranslator(d, files...) // files from vocab.Open("testdata/vocab/fitment")
cat := t.Catalog()                           // nil when no catalog is loaded
ram := vocab.Vehicle{Entry: map[string]string{"make": "Dodge", "model": "Ram 1500"}}
for _, c := range cat.ByVehicle("mirror", []vocab.Vehicle{ram}, new(2010), nil) {
	fmt.Println(c.Fitment.Key.Number, c.Fitment.Key.Side, len(c.Applications)) // MR-1001 left 2, MR-1001 right 1
}
fit, retired := cat.ByNumber("brake-rotor-front", "BR-1999", vocab.SideNone) // BR-2001, through the retired number
```

### Compiling a catalog

A catalog at a parts system's scale takes seconds and hundreds of MB to load
as JSON Lines. `nitrovin vocab compile` checks it as `vocab check` does and
writes it as a compiled catalog (ADR 0021): one indexed file that opens in
milliseconds with a few MB, read in place, answering exactly as its source.
Compile at image build, with the binary that will serve it:

```sh
nitrovin vocab compile -o catalog.compiled testdata/vocab/fitment   # the catalog, its spine and vehicles
nitrovin --vocab testdata/vocab/fitment/part-types.jsonl --vocab testdata/vocab/fitment/catalog-models.jsonl \
  --vocab catalog.compiled info | jq -c '.vocabularies[2].compiled'
```

```json
{"format":1,"source_blake3":"…","writer":"v…","drift_month":"2026_10","drift_month_differs":false,"links_differ":false}
```

A fault refuses the compile, and nothing is written; drift is written to
stderr and stored in the file, `model-qualifier` included. `--vocab` recognises the file by its magic
(name it itself: a directory loads only its `*.jsonl`) and refuses a
compiled format the release cannot read, and a spine or vehicle vocabulary
that is not loaded or lacks a part type or vehicle it was compiled with.
Loading it does not measure drift again in full: `/v1/info` shows the stored
drift, the month it was measured against, and `drift_month_differs` when
that is not the loaded month. Its `vehicle` drift alone is measured at load,
against the vehicle vocabulary loaded, which `links_differ` says is not the
one it was compiled with. `nitrovin vocab check` on the compiled file reads it
in full and checks and measures it again, as its source. JSON Lines stays
the published, diffable form; the compiled file is a deployment's, and the
same source, data month and nitrovin version always compile to the same
bytes.

## Loading

`--vocab <file|dir>`, before or after any command and repeatable, loads
files and directories (every `*.jsonl` directly in one, in name order).
Without it, `NITROVIN_VOCAB` is a path list (`:`-separated, `;` on
Windows). Files load once, with the data, and never change while nitrovin
runs; restart `serve` to pick up a new file.

```sh
nitrovin --vocab testdata/vocab info | jq -c '.vocabularies[] | {name, kind, version, drift}'
NITROVIN_VOCAB=testdata/vocab nitrovin list | jq -c '.entries[] | {name, kind, spine}'
```

`/v1/info` (and `nitrovin info`) lists every loaded file with its version,
blake3 hash, absolute path and drift, and a catalog with its release and
counts.

## Checking

nitrovin tells two kinds of problem apart:

- A **fault** is the file's own mistake, whatever the month: a malformed
  line, an unknown field, two entries that normalize alike, a first year
  after the last, a reference no loaded file defines. A fault stops every
  command before it answers, naming the file and line.
- **Drift** is where a file disagrees with the loaded data month: a vPIC
  id NHTSA has since dropped, a qualifier value the month never decodes, or
  one the month decodes but never for the mapping's own vPIC model (a
  Series only the van line has, on a pickup), an extension named like a
  vPIC entry. NHTSA's monthly changes are not your
  mistakes, so drift is a warning on stderr and in `/v1/info`, and the
  file is served as written. The per-model check (`model-qualifier`)
  costs about 0.14 s and 80–100 MB of memory for an EPA-sized vocabulary,
  so it is left out of load's stderr warnings: `nitrovin vocab check`
  runs it, and `/v1/info` counts it from its first request.

`nitrovin vocab check` loads only its arguments, prints every finding as
a JSON line on stdout and exits 1 on any, fault or drift, so CI can gate a
vocabulary on the month it will run against:

```sh
nitrovin vocab check testdata/vocab            # nothing printed, exit 0
nitrovin vocab check testdata/vocab/drift      # seven drift lines, exit 1
nitrovin vocab check testdata/vocab/fitment    # a catalog with its spine and vehicles: nothing, exit 0
nitrovin vocab check testdata/vocab/fitment/catalog.jsonl   # alone: two faults, exit 1
```

```json
{"file":".../testdata/vocab/drift/yard.jsonl","line":4,"severity":"drift","detail":"/mappings/0/make: vPIC make 99999 is not in data month 2026_10"}
```

A fault:

```sh
printf '%s\n' '{"format":1,"kind":"vocabulary","name":"bad","version":"1","subject":"vehicle","fields":["make"]}' \
  '{"entry":{"make":"Dodge"},"mappings":[{"make":476,"first":2014,"last":2012}]}' > bad.jsonl
nitrovin vocab check bad.jsonl
nitrovin --vocab bad.jsonl info    # refuses to start: exit 1
```

```json
{"file":".../bad.jsonl","line":2,"severity":"fault","detail":"/mappings/0: first year 2014 is after last year 2012"}
```

Check against the data you will serve: `--data` picks the month for
`vocab check` as for any command.

## Translating

A translation starts `from` a VIN, a vocabulary entry or a spine entry,
reaches the spine, and answers the entries of the `to` vocabularies that
map there. Without `to` it answers the spine alone.

```sh
nitrovin --vocab testdata/vocab translate --to yard 1D7RV1GT5AS123456 | jq -c '.to[0].entries[].entry'
```

```json
{"make":"Dodge","model":"Other"}
{"make":"Dodge","model":"Ram 1500"}
{"make":"Dodge","model":"Ram 1500 4x4"}
```

The VIN decodes to a 2010 Dodge Ram, Series `DS (1500)`, four-wheel drive,
so every mapping's year range and qualifiers hold. The 2015 Ram
(`1C6RR7LT5FS123456`) reaches the same two `Ram 1500` names through model
13620 instead, and `Ram / Other` through make 496.

- **`spine`** is every spine entry reached, once each, a model with the
  make it sits `under`; a vPIC id the month lacks comes back with
  `"drift": true`.
- **`via`** on each spine and target entry is the mappings that led there,
  with their `first`, `last`, `origin` and `note`.
- **`unconfirmed`** lists what could not be checked: qualifier element ids
  the decode left empty, and `model_year` when no year was known. Keep
  only entries without `unconfirmed` to keep only confirmed results.
- **`from.status`** is `mapped`, `unmapped` (listed without mappings) or
  `unknown` (not in the vocabulary, or a VIN that decodes to no make or
  model). Nothing to translate is an answer, never an error; an error is
  only for a request the loaded files cannot read, such as a `to` nothing
  loaded has.

From an entry, a translation checks year ranges against `model_year` and
ignores qualifiers, so it returns the union of the entry's mappings. Give
a VIN as context (`vin`) to check them against its decode:

```sh
nitrovin --vocab testdata/vocab translate - <<'EOF' | jq -c '{from: .from.status, spine: [.spine[] | .name]}'
{"from": {"vocabulary": "yard", "entry": {"make": "Packard", "model": "Clipper"}}, "model_year": 1955}
{"from": {"vocabulary": "yard", "entry": {"make": "Studebaker", "model": "Lark"}}}
{"from": {"vocabulary": "yard", "entry": {"make": "Nash", "model": "Rambler"}}}
{"from": {"vocabulary": "listing", "entry": {"category": "Brake Parts"}}, "to": ["listing"], "vin": "1D7RV1GT5AS123456"}
EOF
```

```json
{"from":"mapped","spine":["Clipper","Packard"]}
{"from":"unmapped","spine":[]}
{"from":"unknown","spine":[]}
{"from":"mapped","spine":["Brake Pads","Brake Pads, Motorcycle","Brake Rotor"]}
```

In the last answer, the listing's `Brake Pads` entry comes back through
`brake-pads` but not through `brake-pads-motorcycle`: the VIN's Vehicle
Type rules that mapping out.

## Listing

`list` with nothing names what is loaded: vPIC, then every file in load
order. With a name it pages through that vocabulary's entries and their
mappings, a spine's or extension set's ids, or vPIC's makes; `where`
filters by the listing's fields after the same normalization, and `next`
is the cursor for the following page.

```sh
nitrovin --vocab testdata/vocab list
nitrovin --vocab testdata/vocab list yard --where make=dodge --limit 2 | jq -c '[.entries[].entry], .next'
nitrovin --vocab testdata/vocab list yard --where make=dodge --limit 2 --after WyJkb2RnZSIsInJhbSAxNTAwIl0 | jq -c '[.entries[].entry]'
```

## On every surface

Translate and list are the same two request objects everywhere
([Translation](api/README.md#translation-translate),
[Listing](api/README.md#listing-list)), answered from whatever `--vocab`
or `NITROVIN_VOCAB` loaded; with nothing loaded they answer from vPIC
alone.

**CLI**: `nitrovin translate <vin>…` or `-` for one request object per
line, `nitrovin list [of]`, both above.

**HTTP**: `POST /v1/translate` takes an array of up to 1,000 requests;
`GET /v1/translate` takes one as dotted query parameters; `GET
/v1/vocabularies` is the catalog and `GET /v1/vocabularies/{name}/entries`
a listing.

```sh
nitrovin --vocab testdata/vocab serve    # 127.0.0.1:8080; then, from another shell:
```

```sh
curl 'localhost:8080/v1/translate?from.vin=1D7RV1GT5AS123456&to=yard'
curl 'localhost:8080/v1/translate?from.vocabulary=yard&from.entry.make=Dodge&from.entry.model=Ram%201500&model_year=2015'
curl localhost:8080/v1/translate -d '[{"from": {"model": 1850}, "to": ["yard"]}, {"from": {"spine": "parts", "id": "brake-rotor"}, "to": ["listing"]}]'
curl localhost:8080/v1/vocabularies
curl 'localhost:8080/v1/vocabularies/yard/entries?where.make=dodge&limit=2'
```

`serve` also publishes each loaded file but a catalog, byte for byte, for a browser that
translates locally
([ADR 0013](adr/0013-browser-vocabularies-in-a-service-worker.md)), at
`/ui/r/<release>/vocab/<blake3>/<name>.jsonl`, where the release is
`/v1/info`'s `decoder_version`, `data_month` and `artifact_hash` joined by
`/` and the blake3 is the file's there; beside it the data month's spine
bundle at `/ui/r/<release>/spine.json`. Both are immutable for a year: a
new release or a changed file is a new URL. This exposes nothing new, since
`/v1/vocabularies/{name}/entries` already lists every line. `serve` reads a
file again when it is asked for, and refuses with a 500 a file that
changed on disk since it was loaded: restart `serve` to publish the new
one.

**In a browser**: `serve`'s page at `/` decodes a VIN and translates,
through the server's files, with JavaScript or without. Its controls
(Translate from) have a tab for each thing to translate from: VIN ·
Year, make and model (an entry of a vehicle vocabulary) · one for each
declared spine, named by what its vocabularies name (Part (parts spine):
an entry of its vocabularies) · vPIC id (a make or model id, or an
extension when a map's card asks for one). Each tab's form shows what
the page's URL asks, a declared spine's own entry read-only, and a
refusal is said beside the field holding the value it names, else above
the form. Older links (`/?vin=…`, `/ui/decode`, `/ui/translate`, `/vocab`,
`/ui/vocabularies`) redirect to it (`301`). Below the controls the answer
is drawn as a map: what it is from, the spine entries it crosses and the
entries it reaches as cards in three columns, each mapping a line. Its
key: a mapping; a mapping matched by rule, dashed; a model under its
make; a VIN to the model it decodes to; and a card marked when not every
qualifier or the model year was checked. Each card's text says what its
lines draw (its make, what it was reached through and how), so a screen
reader reads it with the card's name; a name shows on two lines, and
whole while its card is under the pointer or focused. On a phone the
columns stack, the lines and their key are hidden, and each card shows
that text instead. Every card is a link to the page from it (the From
card to the answer's own URL); with JavaScript the card under the
pointer or focus lights the routes through it, and a button shows the
translation's JSON. Back and forward ask the server for each page again,
never a copy the browser kept. The Vocabularies button in the page's
header opens a dialog that
lists what is loaded, each file marked by where it is kept, with its
drift counted by kind as `/v1/info` counts it (the first examples behind
a disclosure), each linked to its entries (`/ui/vocabularies/{name}`, a
page that carries the same dialog).
With JavaScript the dialog also loads your own files,
picked from disk or dropped anywhere on the page, into the browser: a
Service Worker (`/sw.js`), started when the dialog first opens or a file
is dropped, and at page load when one is already registered, keeps
each file's text and parsed index in
IndexedDB and runs the same pages, built from the same Go to WebAssembly,
over them and the server's files, which it links from the URLs above.
The dialog then lists one catalog: vPIC, the server's files and yours, each
marked as built in, on this server or in this browser, yours with their
file name, size and when they were stored, and how much the site keeps in
the browser. Files loaded together load as one batch, which must link
(with the server's files and those already stored), and a file named like
a stored one replaces it; a file named like one of the server's is
refused, and so is a file with a fault, naming the file and line. Removing
a file others link to is refused, naming them, with an offer to remove
them all. A stored file that this release can no longer read, or that no
longer links, is listed with its fault and can be removed. The worker
answers only a request naming a file the browser holds (what it is from,
or a target `to=`), and the entry lists of those files, and leaves
every other request to the server (for a VIN it answers itself, the
decode is `/v1/decode`'s), so a bare link such as `/?from.vin=…` is the
server's answer and means the same with JavaScript on or off and in every
browser; opened in another browser, a link naming your file
says where it lives. The targets you tick are your selection, kept beside
your files and ticked into the forms, so what you ask next names them as
an explicit `to=`. Answers carry the `decoder_version` and `data_month`
of the decode they used. When the server moves to a new release, the
worker steps aside: every page comes from the server, your files are
paused, and Vocabularies says so. Once the new release's worker is ready,
**Update now** hands the page to it and reloads, and your files and
selection stay; without the click, the new worker takes over once every
tab of the site is closed. Every page's header names the release that
drew it. A page loaded with
a hard reload is not the worker's, and says your files are paused for
that load. Release builds also offer [EPA's vocabulary](#epas-vocabulary)
of their data month as a one-click load: `serve` publishes it, byte for
byte as nitrovin-data's release has it, at
`/ui/r/<release>/vocab/<blake3>/epa-vehicles.jsonl`, and the browser keeps
it like a file of yours.

**stdio**: a line with `"op": "translate"` or `"op": "list"` carries that
request, and its reply is keyed `translation` or `list`:

```sh
printf '%s\n' '{"id":1,"op":"translate","from":"1D7RV1GT5AS123456","to":["yard"]}' \
  '{"id":2,"op":"list","of":"parts"}' | nitrovin --vocab testdata/vocab stdio
```

**MCP**: the `translate` and `list` tools take the same objects as
arguments, over stdio (`nitrovin --vocab <path> mcp`; put `--vocab` and an
absolute path in the client's `args`) or at `serve`'s `/mcp`:

```sh
npx @modelcontextprotocol/inspector --cli http://127.0.0.1:8080/mcp --transport http \
  --method tools/call --tool-name translate --tool-arg from=1D7RV1GT5AS123456 --tool-arg 'to=["yard"]'
npx @modelcontextprotocol/inspector --cli http://127.0.0.1:8080/mcp --transport http \
  --method tools/call --tool-name list --tool-arg of=yard --tool-arg limit=2
```

The tool descriptions never name what is loaded; an assistant calls
`list` with no arguments to find out.

**Go**: `vocab.Open` reads and links files, refusing faults;
`nitrovin.NewTranslator` measures their drift against the decoder's data
month and answers `Translate` and `List`.

```go
package main

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

func main() {
	d, err := nitrovin.Embedded()
	if err != nil {
		log.Fatal(err)
	}
	files, err := vocab.Open("testdata/vocab") // files and directories, as --vocab takes them
	if err != nil {
		log.Fatal(err) // vocab.Faults: every fault, by file and line
	}
	t, err := nitrovin.NewTranslator(d, files...) // drift is in t.Drift(), served anyway
	if err != nil {
		log.Fatal(err)
	}
	req, err := contract.ParseTranslateRequest([]byte(`{"from": "1D7RV1GT5AS123456", "to": ["yard"]}`))
	if err != nil {
		log.Fatal(err)
	}
	tr, err := t.Translate(req) // an error only for a request the loaded files refuse
	if err != nil {
		log.Fatal(err)
	}
	for _, e := range tr.To[0].Entries {
		out, _ := json.Marshal(e.Entry)
		fmt.Println(string(out))
	}
}
```

**C**: `nitrovin_open_vocab` opens an artifact with vocabularies beside
it; `nitrovin_decode` then answers translate and list lines exactly as
`nitrovin stdio` does ([the C library](c-library.md#vocabularies)):

```c
#include <stdio.h>
#include "nitrovin.h"

int main(void) {
  const char *vocab[] = {"testdata/vocab"};
  char *err = NULL;
  nitrovin_t *h = nitrovin_open_vocab("nitrovin-2026_10.artifact", vocab, 1, &err);
  if (h == NULL) {
    fprintf(stderr, "%s\n", err); /* a fault: the file and line */
    nitrovin_free(err);
    return 1;
  }
  char *reply = nitrovin_decode(h,
      "{\"id\":1,\"op\":\"translate\",\"from\":\"1D7RV1GT5AS123456\",\"to\":[\"yard\"]}");
  puts(reply); /* {"id":1,"translation":{...}} */
  nitrovin_free(reply);
  reply = nitrovin_decode(h, "{\"id\":2,\"op\":\"list\",\"of\":\"yard\",\"limit\":2}");
  puts(reply); /* {"id":2,"list":{...}} */
  nitrovin_free(reply);
  nitrovin_close(h);
  return 0;
}
```

```sh
gh release download v1.202610.2 -R joshuasprow/nitrovin-data -p nitrovin-2026_10.artifact
cc -Wall -Wextra -o translate translate.c -L. -lnitrovin -Wl,-rpath,. && ./translate
```

A library has no stderr, so `nitrovin_info` is where a handle's drift
shows.

## EPA's vocabulary

Each [nitrovin-data release](https://github.com/joshuasprow/nitrovin-data/releases)
carries `epa-vehicles.jsonl`: the make and model names of EPA's
fuel-economy data
([vehicles.csv](https://www.fueleconomy.gov/feg/download.shtml), public
domain) mapped onto that month's vPIC makes and models, with Drive Type
and Series qualifiers where a name implies them. `cmd/nitrovin-epa`
builds it by rules plus hand-curated overrides (origins `derived` and
`curated`), and the monthly refresh publishes it only when it loads with
no fault. It loads like any vocabulary:

```sh
curl -fsSLO https://github.com/joshuasprow/nitrovin-data/releases/latest/download/epa-vehicles.jsonl
nitrovin --vocab epa-vehicles.jsonl translate --to epa-vehicles 1C6RR7LT5FS123456 | jq -c '.to[0].entries[].entry'
nitrovin --vocab testdata/vocab --vocab epa-vehicles.jsonl translate - <<'EOF' | jq -c '.to[0].entries[].entry'
{"from": {"vocabulary": "yard", "entry": {"make": "chevy", "model": "silverado"}}, "to": ["epa-vehicles"], "model_year": 2004}
EOF
```

The VIN's four-wheel drive narrows the 2015 Ram to EPA's `1500 4WD`; the
yard's `Chevy Silverado`, a name with no decode to check qualifiers
against, reaches every EPA name mapped to vPIC's Silverado for 2004.

## Keeping a vocabulary private

nitrovin reads vocabulary files from the paths you give it and sends them
nowhere (a file loaded in the page's Vocabularies dialog stays in that browser: only a VIN to
decode goes to the server), and an answer names a file only by its `name` and `version`
(`/v1/info` adds its hash and local path). nitrovin ships no third-party
vocabulary but EPA's, and the hosted [nitrovin.app](https://nitrovin.app)
loads none: there, the API's translate and list answer for vPIC alone, and
the page offers EPA's to load into your browser. A vocabulary
built from licensed names (a parts catalog, a marketplace's compatibility
data) is yours to build and load under your own licence, and stays on the
machines you load it on. A catalog stays on its server: `serve` neither
publishes it nor offers it to the browser.
