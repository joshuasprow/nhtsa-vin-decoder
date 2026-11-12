# nitrovin

A decoder that turns a VIN into the vehicle NHTSA's vPIC says it is, shipped as
one binary with its data inside. It translates names and answers fitment from
files it loads, and reads stock from a database only when a stock source says to.

## Language

### Decoding

**VIN**:
The vehicle identification number a caller gives us: 17 characters when
whole, though partial (`*`) and mistyped ones decode too, with error codes.
_Avoid_: serial, chassis number

**WMI**:
The world manufacturer identifier: the VIN's first three characters, or six
when the third is `9`. The unit vPIC's tables are keyed by.
_Avoid_: manufacturer code, prefix

**Element**:
One decodable property of a vehicle, identified by vPIC's numeric element id,
named by its variable name ("Body Class") and coded by its element code
(`BodyClass`), the key of vPIC's flat output. The id is stable across data
months; the name and code are not.
_Avoid_: field, variable (as the identity), column

**Attribute**:
The value an element resolves to for one VIN, carrying vPIC's attribute id
where the element is a lookup.
_Avoid_: value (when the id matters)

**Multi-valued element**:
An element that NHTSA's procedure can emit more than once for one VIN, every
value kept, repeats included (Note, Other Restraint System Info, and Other
Battery Info, which Vehicle Specs can add beside a pattern's). Every other
element resolves to at most one.
_Avoid_: list field, repeating variable

**Item**:
One row the oracle emits for one VIN: an element, its attribute, and where the
attribute came from (source, pattern, schema, keys).
_Avoid_: row, decoding item, provenance entry

**Decode**:
The full result for one VIN: model year, error codes, check-digit validity,
the attributes of every decoded element, and on request its items.
_Avoid_: lookup, result object

**Error code**:
One of vPIC's numeric codes stating what is wrong with a VIN (0 clean, 1 check
digit, 11 bad year code, 400 bad character…).
_Avoid_: validation error, warning

**VIN schema**:
vPIC's group of patterns for one family of VINs. WMIs reach it through a model-year band.
One schema may describe several models.
_Avoid_: schema (alone, where a database schema could be meant), vinschema (in prose)

**Pattern**:
One row of a VIN schema. It holds a key over VIN positions and one element's attribute, and
applies to every VIN its key matches. Of the patterns that match a VIN for one element, the
decoder keeps one.
_Avoid_: rule, mask

