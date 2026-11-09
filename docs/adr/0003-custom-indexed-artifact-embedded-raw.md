# The artifact is a custom indexed binary, embedded raw

The decoder reads NHTSA's data from one monthly artifact. We chose a format of
our own and embed it in `nitrovin-data` uncompressed. Each table is stored as
fixed-width little-endian column arrays sorted by its lookup key, all strings
share one interned arena, and a table directory header points into both. Every
index is a binary search over a column, read in place from the embedded
`[]byte` or an mmap'd `--data` file. We
[measured three candidates on the 2026_10 dump](https://github.com/joshuasprow/nitrovin/blob/prototype/artifact-format/prototypes/artifact-format/results/README.md).
Each ran the same stub decode and returned identical results. On one core the
custom format did 170k VIN/s with p99 23 µs. Flatbuffers did 46k VIN/s, too
little headroom once the full engine does 2–4× the stub's work. SQLite through
`modernc.org/sqlite` did 4–6k VIN/s, below the 20k VIN/s target before the
engine exists. The custom format was also the smallest: 61.7 MB raw, 11.1 MB
zstd-19, 13.8 MB deflated. A rebuild from the same dump gives the same bytes,
and the format needs no code generator.

Raw overrides the embed research's zstd-19 recommendation. That research
expected the choice to flip if the format could be read in place, and it can.
A raw artifact opens in under 1 ms, and a one-VIN run peaks at 8.7 MB of
memory. zstd adds 50 ms to every start and 75 MB of heap. The cost of raw is a
13.8 MB `go install` download instead of 11.1 MB, and a GitHub large-file
warning on every monthly push to `nitrovin-data`, since 62 MB is over the
50 MiB warning line. The 100 MiB block is years away.

## Considered options

- SQLite (`modernc.org/sqlite`, pure Go): the best debugging story, since a
  parity miss is one `sqlite3` query away. Too slow when queried per decode.
  Loading everything into Go at open instead costs 951 ms on every CLI call.
- Flatbuffers: reads in place and `flatc --json` can inspect it. It is 3.7×
  slower than custom in this test, its files are the largest (81 MB raw,
  26.6 MB zstd), and `flatc` must regenerate accessors on every schema change.
- Custom indexed binary: chosen.

## Consequences

- The format has no general-purpose viewer, so we have to write and maintain a
  `dump`-style inspection command that prints the table directory and the rows
  matching a column value.
- Readers assume a little-endian host. Every target we ship is little-endian:
  linux, darwin and windows on amd64 and arm64.
- Strings are `unsafe.String` views into the arena, checked once at load:
  offsets in range and the arena valid UTF-8.

## Amendment: go:embed does not align the artifact

Embedding found that "read in place from the embedded `[]byte`" assumed an
alignment go:embed never promises: `cmd/compile` gives embedded data
`Align = 1`, as a `[]byte` or a `string`, and in one build the artifact sat at
`0x1007ff812`. The reader then copied it to an aligned buffer on every start
(59 MB of heap, a 138 MB peak for one decode). Since
[Embed the artifact in nitrovin-data](https://github.com/joshuasprow/nitrovin/issues/67),
`artifact.Open` reads the column arrays unaligned, in place, on amd64 and
arm64 (every target we ship; checkptr allows it for pointer-free element
types, golang/go#37298), and copies once only on other hosts. One embedded
decode peaks at 21.6 MB. The decision stands; only the reader changed.
