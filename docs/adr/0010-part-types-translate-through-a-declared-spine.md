# Part types translate through a spine their author declares

Vehicles translate through vPIC, which nitrovin carries. Part types have no
such spine
([The part-type spine when nitrovin ships none](https://github.com/joshuasprow/nitrovin/issues/121),
on [the vocabulary map](https://github.com/joshuasprow/nitrovin/issues/117)).
vPIC names no parts: its `GetParts` API lists manufacturers' 49 CFR 565/566
filings, and its elements describe a configured vehicle (Brake System Type
"Disc", not a brake rotor). The taxonomies at a useful grain (Auto Care PCdb,
a salvage-industry interchange catalog's part types, TecDoc) are licensed, PCdb's down to its ids, and the
public ones (NHTSA's recall components, HS codes, Google's taxonomy, Wikidata)
are too coarse ([part-taxonomy research](https://github.com/joshuasprow/nitrovin/issues/119)).
eBay's category tree can never ship
([eBay research](https://github.com/joshuasprow/nitrovin/issues/118)).
So a part-type spine is **declared**: its author supplies it as data, a name
and every entry id, names optional so a subscriber can list PCdb ids without
copying PCdb's text. The list is required because matching is exact: without
it, a mistyped id in a mapping silently becomes a spine entry nothing else
reaches. The vehicle spine stays vPIC and cannot be declared, because decodes
speak vPIC and qualifiers (ADR 0009) depend on it.

A part vocabulary names, once, the spine it maps onto, and its mappings hold
bare ids from that spine. Answers carry a spine entry as a (spine, id) pair,
the shape Catena-X's `PartClassification` gives a part's (classification
standard, id). Any number of spines may be loaded, but each vocabulary maps
onto exactly one and a translation crosses exactly one, never chaining from
one spine to another. The pairwise case needs no concept of its own: a price
list translated to a catalog's part types is a price list mapped onto a
spine named for that catalog. Bridging two spines is a vocabulary of one spine's entries mapped
onto the other.

A declared spine is flat and its author's own. Entries name no parent, so a
coarse entry (a yard's `Brake Parts`) maps to every fine entry it covers,
one-to-many. Position has no concept either: a spine with Fender LF and
Fender RF entries keeps it, and a position-free entry (eBay's Fenders) maps to
both, while a spine with one Fender drops it. There are no extensions, since
the author adds entries, and a spine that carries names is also a vocabulary
of those names, each mapping to itself, as vPIC's names are for vehicles.

Part mappings reuse ADR 0009's entry fields, unmapped entries, provenance and
qualifiers. A part depends on the vehicle only through qualifiers in vPIC's
terms: eBay's motorcycle brake pads (177925) hold where Vehicle Type is
Motorcycle, its car brake pads (57357) where it is not. Which part types exist
for which models (a catalog's catalytic converter coverage from MY2022) is
catalog data, and out of scope.

## Considered options

- Pairwise mappings between part vocabularies: covered by declaring one side
  the spine, without a second translation path.
- A part-type list nitrovin mints and owns: a taxonomy to maintain, coarser
  than what yards and catalogs already use.
- A spine that is only a name, with ids unvalidated: typos become phantom
  entries.
- Prefixed ids per mapping (`catalog:300`), letting one vocabulary span
  spines: more freedom than any known vocabulary needs.
- Chaining across spines: reach at the cost of multi-hop answers and cycles.
- A position qualifier or parent links: each a concept nitrovin would have to
  interpret, where the spine's grain and one-to-many already say it.

## Consequences

- The vocabulary file ticket designs the spine's form and identity (a
  commercial catalog's spine gains types between releases) and checks at load that a
  vocabulary's spine is loaded and every id it maps to is in it.
- The contract ticket renders (spine, id), part translation with and without
  a vehicle, and listing a spine's entries.
