# A model's values for an element, matched by pattern key

`list` can answer, for one vPIC model and one element, the values a VIN of that model can decode to
([Per-model element values](https://github.com/joshuasprow/nitrovin/issues/231),
on [the fitment and stock map](https://github.com/joshuasprow/nitrovin/issues/221)).
This amends two ADRs:

- **ADR 0012:** `list vpic` takes a model and an element.
- **ADR 0009:** load-time drift can test a qualifier against the mapping's own model.

Until now, nothing nitrovin publishes said which Series strings belong to a Silverado:

- `list vpic` filters by make only.
- The spine bundle lists each element's values for the whole month.
- The only per-model code was the EPA builder's `readModelValues`.

A consumer that writes qualifiers outside nitrovin, such as a vocabulary kept in its own
database, had to read the dump itself.

## How a value reaches a model

`readModelValues` gives every model in a VIN schema every value in that schema. That
over-reaches. On 2026_10:

- 5,970 of 25,299 schemas (24%) name more than one model, and one names 356.
- 92% of its 421,022 (model, Series) pairs come only from sharing a schema. Silverado gets
  "1500 Cargo" from the van line, F-150 gets "Econoline", and Camry gets other chassis codes.

So a value counts for a model only where the two could appear in one decode:

- **Patterns.** A pattern's value counts when its key could match the same VIN as one of
  the model's Model (28) pattern keys in that schema. Keys are compared position by
  position with the decoder's own key compiler.
- **Engine-model rows** count through the keys of the Engine Model (18) pattern that
  selects them.
- **Vehicle specs** count for the model they name, gated by make and by a vehicle type that
  the model's WMIs carry. Their key rows are not evaluated.
- **Default values** count for the models whose WMIs carry the default's vehicle type.

The decoder keeps one value per element, and the winning value always comes from a pattern
whose key matched the VIN. So this set can over-reach but never leave out a value a VIN of
the model really decodes to; it is an **upper bound**. A listed value may still lose, in a
given VIN, to a higher-ranked pattern.

Values a formula pattern computes from the VIN are not listed, as for `Month` today. On
2026_10 no Series pattern is a formula.

On 2026_10 the bound holds 31,800 (model, Series) pairs, and 7,830 (model, Drive Type)
pairs before default values are added. Fifteen sampled pairs that the schema-wide rule adds and this rule
drops were each checked by decoding a VIN built from both keys. In every case the model
never decodes the value.

## Computed when the artifact is opened, one element at a time

The index is built from the open artifact on the first request for an element, then cached
(as `Month.valuesOf` is):

- Series and Drive Type together take about 41 ms of patterns and retain 2.6 MB.
- With specs and defaults, about 0.2 s.
- Every element at once would take about 0.5 s and 84 MB, which is why nothing is built
  until asked.
- Opening stays under 1 ms.
- The artifact format does not change, so no data major or decoder major is bumped
  (ADR 0007).

Any public element can be asked for.

Model years play no part. [Model-year spans](https://github.com/joshuasprow/nitrovin/issues/222)
derives spans through the same schema-to-Model-pattern join and may add a year to it.

## The request

`list` with `of: "vpic"` takes `where: {"model": 1850, "element": 34}`:

- **Required together.** Both fields are required together.
- **Refused as `#invalid-request`:**
  - either one alone;
  - either one combined with `where.make`;
  - an element that is not public.
- **An unknown model id answers an empty list.** As ADR 0012 has it, nothing to list is
  never an error.
- **Rows** are `{"value", "name"}`:
  - `value` is the attribute id for a lookup element, or the exact text for a text element.
  - `name` is the attribute's name, and appears only for lookup elements.
- **Order.** Rows are sorted by normalized value and then by exact value, so case variants
  ("1/2 Ton", "1/2 ton") both appear.
- **Paging.** `limit` and `after` work as for every list.

It is additive under `/v1` and reaches every surface that `list` does: HTTP
(`GET /v1/vocabularies/vpic/entries?where.model=…&where.element=…`), stdio and C, the CLI,
MCP and Go. The contract documents change in the same PR as the code.

## Drift against the mapping's model

Load and `nitrovin vocab check` gain a drift class, `model-qualifier`: a qualifier value the
mapping's own model never decodes to.

- It applies only to mappings whose target is a `model`. Make and extension targets keep the
  month-wide check.
- Like all drift, it is reported and never refused.
- A mapping's years are not tested against the patterns' year bands. That waits on #222.

The browser keeps the month-wide check: the spine bundle carries no per-model values. Adding
Series and Drive Type to it would add about 693 KB raw, so it waits for a measurement against
ADR 0013's 1 MB brotli budget.

EPA's vocabulary for 2026_10 would show 388 Drive Type and 273 Series `model-qualifier`
findings. Most come from the builder itself:

- It widens a drive column to two ids where the model decodes one: "Front-Wheel Drive" to
  FWD and 4x2.
- It takes Series spellings from other models' patterns.

The builder moves onto this index and drops `readModelValues` and the widening, so the file it
publishes has no such finding.

## Considered options

- **Matching by schema, as `readModelValues` did.** Simpler, but nine in ten Series pairs it
  reports are wrong.
- **A table in the artifact.** No cost at open, but it is a format break: nitrovin-data v2
  and a decoder major.
- **A file published with each release.** It leaves the artifact alone, but adds one more
  asset to keep in step and sign.
- **A field on every listed model, or a new `values` request.** The first grows every model
  listing and cannot page. The second is a third request shape on six surfaces.
- **Patterns only.** It would leave out values that only vehicle specs or defaults supply.
  The list would stop being an upper bound.
- **Years now.** On EPA's file, checking each mapping's years against the bands flags 810
  more qualifier values. Doing it here would decide part of #222 before its requirements
  arrive.
