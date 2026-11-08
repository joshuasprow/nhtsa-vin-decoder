# Deviations are classes with detectors, gated against a freshened oracle

The decoder recomputes each (WMI, year) correction charset from the dump's
pattern keys, as ultravin does, and never ships `wmiyearvalidchars`. The dump
carries that table as a materialised copy of
`fExtractValidCharsPerWmiYear`, and `spvindecode_errorcode` reads it instead of
recomputing whenever a cell has rows. In 2026_10 the copy is stale in 5,706 of
its cells (ultravin's scan). Live vPIC answers the recompute's way on all seven
VINs ultravin registered for this class, checked field by field on
2026-11-08. Carrying the table would also add 141 MB raw (a 202.6 MB
artifact), over GitHub's 100 MiB file block for the raw artifact ADR 0003
embeds.

The refresh therefore gates against a **freshened oracle**: the same dump, the
same `spvindecode`, with `vpic.wmiyearvalidchars` truncated in a copy of the
database. Only `spvindecode_errorcode` reads that table, so an empty one sends
every charset read to the dump's own fallback and changes nothing else. On
2026_10 the freshened oracle reproduced ultravin's pinned answer on 100 of 100
VINs ultravin excuses as stale-cache (stock Postgres: 0 of 100). It changed
nothing on 100 VINs ultravin does not excuse, and it equalled live vPIC on 35
of 35 fields over the seven witnesses. The gate is zero-tolerance: nitrovin
must equal the freshened oracle on every VIN, with no excuse list. A stock run
over the same corpus enumerates the stale-cache class as the stock-vs-freshened
diff. That list goes in the refresh report and as a release asset, and is
never committed. This amends
[Two oracles](https://github.com/joshuasprow/nhtsa-vin-decoder/issues/3):
Postgres is truth with its derived cache emptied.

The registry, `docs/deviations/`, holds **classes, never per-VIN entries**. A
class is admitted by a PR that adds one file naming:

- the defective upstream artifact, and how it is defective;
- a mechanical **detector** the refresh runs to find the class's VINs;
- at least one **witness**, a VIN on which live vPIC answers the way nitrovin
  does.

A defect that no machine can detect is not admitted: the decoder matches the
oracle instead. Neither of ultravin's files (`known_problems.json`,
`stale_cache_cells.json`) is copied. Our detector regenerates the class from
our own dump each month, and ultravin's seven VINs are the class's witnesses,
credited in its file.

Where Postgres raises (a pattern key its regex engine rejects), the decoder
returns a decode error, as `pkg/decoder` already does. That is agreement with
the oracle, not a deviation. This amends the crash example in
[Adopt ultravin's deviation registry](https://github.com/joshuasprow/nhtsa-vin-decoder/issues/4),
which named such a crash as a defect we would not reproduce.

A class's status is derived each month, never stored. Every detector runs on
every refresh, healed classes included. The report gives each class's count
and flags any class that healed or relapsed. Neither transition fails the
refresh, because the gate forgives nothing that a healed class could leave
behind. Retiring a class file is a human PR. Each refresh re-queries live vPIC
for every witness without gating, because live data runs ahead of the dump.

When nitrovin differs from the freshened oracle on any VIN, the refresh stays
red and no data release ships; last month stays current. The report gives the
per-element diff and the stock and live-vPIC answers for each diverging VIN.
The default reading is a decoder bug. There is no pending or time-boxed excuse.

## Considered options

- **Ship only the stale cells**, about 0.6 MB at most (5,706 cells × 11
  positions × an 8-byte set). This gives byte parity with stock Postgres, but
  it disagrees with live vPIC and with ultravin. It would also reproduce a
  defect, which [Adopt ultravin's deviation registry](https://github.com/joshuasprow/nhtsa-vin-decoder/issues/4)
  ruled out.
- **Ship the whole cache**: the same outcome, 230× the size, and over the file
  block.
- **Port ultravin's adjudicator against stock Postgres.** That means a
  stale-cell scan, an excuse rule confined to the error elements
  142/143/144/156/191, per-VIN counterfactuals, and hand registration for
  VINs whose vehicle changes: about 1.2k lines of Python and Rust in ultravin,
  where the freshened oracle proves every member by construction.
- **Per-VIN entries as a fallback detector**, as in ultravin's
  `known_problems.json`. This would bring back hand-registered VINs and the
  machinery to expire them.
- **Failing the refresh when a class heals**, ultravin's rule. That rule
  protects a registry whose entries excuse divergences. Here, nothing is
  excused against the gate.
- **A class for element 144's order.** The C-collated Postgres oracle once
  printed `_` after the alphanumerics, unlike SQL Server (ultravin
  KNOWN_DEVIATIONS §3). Dump 2026_09 changed the `ORDER BY` to put `_` first
  (`spvindecode_errorcode.sql:54,65,90`), so parity alone yields vPIC's order.

## Consequences

- The fallback creates a temporary table per pattern key, so a freshened
  decode costs about 2× a stock one (mean 25 → 51 ms on 2026_10; about 10× on
  WMIs with many keys, such as 1M1 and JPA). A batch in one statement
  exhausts `max_locks_per_transaction`, so freshened decodes run one VIN per
  transaction. With the stock run, a refresh decodes for about 3× as long as
  the stock oracle alone.
- `scripts/oracle.sh` and `oracle.yml` need a freshened mode. The refresh
  corpus always includes every witness.
- On the PR gate, nitrovin and ultravin agree on the stale-cache class. They
  differ wherever Postgres raises and ultravin answers, which ultravin's
  answer key marks `!`.
