# nitrovin

An offline decoder that turns a VIN into the vehicle NHTSA's vPIC says it is,
shipped as one binary with the data inside.

## Language

### Decoding

**VIN**:
The 17-character vehicle identification number a caller gives us.
_Avoid_: serial, chassis number

**WMI**:
The world manufacturer identifier: the VIN's first three characters, or six
when the third is `9`. The unit vPIC's tables are keyed by.
_Avoid_: manufacturer code, prefix

**Element**:
One decodable property of a vehicle, identified by vPIC's numeric element id
and named by its variable name ("Make", "Body Class"). The id is stable across
data months; the name is not.
_Avoid_: field, variable (as the identity), column

**Attribute**:
The value an element resolves to for one VIN, carrying vPIC's attribute id
where the element is a lookup.
_Avoid_: value (when the id matters)

**Decode**:
The full result for one VIN: model year, error codes, check-digit validity,
one attribute per element, and provenance.
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
Postgres (the ground truth, run on refresh) and ultravin (the fast
differential oracle, run on every PR).
_Avoid_: reference, golden

**Parity**:
Byte-for-byte agreement with the oracle on every element of every VIN in a
corpus, except on registered deviations.
_Avoid_: accuracy, match rate

**Deviation**:
A VIN or class of VINs on which the decoder deliberately disagrees with the
oracle because the oracle's own data or procedure is shown to be defective.
Each one is registered with its evidence.
_Avoid_: known issue, exception, bug

**Corpus**:
A set of VINs with the oracle's answers, used to measure parity.
_Avoid_: golden set, fixtures

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
