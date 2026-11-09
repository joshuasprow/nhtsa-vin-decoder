---
status: accepted
---

# Two version lines, one release per month, no `update` command

The decoder and the data change on different clocks. We version them
separately and ship them together: `nitrovin-data` is tagged
`v<format>.<YYYYMM>.<rebuild>` (`v1.202610.0`: major is the artifact format,
minor the data month, patch a re-import of the same month), and `nitrovin`
is plain semver with the data month visible only at runtime (`--version`,
`/v1/info`, every decode). A new data month that passes the gates cuts a
**full** code release: the refresh bumps `go.mod` to the new data tag, tags a
patch (`v1.4.0 → v1.4.1`) and goreleaser builds every channel, so brew, scoop,
winget, Docker and `go install` users get the month by updating as usual. The
`go.mod` bump is the only way the pin moves; a code release embeds whatever is
pinned, which is the newest month that passed (a red month leaves last
month's data in place, and `--version` says so).

There is no `nitrovin update`. ADR 0002 listed it as a data-only command;
with a binary release every month through every channel it would be a second
data path and a user-writable directory to reason about, for no one who has
asked. `--data` and `NITROVIN_DATA` stay the way to run another month.

## What a month leaves behind

A `nitrovin-data` release carries, forever (nothing is pruned): the artifact
(`nitrovin-2026_10.artifact`), NHTSA's dump zip
(`vPICList_lite_2026_10.custom.zip`) so the Postgres oracle can re-run a month
NHTSA no longer hosts, `manifest.json` (dump URL, size and SHA-256; artifact
blake3; row counts; collation; importer version), `checksums.txt` and cosign
keyless signatures (Sigstore, GitHub OIDC). NHTSA's terms allow the
redistribution. Every `nitrovin` release carries the binaries per OS/arch,
the six C libraries, `checksums.txt` and signatures; no SBOM yet. Docker
images are tagged `v1.4.1`, `v1.4`, `v1`, `latest` and `data-2026_10`, one
multi-arch manifest, running `serve --addr :8080`.

## Compatibility

An artifact loads when its format major equals the binary's; any month within
the major loads, older or newer, because the engine reads elements and
lookups from the artifact. A mismatch is an `#internal` problem naming both
versions. A format break is a new data major and a new decoder major.

## Before v1.0.0

`v0.x.y` tags from now, marked prerelease; v0 means "the pipeline is not yet
proven", not "the API may change" (the contract is frozen at `/v1`).
`v1.0.0` requires: `nitrovin-data` embeds an artifact and `Info.DataSource`
is `embedded`; the refresh has run unattended end to end at least once
(detect → build → gates → data tag → `go.mod` PR → auto-merge → code tag →
goreleaser); the parity corpora and gate thresholds are decided and built;
brew, scoop, ghcr.io and `go install` are live and the winget manifest is
submitted (its review may trail); and the public docs cover every surface.

## Considered

- `v1.4.0+2026.10` build metadata: Go modules reject it in tags.
- Data-only releases with `update` for binaries: two release mechanisms,
  and package-manager users fall behind.
- Pruning data tags after 12 or 24 months: breaks `go install` of an older
  decoder tag and the reproduction of its release; GitHub's size guidance
  is ~5.5 years away.
