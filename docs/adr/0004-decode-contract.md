# The decode keeps vPIC's strings and flat layout by default; the C library ships lean

The output-shape ticket keyed attributes by variable name "as DecodeVinValues
users know them". On a live check, `DecodeVinValues` keys by element code
(`BodyClass`, not `Body Class`), and vPIC has a second, list-shaped standard
(`DecodeVin`) that keeps repeated rows. We kept vPIC's own flat layout as the
default anyway, because people who already use vPIC should not have to learn a
format nitrovin made up. Our ergonomic layout, keyed by variable name with
element ids and arrays for multi-valued elements, is one `shape=nitrovin` away.
The flat layout dedups and joins repeated notes, and no oracle produces it
(NHTSA flattens in its web service, not in the dump). So parity is gated on
items, the oracle's own rows, and both layouts are pure functions of them.

Every value stays the oracle's exact string, numbers included. Parity is
byte-for-byte, and the oracle's own text is not canonical:
`Displacement (CC)` is `2000.0` on one VIN and `2000.00` on another. Turning
values into JSON numbers would make those two equal and leave the harness
comparing something other than what we ship.

The C library ships without the data. Embedding the 85 MB artifact costs about
520 MB per release, and it makes a single NuGet package for every platform
impossible (250 MB limit). A lean library takes the artifact's path in
`nitrovin_open`, the same file `--data` takes, which ships anyway.

## Considered options

- Default layout `nitrovin` with `vpic` on request: ergonomic and
  parity-checkable by default, but people moving from vPIC have to learn a new
  shape first.
- Exactly vPIC's live `DecodeVinValues` key set: byte-compatible keys, but a
  hand-kept list copied from a web layer we cannot compare against the dump
  (it has ten always-empty legacy keys and lacks `PlantAddress`).
- Values typed by `data_type`: easier for consumers, but loses the oracle's
  text.
- Both library forms (embedded for Release assets, lean for package managers),
  as the C-shared research proposed: two forms to build and test.

## Consequences

The `vpic` layout differs from live `DecodeVinValues` in its key set (no
`VIN`, `PlantAddress` present, the ten legacy keys absent) and in how repeated
values are ordered (byte order instead of vPIC's unstable order). A refresh
smoke-checks it against the live service but never blocks on it. C callers
ship two files, the library and the artifact.
