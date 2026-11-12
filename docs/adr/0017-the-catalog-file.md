# The catalog file: JSON Lines beside vocabularies, faults and drift, one reader

ADR 0016 defines what a catalog says. This ADR decides how a catalog is written,
loaded and checked ([The fitment file](https://github.com/joshuasprow/nitrovin/issues/224),
on [the fitment and stock map](https://github.com/joshuasprow/nitrovin/issues/221)).
It extends ADR 0011's format by addition.

## The file

A catalog is one JSON Lines file with `kind: "catalog"`:

```json
{"format":1,"kind":"catalog","name":"parts-catalog","version":"2026-11-12T0300Z","release":"7.02","data_month":"2026_10","spine":"part-types","vehicles":"catalog-models"}
{"fitment":{"type":"300","number":"51014A","side":"left"},"note":"…","applications":[{"vehicle":{"model":"Camry"},"first":2004,"last":2006,"options":"w/o sunroof"},{"vehicle":{"model":"Solara"},"first":2004,"last":2004,"options":"","qualifiers":{"13":["2.4"]}}]}
{"redirect":{"vehicle":{"model":"Camry"},"first":2004,"last":2006,"from":"517","to":"527","options":"rear"}}
{"retired":{"type":"300","number":"51013A","side":"left"},"to":{"type":"300","number":"51014A","side":"left"},"note":"7.01 → 7.02"}
```

**Header**
- `spine` names the declared part spine its part types come from.
- `vehicles` names the vehicle vocabulary its applications use, or `vpic`.
- `release` is the catalog publisher's own release identifier, optional and separate
  from the author's `version`. A stock source numbered under a release compares its
  release with this one.
- `data_month` is optional, as for vocabularies.

**Lines.** Each line holds exactly one of `fitment`, `redirect` or `retired`.

- **`fitment`**: one per fitment, carrying all its applications, the way a
  vocabulary line carries its mappings.
  - The key is `type`, `number` and `side`. Side is optional; absent means the
    fitment has none.
  - An optional `note`.
  - Each application has:
    - `vehicle`: the entry's fields, or a `make`, `model` or `ext` target when
      `vehicles` is `vpic`;
    - `first` and `last`, optional and inclusive;
    - `options`: required, possibly empty;
    - optional `qualifiers` (ADR 0009).
- **`redirect`**: a vehicle, years, the `from` and `to` part types, and optional
  `options`.
- **`retired`**: the retired key, the `to` key it was replaced by, and an optional
  `note`.

Applications can be several to a vehicle: several year ranges for one fitment and
one vehicle, each with its own option text, are normal.

## Loading and checking

**`--vocab` loads catalogs.** The header says what a file is, as ADR 0011 rules for
the other kinds. Files are plain `.jsonl`; compression on the way to an instance is
the deployment's concern.

**`/v1/info`** lists the catalog like the other files: name, kind, version,
`release`, blake3, path, and drift counts with examples. It adds counts of fitments,
applications, redirects and retired numbers.

**`nitrovin vocab check`** checks catalogs too.

**A synthetic catalog** with every line kind goes in `testdata/vocab/`, for the
documentation's examples and for CI.

### Faults

Faults refuse the file. ADR 0011's faults all apply: malformed lines, unknown
fields, `first` after `last`, and malformed qualifiers. Catalogs add these:

- a second catalog loaded;
- a catalog whose spine or vehicle vocabulary isn't loaded;
- a part type the spine lacks, in a fitment or a redirect;
- a vehicle whose fields aren't exactly the vocabulary's;
- a side other than `left`, `right` or `both`;
- a duplicate fitment key;
- a duplicate application within a fitment (same vehicle, years, options and
  qualifiers);
- a retired key that is also a live fitment;
- a retired number whose `to` isn't a fitment in the file.

Exact duplicates are the builder's to remove. In one catalog copy, 161,926 of
2,277,791 rows (7.1%) repeat only because the system files a number under several
of its own codes.

### Drift

Drift warns and still serves:

- **Vehicle vocabulary:** a vehicle entry the vocabulary lacks, or lists without a
  mapping. One catalog has 448 of its 1,326 models unmapped, and refusing that would
  make a catalog unusable until curation finished.
- **vPIC ids:** an id the month lacks, when `vehicles` is `vpic`.
- **Qualifiers:** values the month cannot produce. `model-qualifier` (ADR 0015)
  applies where the vehicle is a vPIC model.
- **Redirects:** a redirect whose target type has no application for that vehicle
  and year. That is 0.3% of one catalog's redirects.
- **Model years:** an application whose years fall outside its vPIC model's years,
  once model-year spans exist ([#222](https://github.com/joshuasprow/nitrovin/issues/222)).

## One reader behind every consumer

Every consumer of a loaded catalog goes through one Go interface, `fitment.Reader`:

- **`ByVehicle(partType, vehicles, year, side)`** returns the candidates: each
  fitment with its matching applications.
- **`ByNumber(partType, number, side)`** returns the fitment, directly or through a
  retired number.
- **`Redirect(partType, vehicle, year)`** returns the target type, if one is listed.
- **`Info()`**.

The JSON Lines loader implements it now, and the compiled form
([#228](https://github.com/joshuasprow/nitrovin/issues/228)) implements it later.
Requests ([#225](https://github.com/joshuasprow/nitrovin/issues/225)) never know which
one is loaded. Stock has its own interface
([#226](https://github.com/joshuasprow/nitrovin/issues/226),
[#227](https://github.com/joshuasprow/nitrovin/issues/227)). The reader leaves room for
a fitment connector without one being built.

## Load budget

The budget is measured on a synthetic fixture at catalog scale: about 790,000
fitments, 1.9 million applications, half with qualifiers, and 100,000 redirects.
JSON Lines must stay within the first user's interim budget:

- a load of at most 15 s on one core;
- at most 1.5 GiB at peak;
- a file of at most 500 MB.

A benchmark in CI records load time and heap per release, so a regression shows.
Today's engine measured 0.8 s and 503 MiB retained on 18 cores at that scale, and
2.3 s on one core. Anything tighter is the compiled form's target: 256 MiB
retained, 512 MiB peak, 1 s to open.

## Considered options

- **One line per application with the fitment key repeated.** Closer to SQL rows,
  but every line repeats the key, and a fitment's applications can be split across a
  file.
- **A `--catalog` flag and a separate check command.** ADR 0011 already rejected a
  flag per kind; the header says what a file is.
- **Collapsing exact duplicate applications silently.** It hides a builder's mistake
  that changes counts.
- **Refusing unmapped vehicle entries.** A catalog would be unusable until curation
  finished.
- **No `release` field, comparing against `version`.** A version is the author's
  free text, while a stock source needs the publisher's release to compare.
- **Tightening the JSON Lines loader now.** The compiled form is the planned lever.

## Consequences

- `docs/api/vocabulary.schema.json` gains the catalog header and the three line
  kinds in the same PR as the loader that reads them. Contract documents change with
  the code that serves them.
- [Fitment requests](https://github.com/joshuasprow/nitrovin/issues/225) are built on
  `fitment.Reader`.
- [The compiled fitment form](https://github.com/joshuasprow/nitrovin/issues/228)
  implements the same interface and is held to the budget above.

## Amendment (2026-11-12): one catalog across a server and its browsers

[ADR 0023](0023-fitment-in-the-browser.md): a browser refuses to load a catalog when its server has one, so one
nitrovin, server and browsers together, answers from one catalog.