**Charset**:
The characters a VIN may hold at each position for one WMI and model year, as
the dump's pattern rows allow. A character outside it is an error the decode
reports with a suggested VIN and the possible values.
_Avoid_: valid chars, the valid-characters cache (the dump's stale copy of it)

**Model-year hint**:
A model year the caller already knows, which competes with the year the VIN
encodes and flags error code 12 when they disagree.
_Avoid_: year override, stated year

**Pass**:
One attempt at decoding a VIN under one candidate model year: the year the
VIN encodes, 30 years earlier when that year is ambiguous, or the model-year
hint. A decode runs one to three passes and keeps the best-scored one.
_Avoid_: attempt, round, iteration

### Data

**Dump**:
NHTSA's monthly publication of the vPIC database, a Postgres custom-format
backup named by its data month.
_Avoid_: backup, release (of NHTSA's)

**Data month**:
The `YYYY_MM` that names a dump. Every decode and every artifact carries the
one it came from.
_Avoid_: version (of the data), vPIC release

**Artifact**:
The compact, deterministic file this project builds from a dump, which the
binary embeds or loads with `--data`.
_Avoid_: database, tables, snapshot

### Correctness

**Oracle**:
An implementation whose answers define correct: NHTSA's `spVinDecode` in
Postgres, run as the freshened oracle, on every PR and every refresh.
ultravin is the reference implementation, not an oracle.
_Avoid_: reference, golden (and never call live vPIC an oracle)

**Freshened oracle**:
The Postgres oracle on a copy of the dump whose derived valid-characters cache
is emptied, so every charset comes from the dump's own pattern rows. Where it
and the stock oracle disagree, the dump contradicts itself.
_Avoid_: patched oracle, fixed dump

**Stale cell**:
A (WMI, model year) cell of the dump's valid-characters cache that disagrees
with what the same dump's pattern rows allow.
_Avoid_: bad cache row, cache bug

**Live vPIC**:
NHTSA's production decode service (`vpic.nhtsa.dot.gov`), running on SQL
Server at whatever data it holds today. A witness for deviations, never an
oracle: its month is not a dump's, and its answers cannot be replayed.
_Avoid_: the API, NHTSA's decoder

**Parity**:
Byte-for-byte agreement with the oracle on every element of every VIN in a
corpus, except on registered deviations.
_Avoid_: accuracy, match rate

**Deviation**:
A class of VINs on which the decoder deliberately disagrees with the oracle
because the oracle's own data or procedure is shown to be defective. Each
class is registered with its root cause, its detector and its witnesses; it is
healed in a month its detector finds nothing.
_Avoid_: known issue, exception, bug, known problem

**Detector**:
The mechanical test, run on every refresh, that finds which VINs of a corpus
belong to a deviation, derived from the dump and the oracle and never from the
decoder's output.
_Avoid_: excuse rule, classifier

**Witness**:
A VIN that exhibits a deviation, on which live vPIC was seen answering the way
the decoder does, on a recorded date.
_Avoid_: example, repro, test case

**Corpus**:
A set of VINs with the oracle's answers, used to measure parity.
_Avoid_: golden set, fixtures

**Answer key**:
The oracle's answers for a list of VINs on one data month: everything it
says about each VIN. A corpus's answers are an answer key.
_Avoid_: golden file, expected output, snapshot

**Gate**:
A parity check that must pass before a change merges or a data month ships.
_Avoid_: test (for this specific role), check

### Delivery

**Surface**:
One way a program reaches the decoder: CLI, HTTP, stdio JSON lines, MCP, the
Go module, the C-shared library.
_Avoid_: transport, interface, frontend

**Refresh**:
The unattended run that notices a new dump, builds its artifact, runs the
gates, and ships a release.
_Avoid_: sync, update

**Publisher**:
One channel a release goes out through: the GitHub release, the image
(`docker`), the Homebrew tap (`homebrew`), the Scoop bucket (`scoop`) and
winget (`winget`), and signing (`sign`) beside them.
_Avoid_: target, destination

**Retry** (of a release):
release.yml dispatched with a tag whose release went out in part: it rebuilds
the tag, requires the published bytes, and runs only the publishers that did
not publish. It completes a release; it never replaces one or makes a version.
_Avoid_: re-release, re-tag, rerun

### Translation

**Vocabulary**:
One system's names for vehicles or parts (a yard's inventory, a parts
catalog, a marketplace's listings), supplied to nitrovin as data, never as
code. vPIC's own makes and models are the one vocabulary nitrovin carries.
_Avoid_: plugin, mapping file, dictionary

**Entry**:
One named thing in a vocabulary: a make, a make and model, a part type. Each
vocabulary declares the fields that name its entries (make and model; a
catalog's own code); a model year is never one of them.
_Avoid_: key, alias, row

**Unmapped entry**:
An entry a vocabulary lists without any mapping: known to it, but not yet
placed on the spine. Distinct from a name the vocabulary does not have at all.
_Avoid_: unresolved, orphan

**Spine**:
The vocabulary a translation routes through. For vehicles it is always vPIC's
makes and models, identified by their ids (a model implies its make). For
part types it is a declared spine.
_Avoid_: canonical list, master data

**Declared spine**:
A part-type spine its author supplies: a name and every entry in it, flat,
with or without names. Each part vocabulary maps onto exactly one, and a
translation crosses exactly one. Its author adds entries rather than minting
extensions.
_Avoid_: custom taxonomy, part spine file

**Spine bundle**:
A data month's vPIC side of translation as one JSON document: its makes,
every model, its elements and the values its decode can give each,
so a translation can run where the artifact is not, in a browser. `serve`
publishes it per release; its format belongs to the release that wrote it.
_Avoid_: spine file, month dump

**Mapping**:
A stated correspondence from one entry to one spine entry (a vPIC make or
model, an extension, or an entry of a declared spine), scoped to the model
years it holds for and optionally narrowed by qualifiers. An entry carries as
many mappings as it resolves to.
_Avoid_: alias, crosswalk

**Qualifier**:
A condition on a mapping, stated in vPIC's own terms: an element and the
values of it the mapping holds for (Drive Type 4WD; Series "DS (1500)"). Only
a decode can satisfy one, so it narrows a translation from a VIN and never one
from a name.
_Avoid_: trim (as the general term), filter, attribute (an attribute is the
decode's value; a qualifier is the mapping's condition on it)

**Model values**:
Every value a decode with a model year gives one element for a VIN of one vPIC model,
except a value a formula pattern computes from the VIN's characters; it may list values no
VIN of the model ends up with. It is worked out from the patterns whose keys could match
together with the model's own, in the model's VIN schema or one its WMI code reaches in an
overlapping year band, and from the model's specs, defaults and conversions.
_Avoid_: model attributes, series list

**Model years**:
The model-year ranges a vPIC model's VIN schemas cover, gaps kept and an open
range still running. When no VIN schema with a Model pattern naming the model has a year band, it has
none: unknown, not empty.
_Avoid_: span (one range hides the gaps), production years

**Extension**:
A spine entry an author mints for a vehicle vPIC lacks: a make, or a model
under a vPIC make or an extension make. It stands in until the author maps to
a real vPIC entry instead; no decode ever lands on one.
_Avoid_: custom model, fake id

**Extension set**:
A named collection of extensions, loaded beside vocabularies and shared by
them, so that two vocabularies' mappings to the same missing vehicle meet.
_Avoid_: extension vocabulary (it names spine entries, not a system's names)

**Vocabulary builder**:
A program that writes a vocabulary from a system's own data, matching its
names to the spine by rules (mappings of origin `derived`) and applying
**curated overrides**, hand-written lines that replace an entry's derived
mappings (origin `curated`). Rules run only in a builder, never in a
translation. `cmd/nitrovin-epa` builds EPA's.
_Avoid_: importer (that builds the artifact), generator

**Translation**:
Resolving an entry of one vocabulary to entries of another through the
spine. May be one-to-many in either direction.
_Avoid_: lookup, aliasing

**Drift**:
Where a vocabulary, extension set, declared spine, catalog or stock file
disagrees with the loaded data month or the files loaded with it: a vPIC id the month lacks,
a qualifier value its decode cannot
produce, or one the mapping's own model never decodes to, an extension named like a vPIC entry under the same make,
a mapping's or a catalog application's years outside its model's model years,
a catalog's vehicle its vehicle vocabulary lacks or lists unmapped, a redirect
no application answers, an orphan shelf row, a personal-looking stock field.
Reported,
never refused: NHTSA's monthly changes are not the author's mistakes.
_Avoid_: stale, invalid, broken mapping

**Fault**:
What refuses a vocabulary, extension set, declared spine, catalog or stock
file whatever the data month: a malformed line, an unknown field, a duplicate
entry, id or fitment key, a reference no loaded file defines, a second
catalog, a retired number that is live or points at no fitment, a shelf whose
catalog is not loaded, a stock row's part or vehicle the catalog's files lack. A fault stops nitrovin from
starting; drift never does.
_Avoid_: error (a decode's error codes are about the VIN), invalid

### Fitment

**Catalog**:
One author's statement of which parts fit which vehicles, loaded as data. It
names the declared part spine its part types come from and the vehicle
vocabulary its applications use. A nitrovin loads one, on its server or in a
visitor's browser, never both.
_Avoid_: interchange (a catalog vendor's term), parts database; "catalog" for
`list`'s list of what is loaded (`CatalogEntry`, ADR 0012), which names
files, not fitments: say "what is loaded" or "the list of loaded files"

**Compiled catalog**:
A catalog checked and written into one indexed file, by `nitrovin vocab compile`
or in a visitor's browser, that opens in milliseconds and is read in place
(ADR 0021, ADR 0023). It answers exactly as its JSON Lines source, which stays the
published form; its drift is the drift measured when it was compiled, and a
browser's compile measures every kind but those its spine bundle cannot.
_Avoid_: binary catalog, catalog database, index (an index is one of its tables)

**Browser's catalog**:
The one compiled catalog a visitor's browser holds, compiled there from JSON
Lines they picked, in a dedicated worker the page starts, and kept in
IndexedDB (ADR 0023). The Service Worker answers the fit page from it; a
browser holds none while its server has a catalog.
_Avoid_: uploaded catalog (nothing is uploaded), local catalog

**Scratch storage**:
Where compiling a catalog sorts what memory does not hold, and assembles the
compiled file before writing it (ADR 0023): a temporary directory for
`nitrovin vocab compile`, IndexedDB in a browser. Written once and read back,
then deleted.
_Avoid_: cache, temp files (a browser has none)

**Fitment**:
One part in a catalog: a part type, the catalog's number for it and its side,
with every vehicle and model-year range it fits.
_Avoid_: interchange, compatibility, part record

**Application**:
One vehicle and model-year range a fitment fits, with the catalog's option text
for it and, optionally, qualifiers.
_Avoid_: fit, usage, vehicle row

**Wide fitment**:
A fitment of more applications than a compiled catalog copies into each of its
vehicles' blocks (16), as a universal part has. The compiled catalog stores it
whole once, and in each vehicle's block only what answers for that vehicle, so
the file grows with its applications, not their square (ADR 0023).
_Avoid_: universal fitment (not every wide one is universal), big fitment

**Option text**:
A catalog's own words for the conditions under which an application holds
("w/o sunroof"), passed through verbatim for a person to settle.
_Avoid_: description, notes (a fitment's note is something else)

**Side**:
Left, right, both or none: the one position a fitment states for itself. Front
and rear, upper and lower live in the part type.
_Avoid_: position (as the field's name), orientation

**Redirect**:
A catalog's statement that, for a vehicle and years, one part type is listed
under another. A request follows it one hop.
_Avoid_: see-also, cross-reference

**Candidate**:
A fitment that may be the right part for a vehicle and a part type, with the
applications that matched, their option text and what a decode could not
confirm. A person settles among candidates.
_Avoid_: match, result, hit

**Retired number**:
A number a catalog no longer uses, pointing at the fitment that replaced it.
_Avoid_: alias (the word is kept away from mappings and entries), old number

**Fit**:
The request that reads a loaded catalog (ADR 0018), shaped by its inputs: a
vehicle's part types, a vehicle and a part type's candidates, a fitment's
vehicles, a vehicle's related vehicles. Nothing to fit is a status (`unknown`,
`no-vehicle`, `no-fitment`, `no-catalog`), never a refusal.
_Avoid_: match, lookup, interchange

**Part-type summary**:
A vehicle's part types, each with its count of fitments: what a fit answers
for a vehicle with no part type, instead of every fitment.
_Avoid_: whole-vehicle list, parts list

**Related vehicle**:
A catalog vehicle and a year range that shares at least one fitment with a
fit's vehicle: the longest range over which it shares the same fitments,
with their count per part type.
_Avoid_: interchangeable vehicle, sibling, twin

**Side filter**:
A fit's `side`: it drops only a fitment whose side excludes the one asked
for. A part for both sides goes on the left or the right, and a fitment
that states no side is never ruled out by one.
_Avoid_: position filter

### Stock

**Stock**:
What a business has to sell that a catalog's fitments lead to: parts on a shelf
and whole vehicles in a self-service yard, each from a named stock source.
_Avoid_: inventory (as the general term), availability

**Stock source**:
A named supply of stock rows, either a file or a database read at request time,
holding one of two things: `shelf` (parts, numbered under one catalog) or `yard`
(vehicles). The source decides what is available and what fields it shows. In
Go, `stock.Source`; its `Kind` is `file` or `live`, as an answer's block says.
_Avoid_: feed, connector (that is how a live source is read)

**Connector**:
How a stock source is read from a database at request time: a stock file with a
header and no lines (`connector.postgres`), naming a relation and the environment
variable that holds its credentials. In Go, `pkg/stock/pg`; only the `nitrovin`
binary links it.
_Avoid_: plugin, adapter, live source (as the term)

**Relation**:
The table or view a connector reads its rows from, whose column names nitrovin
dictates (`id`, `location`, `vehicle_<field>`, `fields`, …) and whose SQL the
source writes; a shelf's **release relation** is one row holding its catalog
release.
_Avoid_: feed table, query

**Unchecked**:
A connector's status before its first lookup: nothing connects at load, so it
is neither available nor unavailable until a fit asks for its stock.
_Avoid_: pending, unknown

**Shelf part**:
One part a source has on a shelf, tied to a fitment by its catalog number, or to
a part type and an exact vehicle and year when it has none.
_Avoid_: item

**Stock row**:
One shelf part or yard vehicle as its source supplies it: a stock file's line,
or a connector's relation row; in an answer, the row with its source's name,
how it matched, and its fields verbatim (`StockRow`).
_Avoid_: stock item, record

**Yard vehicle**:
A whole vehicle in a self-service yard, matched to the spine by its decoded VIN or
by the catalog's own model, so customers can find the vehicles whose parts fit.
_Avoid_: car, unit, stock vehicle

**Orphan**:
A shelf part whose number its catalog lacks, current or retired. It appears in no
answer and is counted as drift.
_Avoid_: unmatched part, missing part

**Stock file**:
A file of `kind: "stock"` loaded with `--vocab`: one stock source's header
(`source`, `as_of`, and a shelf's `catalog` and `release`), then one row a line,
each a shelf part or a yard vehicle. A row is the line, or a connector's relation
row, a part or vehicle comes from.
_Avoid_: inventory file, stock feed

**Stock list**:
A fit answer's list of shelf parts or yard vehicles: a page of its rows, its
`total`, and the block of every source of its kind (name, `file` or `live`,
`as_of`, a shelf's `catalog`, `release` and `catalog_mismatch`, and `status`,
with a `reason` when `unavailable`). A candidate has a shelf and a yard list,
its first rows; a fit from the fitment's key pages them.
_Avoid_: availability, stock result

**Catalog mismatch**:
A shelf source numbered under a catalog release other than the loaded catalog's,
marked on its every stock list: its numbers may name other parts.
_Avoid_: version conflict, stale stock

**Personal-looking field**:
A stock row's field whose name has a word `email`, `phone`, `name`, `address` or
`license`: drift, a nudge that a source supplies no personal data.
_Avoid_: PII detection (nitrovin cannot see what a field holds)
