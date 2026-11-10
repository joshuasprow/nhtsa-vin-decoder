# nitrovin

An offline decoder that turns a VIN into the vehicle NHTSA's vPIC says it is,
shipped as one binary with the data inside.

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
Where a vocabulary, extension set or declared spine disagrees with the loaded
data month: a vPIC id the month lacks, a qualifier value its decode cannot
produce, an extension named like a vPIC entry under the same make. Reported,
never refused: NHTSA's monthly changes are not the author's mistakes.
_Avoid_: stale, invalid, broken mapping

**Fault**:
What refuses a vocabulary, extension set or declared spine whatever the data
month: a malformed line, an unknown field, a duplicate entry or id, a
reference no loaded file defines. A fault stops nitrovin from starting;
drift never does.
_Avoid_: error (a decode's error codes are about the VIN), invalid
