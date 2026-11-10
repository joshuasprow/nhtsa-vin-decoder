# nitrovin

An offline VIN decoder that answers like NHTSA's
[vPIC](https://vpic.nhtsa.dot.gov/). It is a Go port of `spVinDecode`, the
decoding procedure NHTSA publishes inside its monthly vPIC database, and it
ships as one binary with a month of vPIC data inside. No network, no
database, no API key.

Every data month is checked against NHTSA's own procedure running in
Postgres before it ships: every row of every VIN in an 18,318-VIN corpus
must match, byte for byte, or the month does not ship
([what parity means](docs/parity.md)).

```console
$ nitrovin decode -o text 1HGCM82633A004352 | grep -E '^(VIN|Make|Manufacturer Name|Model|Model Year|Body Class|Plant City|Error Code) '
VIN                               1HGCM82633A004352
Body Class                        Coupe
Make                              HONDA
Manufacturer Name                 AMERICAN HONDA MOTOR CO., INC.
Model                             Accord
Model Year                        2003
Plant City                        MARYSVILLE
Error Code                        0
```

Those are 7 of the 43 elements this VIN decodes to (`--shape nitrovin`
lists them).

The same decode is reachable six ways: a CLI, an HTTP server, JSON lines
over stdio, an MCP server for AI assistants, a Go module and a C library.
[nitrovin.app](https://nitrovin.app) runs the latest release's server for
anyone: the form, the `/v1` API (CORS open) and MCP at
`https://nitrovin.app/mcp`, rate-limited per IP.

**Status: v0.x prereleases.** v0 means the release pipeline is not yet
signed off, not that the API may change: the contract is frozen at `/v1`.
v1.0.0 waits on the gates in
[ADR 0007](docs/adr/0007-release-and-versioning.md#before-v100).
nitrovin is not affiliated with or endorsed by NHTSA.

- [Install](#install)
- [Use it](#use-it): [CLI](#cli), [HTTP](#http), [stdio](#stdio-json-lines), [MCP](#mcp), [Go](#go-module), [C](#c-library)
- [Verify a release](#verify-a-release)
- [The data](#the-data)
- [Parity](#parity)
- [Known limits](#known-limits)
- [Performance](#performance)
- [Contributing](#contributing)
- [Credits and licence](#credits-and-licence)

## Install

Every release embeds one vPIC data month; `nitrovin --version` names it.

| Channel | Command | Platforms |
| --- | --- | --- |
| Homebrew | `brew install joshuasprow/tap/nitrovin` | macOS, Linux |
| Scoop | `scoop bucket add joshuasprow https://github.com/joshuasprow/scoop-bucket` then `scoop install nitrovin` | Windows amd64, arm64 |
| winget | `JoshuaSprow.Nitrovin`, not yet submitted ([#78](https://github.com/joshuasprow/nitrovin/issues/78)) | Windows |
| Docker | `docker run --rm -p 8080:8080 ghcr.io/joshuasprow/nitrovin` | linux/amd64, linux/arm64 |
| Go 1.27+ | `go install github.com/joshuasprow/nitrovin/cmd/nitrovin@latest` | anywhere Go builds |
| Archives | [GitHub Releases](https://github.com/joshuasprow/nitrovin/releases): `nitrovin_<version>_<os>_<arch>.tar.gz` (`.zip` on Windows) | linux, darwin, windows × amd64, arm64 |
| Linux packages | the same releases: `sudo apt install ./nitrovin_<version>_linux_<arch>.deb`, `sudo dnf install ./nitrovin_<version>_linux_<arch>.rpm`, `sudo apk add --allow-untrusted ./nitrovin_<version>_linux_<arch>.apk` | linux amd64, arm64 |
| C library | the same releases: `libnitrovin_<version>_<os>_<arch>` | linux, darwin, windows × amd64, arm64 |

```console
$ nitrovin --version
nitrovin v0.1.2 (data 2026_10)
```

The binary is about 70 MB, most of it the data; an archive is about 21 MB.
The Docker image runs `nitrovin serve --addr :8080` by default, as a
non-root user, and takes any other command in its place
(`docker run --rm ghcr.io/joshuasprow/nitrovin decode 1HGCM82633A004352`).
Its tags are `vX.Y.Z`, `vX.Y`, `vX`, `latest` and `data-<YYYY_MM>`.

The Linux packages install `/usr/bin/nitrovin` and put `LICENSE` and
`NOTICE` in `/usr/share/doc/nitrovin`; removing the package removes all
three. They are release assets, not a package repository, so nothing signs
them for `apt`, `dnf` or `apk` (hence `--allow-untrusted`): verify one with
cosign before you install it,

```sh
TAG=v0.1.5
cosign verify-blob --bundle nitrovin_${TAG#v}_linux_amd64.deb.sigstore.json \
  --certificate-identity https://github.com/joshuasprow/nitrovin/.github/workflows/release.yml@refs/tags/$TAG \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com nitrovin_${TAG#v}_linux_amd64.deb
```

or check it against `checksums.txt` as [Verify a release](#verify-a-release)
shows.

The binaries are not notarized by Apple. Homebrew removes the quarantine
flag for you. A macOS binary downloaded with a browser is killed on launch
until you remove it:

```sh
xattr -d com.apple.quarantine nitrovin
```

## Use it

Every surface takes the same request and returns the same decode
([the API contract](docs/api/README.md)):

| Request field | Default | Meaning |
| --- | --- | --- |
| `vin` | required | 1–17 characters; partial (`*`) and mistyped VINs decode too |
| `model_year` | none | a model year you already know, a hint |
| `shape` | `vpic` | `vpic`: vPIC's `DecodeVinValues` keys (`Make`, `BodyClass`); `nitrovin`: keyed by variable name, with element and attribute ids |
| `full` | `false` | add `items`, every row NHTSA's procedure emits, with its source and pattern |
| `now` | the clock | the instant the decode is as of; fix it for reproducible output |

A VIN is never an error: what is wrong with it is in `error_codes` (vPIC's
codes, `[0]` is clean), with `corrected_vin` and the possible values when
the procedure suggests them. Every decode carries its `data_month` and
`decoder_version`.

### CLI

```sh
nitrovin decode 1HGCM82633A004352                           # JSON, pretty on a terminal
nitrovin decode -o text --model-year 2003 1HGCM82633A004352  # two columns
nitrovin decode --shape nitrovin --full 1HGCM82633A004352    # by variable name, with items
printf '1HGCM82633A004352,2003\n5YJ3E1EA2JF000316\n' | nitrovin decode -   # one JSON line per VIN
nitrovin info                                                # version, data month, artifact hash, elements
```

`decode -` reads one VIN per line, optionally followed by a comma or a tab
and its model year. Exit status is 0 when every VIN was decoded, whatever
its error codes; 1 when nitrovin could not answer (data that will not load,
a refused input line); 2 for a mistake on the command line, such as an
argument VIN over 17 characters. Problems go to stderr as one JSON line.
[CLI contract](docs/api/README.md#cli-nitrovin-decode).

### HTTP

```sh
nitrovin serve        # 127.0.0.1:8080; --addr, or $PORT, to change
```

```sh
curl localhost:8080/v1/decode/1HGCM82633A004352
curl 'localhost:8080/v1/decode/1HGCM82633A004352?model_year=2003&shape=nitrovin'
curl localhost:8080/v1/decode -d '[{"vin":"1HGCM82633A004352"},{"vin":"5YJ3E1EA2JF000316","shape":"nitrovin"}]'
curl localhost:8080/v1/info
```

Open the address in a browser for a decode form: a VIN, an optional model
year, the shape and `full`, rendered on the server, each decode a
shareable URL (`/?vin=1HGCM82633A004352`). A decode shows its elements in
vPIC's groups (empty values hidden in the `vpic` shape until asked), its
error codes in NHTSA's words, a suggested VIN with the differing positions
marked, a link to the same VIN on NHTSA's decoder, and the decode's JSON,
URL, `curl` and `nitrovin decode` equivalents to copy. The empty form says
how to install, configure MCP and verify this version, with links to its
release and data month. The only script, `/copy.js`, adds copy buttons;
without JavaScript everything else works and the text stays selectable.
The form is not part of the API: it is not in the OpenAPI document and may
change.

`POST /v1/decode` takes up to 1,000 requests and returns an array of
decodes in order. Refusals are `application/problem+json` (RFC 9457) with a
`pointer` to the offending value. `GET /v1/openapi.json` serves the
[OpenAPI 3.1 document](docs/api/openapi.yaml); `GET /healthz` is the probe.
There are no CORS headers unless `--cors <origin>` (or `--cors '*'`) names
the browsers that may call `/v1` and `/mcp`. SIGINT or SIGTERM drains
requests in flight and exits 0.

### stdio (JSON lines)

```sh
printf '{"id":1,"vin":"1HGCM82633A004352"}\n{"id":2,"vin":"5YJ3E1EA2JF000316","shape":"nitrovin"}\nnot json\n' | nitrovin stdio
```

One request per line in, one line out per request, in order and flushed per
line: `{"id": …, "decode": {…}}` or `{"id": …, "error": {…}}`. A line with
`"op": "translate"` or `"op": "list"` translates or lists instead, through
the `--vocab` files, and its reply is keyed `translation` or `list`. A bad
line gets an error line and the session goes on; EOF ends it. This is the
easiest way to keep one decoder process open from another language
(`docker run --rm -i ghcr.io/joshuasprow/nitrovin stdio` works too).
[stdio contract](docs/api/README.md#stdio-nitrovin-stdio).

### MCP

`nitrovin mcp` is an MCP server over stdio with the tools `decode_vin`,
whose arguments are the request above, and `translate` and `list`, which
answer from the vocabularies `--vocab` loads
([tool definitions](docs/api/mcp-tools.json)). For Claude Desktop
(`~/Library/Application Support/Claude/claude_desktop_config.json` on
macOS, `%APPDATA%\Claude\claude_desktop_config.json` on Windows) and Cursor
(`~/.cursor/mcp.json`) the entry is the same:

```json
{
  "mcpServers": {
    "nitrovin": {
      "command": "/opt/homebrew/bin/nitrovin",
      "args": ["mcp"]
    }
  }
}
```

Use the absolute path `which nitrovin` prints: desktop apps do not see your
shell's `PATH`. With Docker instead, `"command": "docker"` and
`"args": ["run", "--rm", "-i", "ghcr.io/joshuasprow/nitrovin", "mcp"]`.
A client that installs from the [MCP Registry](https://registry.modelcontextprotocol.io)
finds this server as `io.github.joshuasprow/nitrovin`: the Docker image at
the release tag, run with `mcp`.
To try it without a client, save the snippet as `claude_desktop_config.json`
and call the tool with the MCP Inspector:

```sh
npx @modelcontextprotocol/inspector --cli --config claude_desktop_config.json --server nitrovin \
  --method tools/call --tool-name decode_vin --tool-arg vin=1HGCM82633A004352
```

`nitrovin serve` serves the same server over Streamable HTTP at `/mcp`,
stateless and with JSON responses, for clients that take a URL instead of a
command (`"url": "http://127.0.0.1:8080/mcp"`):

```sh
npx @modelcontextprotocol/inspector --cli http://127.0.0.1:8080/mcp --transport http \
  --method tools/call --tool-name decode_vin --tool-arg vin=1HGCM82633A004352
```

Or use the hosted one, `https://nitrovin.app/mcp`, with no install.

### Go module

```sh
go get github.com/joshuasprow/nitrovin/pkg/nitrovin@latest
```

```go
package main

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
)

func main() {
	d, err := nitrovin.Embedded() // or nitrovin.Open("nitrovin-2026_10.artifact")
	if err != nil {
		log.Fatal(err)
	}
	req, err := contract.ParseRequest([]byte(`{"vin": "1HGCM82633A004352"}`))
	if err != nil {
		log.Fatal(err)
	}
	dec, err := d.Decode(req) // a VIN is never an error: see dec.ErrorCodes
	if err != nil {
		log.Fatal(err)
	}
	out, err := json.Marshal(dec)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(out))
}
```

A `Decoder` is safe for concurrent use. `nitrovin.Embedded` links the data,
about 61 MB, into your binary, and the module download includes it (a
20 MB zip of `github.com/joshuasprow/nitrovin-data`); `nitrovin.Open` loads
an artifact file instead, and a binary that never calls `Embedded` does not
carry the data. Every `pkg/` package is public API:
[pkg.go.dev](https://pkg.go.dev/github.com/joshuasprow/nitrovin/pkg/nitrovin).

### C library

`libnitrovin` (`.so`, `.dylib`, `.dll`) and `nitrovin.h` carry no data:
`nitrovin_open` loads an artifact, the same file `--data` takes, from a
[nitrovin-data release](https://github.com/joshuasprow/nitrovin-data/releases).
`nitrovin_decode` takes one stdio request line and returns exactly the
reply line `nitrovin stdio` would write; `nitrovin_open_vocab` opens the
artifact with vocabularies beside it, for translate and list lines
([the C library](docs/c-library.md)).

```c
#include <stdio.h>
#include "nitrovin.h"

int main(void) {
  char *err = NULL;
  nitrovin_t *h = nitrovin_open("nitrovin-2026_10.artifact", &err);
  if (h == NULL) {
    fprintf(stderr, "%s\n", err);
    nitrovin_free(err);
    return 1;
  }
  char *reply = nitrovin_decode(h, "{\"id\":1,\"vin\":\"1HGCM82633A004352\"}");
  puts(reply); /* {"id":1,"decode":{...}}, as `nitrovin stdio` writes it */
  nitrovin_free(reply);
  nitrovin_close(h);
  return 0;
}
```

```sh
cc -o decode decode.c -L. -lnitrovin -Wl,-rpath,. && ./decode
```

Free every returned string with `nitrovin_free`, never `free()`, and never
unload the library. [docs/c-library.md](docs/c-library.md) has the
downloads, Python through ctypes, and the rules;
[the C ABI contract](docs/api/README.md#c-abi-libnitrovin-nitrovinh) is
authoritative.

## Verify a release

Releases are signed with [cosign](https://github.com/sigstore/cosign)
keyless signatures from GitHub Actions: no key to fetch, the certificate
names the workflow that built the release. Every asset has a
`<asset>.sigstore.json` bundle and a line in `checksums.txt`.

nitrovin, from the directory you downloaded the assets to:

```sh
TAG=v0.1.2
cosign verify-blob --bundle checksums.txt.sigstore.json \
  --certificate-identity https://github.com/joshuasprow/nitrovin/.github/workflows/release.yml@refs/tags/$TAG \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt
sha256sum --check --ignore-missing checksums.txt
cosign verify ghcr.io/joshuasprow/nitrovin:$TAG \
  --certificate-identity https://github.com/joshuasprow/nitrovin/.github/workflows/release.yml@refs/tags/$TAG \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

nitrovin-data, the data releases (signed from `main`, not from a tag):

```sh
cosign verify-blob --bundle checksums.txt.sigstore.json \
  --certificate-identity https://github.com/joshuasprow/nitrovin-data/.github/workflows/release.yml@refs/heads/main \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt
sha256sum --check checksums.txt
```

`cosign verify-blob --bundle <asset>.sigstore.json … <asset>` checks one
asset directly. A signature from any other workflow or tag is rejected.

## The data

The data is NHTSA's [vPIC](https://vpic.nhtsa.dot.gov/) (Vehicle Product
Information Catalog). NHTSA publishes it once a month as a Postgres dump on
[its downloads page](https://vpic.nhtsa.dot.gov/downloads/), and the
decoding procedure this project ports ships inside that dump. NHTSA's
[terms of use](https://www.nhtsa.gov/about-nhtsa/terms-use) say the
information "is considered public information and may be distributed or
copied", and NHTSA disclaims any warranty on it; as a work of the U.S.
Government it has no copyright (17 U.S.C. § 105). The reading behind this is
[the dump research](https://github.com/joshuasprow/nitrovin/blob/research/nhtsa-dump/docs/research/nhtsa-dump.md#6-terms-of-use).
See [`NOTICE`](NOTICE).

A **data month** (`YYYY_MM`) names one dump. nitrovin converts a dump into
an **artifact**, a 61 MB indexed file that is the same bytes whenever the
same dump is converted, and
[nitrovin-data](https://github.com/joshuasprow/nitrovin-data/releases)
releases it as `v<format>.<YYYYMM>.<rebuild>` (`v1.202610.1`). Each data
release keeps, forever: the artifact, NHTSA's dump zip (so a month can be
re-checked after NHTSA stops hosting it), `manifest.json` (the dump's URL,
size and SHA-256, the artifact's blake3, row counts, the importer commit),
the month's frozen parity key, its stale-cache list and the EPA vocabulary
(below).

```console
$ nitrovin info | jq '{decoder_version, data_month, data_source, artifact_hash}'
{
  "decoder_version": "v0.1.2",
  "data_month": "2026_10",
  "data_source": "embedded",
  "artifact_hash": "blake3:7518c5b46c4264fe94a8e5158cb1949b000e50803b36d388fec9ebe914a780cb"
}
```

To decode against another month, download its artifact and pass it with
`--data` (any command) or `NITROVIN_DATA`; `--data` wins, then
`NITROVIN_DATA`, then the embedded data:

```sh
nitrovin --data nitrovin-2026_10.artifact info
NITROVIN_DATA=nitrovin-2026_10.artifact nitrovin serve
```

An artifact loads when its format major matches the binary's (format 1
today); a mismatch is an error naming both. When NHTSA publishes a month,
an unattended refresh builds it, gates it and, if it passes, cuts a patch
release of nitrovin with it through every channel, so updating nitrovin is
how you get new data. A month that fails ships nothing and the previous
month stays current. There is no `nitrovin update`.

A decode is a function of the VIN, the model-year hint, `now`, the data
month and the decoder version, and it carries all five; the same five give
the same bytes. Live vPIC runs on newer data than any monthly dump, so it
can answer differently from nitrovin on the same day.

Your own names for vehicles and part types (a yard's inventory, a catalog's
codes) load beside the data as vocabulary files, JSON Lines mapped onto
vPIC's makes and models or onto a part-type spine you declare
([`vocabulary.schema.json`](docs/api/vocabulary.schema.json)). Name files or
directories with `--vocab` (repeatable, any command) or `NITROVIN_VOCAB`. A
file with a fault stops nitrovin from starting; where a file disagrees with
the data month (an id NHTSA dropped, say) nitrovin warns on stderr and in
`nitrovin info`, and serves it anyway. `nitrovin vocab check` fails on both,
for your CI:

```sh
nitrovin vocab check vocabularies/
nitrovin --vocab vocabularies/ serve
nitrovin --vocab vocabularies/ list                                  # what is loaded
nitrovin --vocab vocabularies/ list yard --where make=dodge          # a vocabulary's entries, paged
nitrovin --vocab vocabularies/ translate --to yard 1C6RR7LT5FS123456  # a VIN to your names
```

Each data release also carries `epa-vehicles.jsonl`, a vocabulary of the
names EPA's fuel-economy data
([vehicles.csv](https://www.fueleconomy.gov/feg/download.shtml)) gives makes
and models, mapped onto that month's vPIC makes and models with Drive Type
and Series qualifiers where the name implies them. The monthly refresh builds
it from that day's vehicles.csv against the new month and publishes it only
if it loads with no fault; its version is EPA's newest record date and the
data month (`2026-11-03+2026_10`), and the refresh PR reports its drift.
Download it from the release of the month your binary embeds (`nitrovin
--version` names it), or the latest:

```sh
curl -fsSLO https://github.com/joshuasprow/nitrovin-data/releases/latest/download/epa-vehicles.jsonl
nitrovin --vocab epa-vehicles.jsonl info | jq '.vocabularies[] | {name, version, drift}'
```

A vocabulary built against another month loads too, with a drift warning for
each id NHTSA has changed since. `checksums.txt` on the same release covers
it ([Verify a release](#verify-a-release)).

## Parity

nitrovin's answer for a VIN is NHTSA's procedure's answer, not an
approximation of it: every row the procedure emits, all 15 columns, exact
strings, compared as a multiset per VIN. There is no tolerance and no
per-VIN excuse list. The oracle is NHTSA's own `spvindecode` run in Postgres
17 on the month's dump, with one stale cache table emptied: the dump's
cache of valid characters contradicts the dump's own pattern rows, and live
vPIC answers the way the recomputation does. That is the one registered
[deviation](docs/deviations/README.md), [`stale-cache`](docs/deviations/stale-cache.md),
and the corpus VINs it touches are listed with each data release.

The 2026_10 data in v0.1.2 passed
[refresh run 42662006323](https://github.com/joshuasprow/nitrovin/actions/runs/42662006323):
18,339 calls and 966,232 rows, 0 disagreements; every one of the month's
13,014 WMIs and 25,283 of its 25,293 VinSchemas reached; 52 stale-cache
calls; all 7 deviation witnesses agree with live vPIC on 253 fields. Its
report is the body of
[the refresh PR](https://github.com/joshuasprow/nitrovin/pull/80).

[docs/parity.md](docs/parity.md) explains the corpus and the gates, where
each month's report lives, and how to re-run a release's parity check
yourself in a second.

## Known limits

- **10 VinSchemas are never exercised.** In 2026_10, schemas 4329, 5046,
  6173, 11117, 11146, 11395, 11396, 13555, 15597 and 21187 lose to another
  schema on the same WMI and years on every VIN tried, so no gated VIN
  reaches them ([run 42662006323](https://github.com/joshuasprow/nitrovin/actions/runs/42662006323),
  [#72](https://github.com/joshuasprow/nitrovin/issues/72)).
- **Deviations are classes, never VINs.** Where nitrovin disagrees with
  NHTSA's procedure, it is a bug unless a registered class proves a defect
  in NHTSA's data, with a mechanical detector and a VIN on which live vPIC
  agrees with nitrovin ([ADR 0006](docs/adr/0006-deviations-are-detected-classes.md),
  [the registry](docs/deviations/README.md)). There is one class.
- **The `vpic` layout is not live `DecodeVinValues` byte for byte.** It has
  no `VIN` key, has `PlantAddress`, lacks vPIC's ten always-empty legacy
  keys, and joins multi-valued elements in byte order where vPIC's order is
  unstable ([details](docs/api/README.md#layout-vpic-the-default)). Parity
  is gated on the items both layouts are built from.
- **Element names and codes are data, not API.** NHTSA renames them between
  months; pin `element_id` and `attribute_id` for anything long-lived.
- **Over 17 characters is refused.** NHTSA's procedure raises on it, so
  there is no answer to match.
- **Not drop-in for vPIC's URLs.** `serve` has its own `/v1` routes, not
  vPIC's `/api/vehicles/...`.
- **winget** is not live yet ([#78](https://github.com/joshuasprow/nitrovin/issues/78)).
  **macOS binaries are not notarized** (see [Install](#install)).
- **The C library** cannot be unloaded, needs glibc 2.17+ on Linux (no
  musl), and macOS 11+.

## Performance

On one core of an Apple M5 Pro, v0.1.2 decodes about 17,600 VINs a second
through `nitrovin decode -` with JSON output (25,000 corpus VINs in 1.42 s
under `GOMAXPROCS=1`), and a one-VIN `nitrovin decode` process takes about
6 ms and 22 MB of memory.

## Contributing

Issues and pull requests are welcome. [CONTRIBUTING.md](CONTRIBUTING.md)
covers building, the tests, the Postgres oracle on your own machine, and the
rules for a change that touches decoding. Decisions live in
[`docs/adr/`](docs/adr), words in [`GLOSSARY.md`](GLOSSARY.md), and the plan
in the [wayfinder map](https://github.com/joshuasprow/nitrovin/issues?q=label%3Awayfinder%3Amap).

## Credits and licence

MIT, see [`LICENSE`](LICENSE). The data is NHTSA's. The port's structure,
its parity method and its deviation registry follow
[ultravin](https://github.com/blackthorn-interstellar/ultravin) by Bryce
Drennan (MIT), the reference implementation; no ultravin source is copied,
and the committed parity corpus includes ultravin's VIN lists. Details in
[`NOTICE`](NOTICE).
