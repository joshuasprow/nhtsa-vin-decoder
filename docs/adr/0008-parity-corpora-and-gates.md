---
status: accepted
---

# Postgres gates every PR and every month; the corpus is committed VINs plus a fresh sweep, under 5,000

Parity is measured against NHTSA's own procedure in Postgres, freshened
(ADR 0006), on pull requests as well as on the monthly refresh. The charting
decision that ultravin would be the differential oracle on PRs is withdrawn:
the dump restores in 21 s and the suites run in about 20 minutes, which is
cheap enough that PRs need no second reference and CI needs no Rust or Python
toolchain. ultravin stays the reference implementation and the source of the
committed VIN list, credited in `NOTICE` and in each corpus file's header.

## The corpus

- **Committed** (`testdata/parity/<month>/corpus.tsv`): every VIN on main
  today (the oracle sample, the core, error and wrapper sets, ultravin's
  434-VIN parity corpus and its 64 cover VINs; about 700) plus every
  deviation witness. The yard-owned 131-VIN inventory set from
  snydersalvage/agent-tools is not published.
- **Generated each run**: VINs synthesized from the month's own patterns to
  reach every schema and WMI, and one-character mutations of corpus VINs for
  the error paths. They gate exactly like the committed set.
- **Bound**: at most 5,000 VINs per run, which is what one `oracle.yml` job
  decodes in both modes inside its 30 minutes. A larger corpus means shards,
  not a weaker gate.

## The gates

- **Zero tolerance.** Every row of every VIN must equal the freshened
  oracle: all 15 columns as a multiset per VIN, group-rank order, exact
  strings, element 144 included. No excuse list, no threshold. A miss is a
  decoder bug or a new deviation class, which needs a PR with root cause,
  detector and witness (ADR 0006).
- **PRs** run the live suites against a restored dump (`import.yml`) and,
  offline, the engine against the committed **frozen answer key**
  (`testdata/parity/<month>/answer-key.csv` with its `manifest.json`), so
  `go test ./...` with an artifact catches a regression in seconds. One
  month's key lives in git and is replaced by the refresh PR; older keys are
  release assets on `nitrovin-data`.
- **The refresh** runs the live gate on the whole corpus, re-freezes the key,
  and reports. The stale-cache detector and the witness re-check against live
  vPIC report heals, relapses and the cell-count delta prominently and never
  gate, as ADR 0006 says. Go coverage of `pkg/decoder` is recorded in the
  report, not gated.

## The report

Per (VIN, element id): the oracle's value and ours, with source and pattern
id, beside the stock oracle's and live vPIC's answers for that element. A
markdown summary table in the refresh PR body and a JSONL file as the run
artifact, so a red month can be classified without re-running anything.

## Considered

- ultravin as the PR oracle (charting D2): fast, but it would make its
  deviations a reference for ours and add a toolchain to CI.
- Frozen key only on PRs: cannot catch a port bug on a VIN the key lacks.
- A stale-cache jump as a soft gate, as ultravin does: NHTSA's data moving
  is not our defect, and the freshened gate already removes it from parity.
- A 100 % coverage gate with an allowance file: a month that stops exercising
  a path (no QC'd schema) would go red for a data reason.
