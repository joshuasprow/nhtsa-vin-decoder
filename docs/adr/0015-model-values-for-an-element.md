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
  the model's Model (28) pattern keys in that schema, or in another schema its WMI code
  (every WMI row with it) reaches in an overlapping year band. Keys are compared position by
  position with the decoder's own key compiler.
- **Engine-model rows** count through the keys of the Engine Model (18) pattern that
  selects them.
- **Vehicle specs** count for the model they name, gated by make and by a vehicle type that
  the model's WMIs carry. Their key rows are not evaluated.
- **Default values** count for the models whose WMIs carry the default's vehicle type.
- **Conversions** count for a model when the decoder's own conversion gives them from one
  of the model's values of the conversion's from element (Displacement CC, CI and L), a
  formula pattern's attribute included, as the conversion stage reads it.
- **Make, Manufacturer Name and Vehicle Type**, which the pattern pass skips, are the
  model's makes and its WMIs' manufacturers and vehicle types. Model is the model itself.

The list holds every value a decode with a model year gives the element for a VIN of the
model, except a value a formula pattern computes from the VIN's characters; it may list
values no VIN of the model ends up with. It is an **upper bound**: a listed value may still
lose, in a given VIN, to a higher-ranked pattern. On 2026_10 no Series pattern is a formula.

Some public elements are decoded from the VIN itself, not from a row: Model Year (29), and
the Corrections elements (142 suggested VIN, 143 error codes, 144 possible values, 156,
191 error text, 196 the descriptor). They have no model values, and asking for one is
refused.

On 2026_10 the bound holds 31,800 (model, Series) pairs, and 7,830 (model, Drive Type)
pairs before default values are added. Fifteen sampled pairs that the schema-wide rule adds and this rule
drops were each checked by decoding a VIN built from both keys. In every case the model
never decodes the value.

*Amended when built ([#233](https://github.com/joshuasprow/nitrovin/issues/233)):*

- **Partner schemas.** The same-schema rule left out 412 of the 12,659 values the parity
  corpus's decodes give. A pass reads every schema of every year band holding its model
  year, and some makers keep a model's Model patterns in one schema and its engine or plant
  patterns in another under the same WMI code and years. Schemas whose bands overlap under
  one WMI code therefore count together. That adds 887 (model, Series) pairs (32,687 in
  all). The Silverado gets no Express or cargo van series; it gains APV, 6000/7000 Medium
  Duty and 3 Ton, each decodable. Ignoring the years instead would give it "1500 Cargo" and
  cost 1.5 s for Series alone.
- **Conversions and the VIN's own elements.** Displacement CC, CI and L are filled by
  conversions as often as by patterns, so the list converts the model's values as the
  decoder does. Model Year and the Corrections elements are refused.
- **What is left out.** No value of a corpus decode with a model year is left out, but for
  a formula pattern's. Two kinds of decode can still give a value the list lacks: one with no
  model year, whose year character is not one, which reads every band; and one of a VIN
  holding `#`, which only a formula key holds.

## Computed when the artifact is opened, one element at a time

The index is built from the open artifact on the first request for an element, then cached
(as `Month.valuesOf` is):

- Measured when built, on an M5 Pro: the first ask of Series and Drive Type together
  takes 0.14-0.16 s. 60-90 ms of it is the join every element shares (each schema's Model
  keys and partners, each model's WMIs), which keeps about 31 MB. Patterns take about 75 ms
  and keep about 3 MB for the two elements; specs and defaults take about 5 ms.
- Displacement CC, CI and L together take about 0.4 s and keep about 17 MB more: each
  converts the others' values.
- Every element that can be asked for (134 on 2026_10) would take about 2.1 s and keep
  about 200 MB, which is why nothing is built until asked.
- Opening stays under 1 ms.
- The artifact format does not change, so no data major or decoder major is bumped
  (ADR 0007).

Any public element can be asked for, but those decoded from the VIN itself.

A value counts only through schemas whose year bands overlap under the VIN's WMI code;
spans are [#222](https://github.com/joshuasprow/nitrovin/issues/222)'s.

## The request

`list` with `of: "vpic"` takes `where: {"model": 1850, "element": 34}`:

- **Required together.** Both fields are required together.
- **Refused as `#invalid-request`:**
  - either one alone;
  - either one combined with `where.make`, or with `where.year` (ADR 0022);
  - an element that is not public, or one decoded from the VIN itself.
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

`nitrovin vocab check` and `/v1/info` gain a drift class, `model-qualifier`: a qualifier value
the mapping's own model never decodes to.

- It applies only to mappings whose target is a `model`. Make and extension targets keep the
  month-wide check.
- Like all drift, it is reported and never refused.
- A mapping's years are not tested against the patterns' year bands. That waits on #222.
- Elements that have no model values (Model Year 29 and the Corrections elements 142–144,
  156, 191, 196) cannot be tested per model, so they are skipped.

**Not at load (amended, #234).** Testing it builds the model values index for every element a
model mapping qualifies on. On 2026_10 with EPA's vocabulary that costs about 0.14 s and
80–100 MB of peak memory. Running it at load would put load over ADR 0011's 100 ms budget for
an EPA-sized vocabulary, on every process start and every C handle. So:

- `nitrovin vocab check` always runs it.
- Load does not run it, so it prints no `model-qualifier` warnings on stderr.
- `/v1/info`'s drift counts and examples include it. They are computed on the first info
  request and cached, so a server pays the cost only if someone asks. Startup, assets,
  `/sw.js` and the decode views read the info as load measured it, so they never pay. The one
  view that lists the files' drift does: the Vocabularies dialog, which every full (not htmx)
  page draws, `/` and `/ui/vocabularies/{name}` among them, so the first such page served
  pays it once.
- A catalog whose vehicles are vPIC's (`vehicles: "vpic"`) has its applications'
  qualifiers tested the same way, against the application's model: one finding per model and
  value, at its first application.

The browser keeps the month-wide check: the spine bundle carries no per-model values. Adding
Series and Drive Type to it would add about 693 KB raw, so it waits for a measurement against
ADR 0013's 1 MB brotli budget.

As built (#233's partner schemas), EPA's vocabulary for 2026_10 shows 335 Drive Type and 273
Series `model-qualifier` findings. The 388 Drive Type first estimated came before partner
schemas, which account for all 53 between; no conversion gives Drive Type, and default values
change nothing. Most findings come from the builder itself:

- It widens a drive column to two ids where the model decodes one: "Front-Wheel Drive" to
  FWD and 4x2, or a 4WD to AWD.
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
