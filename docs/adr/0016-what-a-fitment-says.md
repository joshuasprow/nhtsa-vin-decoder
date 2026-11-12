# What a fitment says

nitrovin loads **catalogs**: an author's statement of which parts fit which vehicles
([What a fitment says](https://github.com/joshuasprow/nitrovin/issues/223), on
[the fitment and stock map](https://github.com/joshuasprow/nitrovin/issues/221)).
This amends two ADRs:

- **ADR 0010** ruled "which part types exist for which models" out of scope as
  catalog data. That data is now in scope.
- **ADR 0011** gains a fourth kind of named file beside vocabularies, extension sets
  and declared spines. The [fitment file](https://github.com/joshuasprow/nitrovin/issues/224)
  decides its format, faults and drift.

## A catalog and its fitments

A catalog shares the one namespace of vocabularies, extension sets and spines. It
names two things:

- the **declared part spine** (ADR 0010) its part types come from;
- the **vehicle vocabulary** its applications use, or `vpic`.

A deployment loads one catalog.

A **fitment** is one physical part, identified within its catalog by three values:

- **part type:** an entry of the catalog's part spine;
- **number:** the catalog's own string;
- **side:** `left`, `right`, `both` or none.

Side is part of the identity because catalogs give a left and a right part the same
number. Front and rear, upper and lower, live in the part type, as catalogs already
name their types ("Front Brake Rotor"). There is no general position field.

A fitment may carry a `note`, passed through and never interpreted, as on mappings.

A system that files one catalog number under several of its own codes (one copy of
a commercial catalog puts 27,726 numbers under two or more codes) is a **part
vocabulary** on the catalog's spine (ADR 0010). Its codes map onto the catalog's part
types, and the fitment never carries them. A fitment stays one part.

## Applications

An **application** is one vehicle and one model-year range that a fitment fits:

- **Vehicle.** An entry of the catalog's vehicle vocabulary, named by its fields.
  If the catalog declares `vpic`, it is a spine target written as a mapping writes
  one: `make`, `model` or `ext`.
- **Years.** `first` and `last`, inclusive, either end optional, as on mappings
  (ADR 0009).

Applications name the catalog's own model entries rather than vPIC ids. A request
translates them, so:

- a catalog keeps its own model names;
- a model the vocabulary hasn't mapped stays listable by name. The first user has
  448 of 1,326 such models;
- curating the vocabulary never forces a catalog rebuild.

A catalog that knows vPIC ids declares `vpic` and skips the translation.

**Option text** is carried on every application. It is the catalog's own words for
the conditions a person settles ("w/o sunroof", "VIN H, 8th digit"): verbatim, never
interpreted, possibly empty. Answers return it.

About 72% of one catalog's application rows state a condition no VIN settles, so
option text is the requirement, and the reason a fitment answer is a set of
candidates for a person.

An application may also carry **qualifiers**, exactly as ADR 0009 has them:
- conditions in decode terms;
- checked only from a VIN;
- an unknown value keeps the application.

A builder derives them from the option text by rules. Text a rule cannot read stays
text.

A condition that names a VIN position stays text: there is no second kind of
qualifier. It is about 5% of rows, and no user has asked for one.

Applications carry no `origin`. A catalog is one author's published data, not a mix
of derived and curated rows, and a builder that derives qualifiers says so in the
catalog's version.

## Redirects and retired numbers

A **redirect** says that, for a vehicle and year range, part type A is listed under
part type B. It may carry option text. One catalog copy carries about 100,000 of
them, and 99.7% point at a type that has an application for the same year.

A request for A that finds no fitment, only a redirect, answers with B's candidates,
marked as reached through the redirect. It follows one hop at most. A redirect to a
type the spine lacks is a fault.

A **retired number** is a number the catalog no longer uses. It points at the
fitment that replaced it, with an optional note (the release step, say). A lookup by
a retired number answers the current fitment, marked as reached through a retired
number.

The author resolves chains: every retired number points at a fitment in the file,
and anything else is a fault.

A shelf of stock renumbered at the catalog's own release needs no retired numbers.
They serve numbers that callers bring from an older release: one release step
retires or moves about 1,700 applications.

## Considered options

- **Number and part type as the identity, with side as a description.** A catalog
  that reuses a number across sides would have to fold the side into the number.
- **Applications by vPIC id only.** Faster and simpler, but unmapped models vanish,
  and every vocabulary fix rebuilds the catalog.
- **A VIN-position qualifier** ("character 8 is H"). It is exact and needs no decode,
  but nobody needs it yet. It can be added later as a new field, which format 1
  allows.
- **A general position field.** It duplicates what part types already say.
- **Dropping redirects at build.** A request for the listed-under type then finds
  nothing.
- **Leaving renumbering to the author.** Callers working from an older release then
  get no answer.
- **The system's own codes as a fitment attribute.** It splits one part into several
  fitments, or pushes one system's codes into every answer.
- **`origin` on applications.** It is a curation concept, and a catalog is not
  curated line by line.

## Consequences

- [The fitment file](https://github.com/joshuasprow/nitrovin/issues/224) decides:
  - the catalog's file kind and header;
  - the lines for fitments, applications, redirects and retired numbers;
  - which of the above are faults or drift;
  - the version a stock source is checked against.
- [Fitment requests](https://github.com/joshuasprow/nitrovin/issues/225) decides how
  candidates carry option text, `unconfirmed`, side, and the redirect and
  retired-number marks.
- Synthetic fixtures need a small catalog with every line kind, for the
  documentation's examples.
