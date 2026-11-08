# Deviations

nitrovin matches its oracle byte for byte, except on the classes registered
here. Each one is a class of VINs on which the oracle's own data is shown to
be defective. Policy and reasons: [ADR 0006](../adr/0006-deviations-are-detected-classes.md).
Terms: [`GLOSSARY.md`](../../GLOSSARY.md).

## Classes

| class | defective artifact | detector | first seen |
|---|---|---|---|
| [`stale-cache`](stale-cache.md) | `vpic.wmiyearvalidchars`, a stale copy of the dump's pattern keys | stock ≠ freshened oracle | 2026_10 (ultravin: 2026_08) |

This table lists classes, not their status. A class is active in a month when
its detector finds VINs and healed when it finds none. Each month's refresh
report says which, and flags any class that changed.

## A class file

One file per class, `docs/deviations/<class>.md`, with these sections:

- **Root cause.** Name the defective upstream artifact (a table, cell, row or
  procedure line in the dump) and show how it is defective. An output diff is
  never a root cause: it is the thing being explained.
- **Detector.** The mechanical test the refresh runs to find the class's VINs
  in a corpus, and where it is implemented. A detector derives membership from
  the dump and the oracle, never from nitrovin's output.
- **What nitrovin does.** The answer the decoder gives instead of the
  oracle's.
- **Witnesses.** VINs that exhibit the class, each with the stock oracle's
  answer, live vPIC's answer (which equals nitrovin's) and the date live vPIC
  was checked.
- **Credit.** Who found it first, if it wasn't us (`NOTICE`).

## When the refresh goes red

The refresh gate compares nitrovin with the freshened oracle and allows no
differences. A red refresh ships no data release, and the current month stays
current. The report lists each diverging VIN with:

- the elements that differ, by id and code;
- nitrovin's, the freshened oracle's and the stock oracle's values;
- live vPIC's answer.

Read a divergence as a decoder bug first and fix the decoder. Open a new class
only when every one of these holds:

1. The root cause is a named defect in the dump, not a difference you have
   observed.
2. Live vPIC answers at least one witness VIN the way nitrovin does.
3. A detector can find every member mechanically, and the refresh can run it
   against a freshened or counterfactual oracle that stays zero-tolerance for
   everything outside the class.

A class that fails any of these is not admitted, and the decoder matches the
oracle instead. Nothing gets a per-VIN or temporary excuse.

## Healing and retiring

Every detector runs on every refresh, healed classes included, so a relapse is
reported as well as a heal. Neither fails the refresh. A class that has stayed
healed long enough to be uninteresting is retired by a PR that deletes its file
and its row above.

Every refresh re-queries live vPIC for every witness. A witness on which live
vPIC no longer agrees with nitrovin is reported, not gated, because live vPIC
runs on newer data than any dump. A human decides whether the class still
stands.
