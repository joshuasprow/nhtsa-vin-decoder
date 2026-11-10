# Vocabularies are JSON Lines loaded once; drift warns and serves

Vocabularies, extension sets and declared spines (ADR 0009, ADR 0010) reach
nitrovin as files in one format
([The vocabulary file](https://github.com/joshuasprow/nitrovin/issues/122),
on [the vocabulary map](https://github.com/joshuasprow/nitrovin/issues/117)),
specified line by line in
[`docs/api/vocabulary.schema.json`](../api/vocabulary.schema.json). A file is
JSON Lines: a header naming its `format` (1), `kind` (`vocabulary`,
`extension-set`, `spine`), `name`, the author's `version` and, optionally, the
`data_month` it was built against; then one line per entry, extension or spine
entry. A vocabulary line carries an entry and all its mappings, so an unmapped
entry is a line with no mappings. It is diffable, streamable and one
`json_agg` away from a SQL export.

A reader might expect a compiled form, since ADR 0003 chose a custom indexed
binary over text for the artifact. That choice paid for 61 MB read in place
on every start. Vocabularies are an author's export, small next to it, and
change between releases rather than with data months, so a `nitrovin vocab
build` step would put a second artifact in every user's pipeline before any
load time has been measured against a budget. Files are parsed and indexed in
memory when they load. A compiled form can be added beside the text one if a
measured load time calls for it.

Files are named with `--vocab <file|dir>`, repeatable, on the root or any
command (a directory loads every `*.jsonl` in it), or `$NITROVIN_VOCAB`, a
path list that flags replace, as `--data` replaces `$NITROVIN_DATA`. They load
once, with the data, and never change for the life of a process: no reload in
`serve`, which picks up new files on restart (the artifact itself opens in
under 1 ms, ADR 0003). A Go `Decoder` stays
vocabulary-free; a new value holds a decoder and its loaded files. The C
library gains an open function that takes the artifact and vocabulary paths
together, so a handle stays immutable and thread-safe. Names are one
namespace across the three kinds, and answers carry a file's name and version
as decodes carry `data_month`; `/v1/info` lists each file's blake3 hash and
path. Entry fields are matched after trimming, collapsing whitespace and
Unicode lower-casing, nothing more.

Faults in a file refuse to start, as a bad `--data` does: exit 1 and an
`#internal` problem naming the file and line. That covers malformed lines, an
unknown format, kind or field, duplicate entries or ids, references to
extensions or spine ids nothing loaded defines, qualifiers on an extension
mapping, a first year after the last, a part vocabulary whose spine isn't
loaded, and two files with one name. An unknown field refuses rather than
being ignored, because a newer field may narrow a mapping that an older
release would then serve too widely. So format 1 grows by additions: new
releases read old files, and old releases refuse new fields loudly.

**Drift** against the data month warns and serves. A vPIC id the month
lacks, a qualifier value its decode cannot produce, or an extension named like
a vPIC entry under the same make is reported on stderr at load and as counts
with examples in `/v1/info`, and the mapping is still served. NHTSA drops and
renames entries from month to month and the refresh ships each month
unattended (ADR 0007), so refusing on drift would turn NHTSA's churn into an
outage for every user whose files were right last month. `nitrovin vocab
check` fails on drift as well as faults, for authors' CI.

## Considered options

- One line per mapping, with the entry's fields repeated: the closest to SQL
  rows, but an unmapped entry needs a mapping-less row and duplicates are
  harder to see.
- One JSON document per file: not streamable or diffable line by line.
- A compiled binary from `nitrovin vocab build`: premature without a measured
  load time.
- A flag per kind: three flags for files whose headers already say what they
  are.
- Refusing on drift: correct in the abstract, an outage in practice.
- Dropping drifted mappings with a warning: strands entries silently when
  NHTSA drops a model.
- Reloading in `serve` (SIGHUP or a file watch): an atomic swap to build and
  test, where a restart already picks up new files.
- Ignoring unknown fields: lets old releases read new files, at the price of
  silently wrong answers.

## Consequences

- The contract ticket decides where name and version sit in answers, the
  `/v1/info` listing, how an answer marks an id the month lacks, the Go and C
  names, and `vocab check`'s output.
- Detecting text-qualifier drift means collecting each text element's values
  from the month's pattern rows at load.
- How long a large vocabulary takes to load is unmeasured. The performance
  budget on the map's Not yet specified list owns it.
