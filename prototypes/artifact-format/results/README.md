# Bake-off results (PROTOTYPE)

Data month 2026_10 (`vPICList_lite_2026_10`, zip sha256 `373e6a87…d2c1`), Apple M5 Pro,
go1.27.3 darwin/arm64, zstd 1.5.7. Every number is from `measure.sh`; raw output in
[`raw.txt`](raw.txt). 5,028 VINs: the 25 oracle sample VINs plus 5,000 synthesized from
real model patterns (`bakeoff genvins`, seed 17); 4,354 match at least one pattern.
All three candidates produce the same checksum over every decode (`958b1bf8c2059f1b`).

Stub hot path = sanitize, WMI, raw year, C1 WMI row + availability, C2 bands → schema
QC → every pattern of the schema (566.7 visited per VIN), key match, C8-shaped dedup,
lookup resolve. The full engine adds C3–C20 and the correction charset, which re-reads
the same pattern keys: [INFERENCE] 2–4× the stub's data work per VIN.

| | custom indexed binary | flatbuffers | SQLite (modernc) |
|---|---|---|---|
| build, TSV → artifact (TSV load 0.53 s shared; extract 2.5 s shared) | 0.12 s | 0.21 s | 2.3 s |
| deterministic (two builds, one sha256) | yes | yes | yes |
| artifact, raw | 61.7 MB | 81.0 MB | 71.2 MB |
| zstd -19 | 11.1 MB | 26.6 MB | 19.8 MB |
| deflate -6 (≈ module zip and git pack growth per month, raw) | 13.8 MB | 32.6 MB | 27.2 MB |
| with `wmiyearvalidchars` (8.8M rows): raw / zstd -19 | 202.6 / 11.6 MB | 292.4 / 76.9 MB | 442.0 / 61.8 MB |
| open, raw (mmap or embedded `[]byte`) | 0.05–0.45 ms (+0.2–6 ms one-time verify) | 0.04–0.8 ms | 0.5–2 ms |
| open, zstd (decompress to heap) | 50 ms | 103 ms | 68 ms |
| one-VIN process, `--data` raw file (median of 20) | 3.4 ms | 3.5 ms | 4.5 ms |
| one-VIN process, `go:embed` raw (median of 20) | 4.8 ms | 4.4 ms | 5.4 ms |
| one-VIN process, `go:embed` zstd (median of 20) | 54 ms | 107 ms | 73 ms |
| max RSS, one-VIN embedded process: raw / zstd | 8.7 / 82 MB | — | — |
| embedded binary: raw / zstd | 72.8 / 21.7 MB | 92.2 / 37.3 MB | 82.4 / 30.5 MB |
| **warm VIN/s, one core** (target ≥ 20k) | **170k** | **46k** | **4.0k** (6.3k with LIKE pushed into SQL) |
| p50 / p99 / p99.9 / max µs (target < 1 ms) | 4.3 / 23 / 37 / 626 | 16 / 76 / 94 / 1,099 | 173 / 1,038 / 1,350 / 12,618 |
| allocations per decode | 3 | 3 | 5,284 (734 pushdown) |
| inspect a parity miss | own `dump` command (table directory, rows by column=value) | `flatc --json` with the schema: 350 MB of JSON in 0.9 s; no query | `sqlite3` + SQL |
| tooling | none | `flatc` to regenerate accessors on any schema change | none (pure-Go driver) |

SQLite as a container instead (read every table into Go slices at open, decode from the
heap): 951 ms to load 2.18M rows, paid by every CLI call.
