# A mapping names one spine entry and states its conditions in vPIC's terms

A vocabulary maps each entry onto the spine one mapping at a time
([What a mapping says](https://github.com/joshuasprow/nitrovin/issues/120),
on [the vocabulary map](https://github.com/joshuasprow/nitrovin/issues/117)).
A mapping names exactly one spine entry: a vPIC make id, a vPIC model id
(which implies its make, since no model id sits under two makes in 2026_10),
or an extension id. It holds for an optional range of model years, inclusive,
where a missing bound is unbounded. An entry carries as many mappings as it
resolves to, so a yard's `Dodge / Ram` is two mappings: Dodge `Ram` (1938) up
to 2012 and the Ram make's `1500` (13620) from 2013.

What a mapping cannot say at model grain it says with **qualifiers**, and a
qualifier is written in what a decode emits: a vPIC element id and the set of
values it holds for, attribute ids for lookup elements (Drive Type, Body
Class) and the exact string for text elements (Series, Trim). A mapping holds
when every qualifier holds, any listed value satisfying its element. A reader
might expect a canonical class instead, such as the tonnage class (1500, 2500,
3500) Snyder's private mapping tables carry. We rejected it because nitrovin
matches exactly and runs no rules, and a class means reading vPIC's Series
text (`DS (1500)`, `3500W`, `1/2 Ton`) through one. The cost falls on authors,
who list every spelling a decode produces. EPA's `Silverado 1500 4WD` becomes
model `Silverado` with Drive Type 4WD and the Series values the author lists.

Only a decode can satisfy a qualifier, so a translation from a name ignores
qualifiers and returns the union of the entry's mappings. From a VIN, a decoded
value outside a qualifier's set, or a decoded model year outside the range,
rules a mapping out. An element the decode leaves empty, or a missing model
year (a partial VIN), keeps the mapping as a candidate: unknown is not a
mismatch, because otherwise an undecodable Series would give no answer at all.

Vehicles vPIC lacks (pre-1981 cars, marques it has no models for) get
**extensions**, spine entries an author mints in a named **extension set**: a
file loaded beside vocabularies and shared by them, so two vocabularies'
Corvairs meet in a translation. An extension is a make, or a model under a
vPIC make or an extension make, with an id prefixed by its set
(`ext:snyder/42`). It carries no pointer to a vPIC entry that later replaces
it; the author re-points the mappings and drops it. Extension mappings take no
qualifiers, since no decode lands on an extension.

An entry is named by the fields its vocabulary declares, an ordered list of
strings (`make`, `model`; Hollander's `mfr_name`, `model_nm`; a catalog's
`code`), each matched exactly after one fixed normalization of case and
spaces. A model year is never one of them; eBay's and EPA's year columns
become the mapping's scope. A vocabulary may list unmapped entries, so an
answer can tell a known but unplaced entry from a name the vocabulary lacks.
There is no "never maps" state: such an entry is unmapped, with a note.
Mappings carry an optional origin (`derived`, `curated`, `seed`) and note,
passed through to answers and never interpreted. Authorship and history stay
with the author.

## Considered options

- One opaque string per entry: simpler, but composite keys (Hollander's two
  fields, eBay's four) get an invented delimiter.
- A fixed make and model pair: doesn't fit a catalog keyed by its own code.
- Model ids only, as in Snyder's tables: a make vocabulary (NHTSA's recall
  file spells Mercedes-Benz three ways) could not be mapped.
- A tonnage class, or no qualifiers at all: the first is a rule inside
  nitrovin; the second leaves every Silverado on one union.
- Unknown drops the mapping: precise, but silent when vPIC's data is thin.
- Extensions inside each vocabulary: translations through them never
  converge.
- An extension that points at its vPIC replacement: two spine entries for one
  vehicle until the author cleans up.

## Consequences

- Load-time validation belongs to the vocabulary file's design: mappings to
  ids the loaded data month lacks, qualifier values that month's decode cannot
  produce, extension names that equal a vPIC name under the same make, and
  references to extensions no loaded set defines. The glossary promises stable
  element ids across data months, not stable attribute ids or text values.
- vPIC records no year span per model, so a mapping's year range cannot be
  checked against the spine.
- Part-type mappings reuse the entry fields, unmapped entries and provenance.
  Their spine reference, position and dependence on the vehicle are
  [the part-spine ticket's](https://github.com/joshuasprow/nitrovin/issues/121).
