# Model years: the ranges a vPIC model's VIN schemas cover

nitrovin derives, for each vPIC model, the model years its VIN schemas cover.
`list vpic` shows them and filters on them, and drift reports a mapping or
application whose years fall outside them
([Model-year spans](https://github.com/joshuasprow/nitrovin/issues/222), on
[the fitment and stock map](https://github.com/joshuasprow/nitrovin/issues/221)).

This amends three ADRs:

- **ADR 0012:** "vPIC models get no year filter, because vPIC records no year span
  per model".
- **ADR 0009:** "a mapping's year range cannot be checked against the spine".
- **ADR 0017:** adds the model-year drift it anticipated.

## Derivation

A model's years are the union of the year bands (`wmi_vinschema`) of every VIN
schema that has a Model (28) pattern naming that model, across all of the schema's
WMIs, merged into ranges.

- **Gaps are kept.** On 2026_10, 920 models have more than one range.
- **Open bands stay open.** A band with no `yearto` gives a range with no `last`:
  still running. On 2026_10 that is 12,186 of 41,810 bands. The decoder's procedure
  treats such a band as running to 2999.
- **Nothing is excluded.** Every schema counts, including any flagged for QC (none on
  2026_10). Every WMI counts, including the 3 of 12,904 that are unavailable or
  non-compliant. The make plays no part.
- **No patterns means unknown years.** A model with no Model pattern has no years
  (540 of 32,070 on 2026_10). That means unknown, not empty, and nothing is flagged
  against it. 218 models have years starting before 1981.

The years are computed when the artifact is opened, on first use, then cached, as
ADR 0015 does for model values. That costs 53 ms and 2.8 MiB for every model, so
the format does not change and the data major does not move. The spine bundle does
not carry years for now. A browser need would be measured against ADR 0013's 1 MB
budget first.

## Where they show

**`list vpic`.** Each model row gains `years`: `[{"first": 1999, "last": 2006},
{"first": 2009}]`.

**`where.year`.** `list` with `of: "vpic"` and `where.make` also takes `where.year`
(one year). It keeps a model whose ranges include that year:

- an open range includes every later year;
- a model with no years is kept.

So the filter returns a superset of the models built in that year. It never hides
a model it cannot place.

- The make is given by id, and the models are those `list vpic` already relates to
  the make, not every model under a WMI the make shares.
- `where.year` with `where.model` and `where.element` (ADR 0015) is refused, as any
  other unknown combination is.

**Translate** does not add years to its spine entries.

## Drift: `model-years`

`vocab check` and load report a new drift class, `model-years`. It applies to a
mapping to a `model`, and to a catalog application whose vehicle is a vPIC model,
when the years fall partly or wholly outside the model's years. The finding says
which, and gives the model's ranges.

- A model with no years is never flagged.
- Make and extension targets are never flagged.
- Like all drift, it is reported and never refused.

The first user's numbers: of 1,473 year-bounded mappings, 616 fall inside, 554
partly outside and 303 wholly outside. Many of those are errors in the user's own
data, which is what this check is for.

## Considered options

- **One span per model, first year to last.** It hides a gap, so a mapping that
  covers only gap years would pass.
- **An artifact table built at import.** A format break, to save 53 ms.
- **Drift only, with no years on rows and no filter.** No consumer needs the filter
  today, but the derivation exists either way and a row costs a few bytes.
- **A range filter (`where.first`/`where.last`).** One year answers the question
  callers ask ("what did this make build in 2004?"). A range can be added later.
- **Reporting only ranges wholly outside.** A partial overlap is often a loose end
  year, but one of them is how a mapping drifts into another generation's parts.
- **Scoping years by make through shared WMIs.** On the June 2026 dump, about 40,000
  of 71,377 (make, model) pairs exist only through a WMI two makes share.

## Consequences

- The contract documents gain `years` on vPIC model rows and `where.year`, in the
  same PR as the code.
- The catalog loader ([#239](https://github.com/joshuasprow/nitrovin/issues/239))
  reports `model-years` drift for applications.
