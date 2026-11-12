# Request-time stock: a Postgres connector declared as a stock file

A stock source (ADR 0019) can be read from Postgres at request time, so a yard's
rows and photos are as fresh as its database. This is decided on
[Request-time stock](https://github.com/joshuasprow/nitrovin/issues/227), on
[the fitment and stock map](https://github.com/joshuasprow/nitrovin/issues/221).
It amends ADR 0011 ("files load once… and never change while nitrovin runs") for
stock only, and the glossary's opening line. Fitment still loads from files
(ADR 0017), and its reader leaves room for a connector without one being built.

## Declaring a connector

A connector is a `kind: "stock"` file (ADR 0019) with a header and no lines:

```json
{"format":1,"kind":"stock","name":"shelf","version":"1","source":"shelf","catalog":"parts-catalog",
 "connector":{"postgres":{"relation":"stock.shelf_parts","release_relation":"stock.catalog_release",
   "dsn_env":"NITROVIN_STOCK_SHELF_DSN","statement_timeout_ms":1000,"max_connections":4}}}
```

- **Loading.** It loads through `--vocab` like every file, in the one namespace,
  with no new flag. Two sources with one name are a fault (ADR 0011).
- **Credentials.** The DSN comes only from the environment variable the header
  names, so the file carries no secret. Neither `/v1/info` nor any answer shows a
  DSN, host or user. Both show the source's name, `connector: "postgres"`, the
  relation and its status.
- **Freshness.** A connector's rows change while nitrovin runs. That is the
  amendment to ADR 0011, and only stock may do it.

## The relation's shape

nitrovin dictates column names, never SQL. It runs fixed, parameterised `SELECT`s
against the named relation, which is a table or view of the source's choosing:

| Source | Columns |
| --- | --- |
| `shelf` | `id`, `location`, `type`, `number`, `side` (all three null on a row with no number), `part_type` (a spine id, for rows with no number), `vehicle_<field>` for each field of the catalog's vehicle vocabulary, `year`, `fields` (jsonb), `as_of` (optional) |
| `yard` | `id`, `location`, `decoded_make`, `decoded_model`, `decoded_year`, `vehicle_<field>`, `year`, `fields` (jsonb), `as_of` (optional) |
| release | one row, column `release` |

The connector never decodes: a yard relation carries its decodes, which the
source refreshes itself, for example by calling nitrovin's batch decode.

**Lookups and indexes.** These are the lookups, and the indexes the documentation
says a source should have:

- shelf, by fitment keys: `(type, number, side)`;
- shelf, rows with no number: `(part_type, vehicle_<fields>, year)`;
- yard, by vPIC model: `(decoded_model, decoded_year)`;
- yard, by catalog vehicle: `(vehicle_<fields>, year)`.

All the stock lists of one answer are fetched together: at most one query per
source and list kind, each bounded by the page.

**At first connection** nitrovin checks the relation's columns against
`information_schema`. A missing or mistyped column makes the source `unavailable`,
with the reason. Nothing guesses.

## Reading and failure

- Every query runs in a `READ ONLY` transaction.
- Defaults, each settable in the header: `statement_timeout` 1 s,
  `max_connections` 4, connect timeout 2 s.
- **A database down at start does not stop nitrovin.** The source answers
  `unavailable` and retries at most every 5 s, so a dead database does not pile up
  connections.
- **A slow or failed query fails only its stock list.** The list answers
  `unavailable` with a reason (ADR 0019), and the rest of the answer stands.
- **Nothing is cached.** A request-time source is read at request time.

## Files and connectors together

Sources are named and independent, with no precedence. A list holds the rows of
every loaded source of its kind, each row tagged with its source, and the answer
carries each source's block.

## Surfaces, driver and licence

The connector works wherever a stock file does in the Go binary: `serve`, stdio,
the CLI and MCP. The C library and the browser (ADR 0013) read stock files only,
so neither carries a database driver.

The driver is `pgx/v5` (MIT). Through `pgxpool` it adds about 0.5 MB to a 75 MB
binary and 16 packages. It lives in its own package, `pkg/stock/pg`, which
`pkg/vocab` never imports, so the WebAssembly build is unchanged.

## Testing

CI runs the connector against a Postgres service, as the oracle tests already do:

- the synthetic shelf and yard rows (ADR 0019's fixtures) are loaded into views;
- the connector's answers must equal the same rows loaded as files, with the same
  candidates in the same order;
- an unreachable database must answer `unavailable`;
- a missing column must be reported.

## What "offline" still promises

The glossary's opening line becomes: "A decoder that turns a VIN into the vehicle
NHTSA's vPIC says it is, shipped as one binary with its data inside. It translates
names and answers fitment from files it loads, and reads stock from a database
only when a stock source says to."

Decode, translate and fitment never touch the network. Only a configured
connector does.

## Considered options

- **Flags per connector** (`--stock name=…`): a second way to name sources, beside
  files.
- **The source's own SQL per lookup:** nitrovin would run a caller's code, which
  the vocabulary map ruled out (plugins are data, not code).
- **Refusing to start without the database:** it turns a stock outage into a
  decode outage.
- **Caching answers:** stale stock, which is what a request-time source exists to
  avoid.
- **A driver in the C library:** the library's size and its dependency-free build
  for a feature only a server uses.
- **Precedence between a file and a connector:** names already keep sources apart.

## Consequences

- [#243](https://github.com/joshuasprow/nitrovin/issues/243) builds stock on one
  interface. A connector ticket adds `pkg/stock/pg` behind it.
- The documentation lists the columns and indexes, and gives a worked view over a
  synthetic schema.
- Measured when [#245](https://github.com/joshuasprow/nitrovin/issues/245) built
  it: `pgxpool` adds 4.3 MB to the 75.4 MB release binary (`-trimpath
  -ldflags='-s -w'`), not 0.5 MB, most of it pgx's type codecs and the
  `golang.org/x/text` tables its SCRAM password normalisation links. The C
  library and the WebAssembly build are unchanged.
