# nhtsa-vin-decoder

An offline VIN decoder that answers like NHTSA's vPIC, as one binary.

Status: **charting**. Nothing decodes yet. The plan is the
[wayfinder map](https://github.com/joshuasprow/nhtsa-vin-decoder/issues?q=label%3Awayfinder%3Amap)
in this repo's issues; decisions already made are indexed there.

What it will be, as decided so far:

- A Go port of NHTSA's `spVinDecode` procedure, proven against two oracles:
  [ultravin](https://github.com/blackthorn-interstellar/ultravin) on every PR
  and NHTSA's own procedure in Postgres on every monthly data refresh.
- The vPIC data embedded in the binary (`--data <file>` to load another
  month), rebuilt from NHTSA's monthly dump by an unattended pipeline.
- Surfaces: CLI, HTTP, stdio JSON lines, MCP over stdio, a Go module, and a
  C-shared library.
- Released per OS/arch on GitHub Releases, Homebrew, Docker, winget/scoop.

MIT licensed. See `NOTICE` for data provenance and the projects this builds
on.

## The Postgres oracle

`scripts/oracle.sh` restores a month of NHTSA's dump into Postgres 17 and
runs its `spvindecode` over a VIN list, writing an answer key (CSV, every
row the procedure emits per VIN, in a stable order). CI runs it through the
reusable workflow `.github/workflows/oracle.yml`; on a workstation with
Docker:

```sh
scripts/oracle.sh up
scripts/oracle.sh fetch "$(scripts/oracle.sh month)" /tmp/vpic   # prints backup=<path>
scripts/oracle.sh restore /tmp/vpic/vPICList_lite_<YYYY_MM>.backup
scripts/oracle.sh decode testdata/oracle/sample-vins.tsv answer-key.csv
scripts/oracle.sh down
```
