# What stock says: shelf parts and yard vehicles, from named sources

nitrovin answers stock: the parts on a shelf and the whole vehicles in a
self-service yard that fit what a caller asked about. Decided on
[What stock says](https://github.com/joshuasprow/nitrovin/issues/226), on
[the fitment and stock map](https://github.com/joshuasprow/nitrovin/issues/221).
Stock attaches to `fit` (ADR 0018). A stock source is a file (below) or a
Postgres connector ([#227](https://github.com/joshuasprow/nitrovin/issues/227)).

## Stock sources

A **stock source** is named, in the one namespace of vocabularies, extension sets,
spines and catalogs. It has one of two kinds:

- **`shelf`**: parts. It names the one catalog its numbers belong to, and
  optionally the catalog `release` its numbers are under (for a connector, where to
  read it).
- **`yard`**: whole vehicles. It names no catalog, because a yard vehicle carries a
  VIN and no number.

Any number of sources may be loaded. A row belongs to exactly one of them.
Availability is the source's rule: a source supplies only what it would offer,
and nitrovin passes any status through without interpreting it.

## A shelf row

nitrovin interprets these fields:

- **`id`**: the source's own stock id.
- **`location`**: an opaque string that a request can filter on.
- One of two ways to name the part:
  - the **fitment key**: `type`, `number`, `side`;
  - for a row with no number, the **`part`** (a part-spine or part-vocabulary entry)
    plus the catalog **`vehicle`** and **`year`**.

  In one parts system, 12% of shelf rows have no number: part types the catalog does
  not cover.

Everything else goes in **`fields`**: a flat object of strings, numbers and null,
passed through verbatim and never interpreted. Stock number, condition, mileage,
price, status, comments and dates are all fields.

A row with a number joins its fitment, marked `matched: "fitment"`. A row whose
number is a retired number (ADR 0016) joins the successor, marked
`reached: "retired"`.

A row with no number is matched only by part type and the exact vehicle and year,
marked `matched: "vehicle"`.

A number the catalog lacks entirely makes an **orphan** row. It never appears under
a candidate. `/v1/info` and `vocab check` count orphans, with examples, as drift. One
user's count has been exactly 0 in every copy it kept, and it uses that count as an
alarm.

## A yard row

nitrovin interprets **`id`**, **`location`**, and the first of these routes to the
spine that is present:

1. **`decoded`**: a vPIC make, model and year that the source precomputed, which a
   connector can read from a column;
2. **`vin`**: decoded by nitrovin. This applies to file sources only, and the decode
   is cached for the life of the process;
3. **`vehicle`** plus **`year`**: an entry of the catalog's vehicle vocabulary, the
   fallback.

The answer says which route matched: `matched: "vin"` or `matched: "catalog-vehicle"`.
In one yard system, every live vehicle carries both a VIN and the catalog's own model,
and the two agree about 95% of the time.

Everything else goes in **`fields`**: any JSON value, so a list of photos passes
through. Stock number, colour, row, date added and photos are all fields.

## In answers

`fit` gains `stock` (`["shelf"]`, `["yard"]`, or both) and an optional `location`
filter (one or more):

- **Each candidate** carries two stock lists:
  - `shelf`: the rows of that fitment;
  - `yard`: the yard vehicles whose vehicle and year are among the fitment's
    applications.

  Each list carries its first 10 rows and a `total`. A `fit` from that fitment's key
  with the same `stock` pages the whole list.
- **The answer** also carries `shelf_by_vehicle`: the rows with no number for the
  part type and the exact vehicle and year.
- **Related vehicles** each carry their count of yard vehicles.

**Every stock list carries its source:**

- name and kind;
- **`kind`**: `file` or `live`;
- **`as_of`**: a file's own time, or a live source's load time when the source
  exposes one;
- for a shelf source, its catalog **`release`**, and **`catalog_mismatch: true`**
  when that differs from the loaded catalog's release.

A source that is down answers status `unavailable` with a reason and an empty list.
The rest of the answer stands.

## The stock file

A file of `kind: "stock"`, loaded by `--vocab`, is for callers with no database:

- **The header** carries `name`, `version`, `source` (`shelf` or `yard`), `as_of`,
  and for a shelf source `catalog` and `release`.
- **The lines** are one row each, as above.
- **A new file means a restart**, as ADR 0011 rules for every file.

## What stock never carries

`fields` are served verbatim to every caller of the deployment. So the contract
says a source supplies no personal data: no names, contact details or identity
numbers of owners, sellers, customers or staff. It also supplies nothing it would
not show a customer, such as cost.

nitrovin cannot tell what a field holds. The source chooses its columns: a view, or
the file's lines. `vocab check` reports field names that look personal (`email`,
`phone`, `name`, `address`, `license`) as drift. That is a nudge, not a guarantee.

## Considered options

- **One unnamed stock set.** A deployment with shelf and yard stock, or two parts
  systems, could not tell them apart.
- **A fixed, typed shelf schema.** nitrovin would interpret condition and price
  codes that differ between every system.
- **Always decoding a yard VIN in nitrovin.** A connector would decode thousands of
  rows on every request. A precomputed decode is a column read.
- **A separate `stock` request.** It costs a second round trip for the answer most
  callers want with their candidates.
- **Matching an orphan row by its vehicle.** It hides the row's real problem, a
  number the catalog lacks, which its count exists to raise.
- **No field-name check.** It gives up a cheap warning on the rule nitrovin cannot
  enforce.

## Consequences

- [The connector](https://github.com/joshuasprow/nitrovin/issues/227) supplies rows
  of exactly these shapes from a Postgres relation, and decides how columns map to
  them.
- [#241](https://github.com/joshuasprow/nitrovin/issues/241) builds `fit`. A stock
  ticket adds `stock`, `location` and the stock file to it.
