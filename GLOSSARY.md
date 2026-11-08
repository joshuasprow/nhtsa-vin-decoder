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
An element that can resolve to several attributes for one VIN, every one of
them kept, repeats included (Note, Other Restraint System Info). Every other
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

**Model-year hint**:
A model year the caller already knows, which competes with the year the VIN
encodes and flags error code 12 when they disagree.
_Avoid_: year override, stated year

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
Postgres, run as the freshened oracle (the ground truth, run on refresh), and
ultravin (the fast differential oracle, run on every PR).
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
_Avoid_: sync, update (that word is `nitrovin update`, the command a user runs)
