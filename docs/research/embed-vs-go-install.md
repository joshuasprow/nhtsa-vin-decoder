# Embedding an 85 MB monthly artifact without breaking `go install` or git

Ticket: [#15](https://github.com/joshuasprow/nhtsa-vin-decoder/issues/15).
Date: 2026-11-07. Decisions this builds on: #5 (embed by default, `--data`
loads another), #9 (unattended refresh), #10 (five channels incl. `go install`).

**Recommendation: scheme E = C + D.** A second module,
`github.com/joshuasprow/nhtsa-vin-decoder-data`, carries one zstd-compressed
artifact per data month and is tagged `v<schema>.<YYYYMM>.<rebuild>`; the main
module `require`s one exact version and decompresses at init. Release binaries,
`go install` and the Go module surface all get the same bytes through the module
proxy; the code repo never holds an artifact; a data month is a `go.mod` line.
Measured end to end in §6.

## 1. Facts from primary sources

### 1.1 Go: embed, generate, install

| Fact | Source |
| --- | --- |
| `//go:embed` fills a `string`, `[]byte` or `embed.FS` "with the contents of files read from the package directory or subdirectories at compile time". | `go doc embed`, Go 1.27.3 |
| "Patterns must not match files outside the package's module … or any directories containing go.mod (these are separate modules)." So the embed directive must live in the module that owns the file. | `go doc embed` |
| "each pattern in a `//go:embed` line must match at least one file or non-empty directory"; a directory pattern like `data/*` is satisfied by a dotfile (`image/*` embeds `image/.tempfile`). | `go doc embed` |
| A zero-byte file embeds; the binary is 2.4 MB. A directory pattern over `data/.gitkeep` alone builds and `ReadFile("data/vpic.bin")` returns "file does not exist". | measured, §5 |
| "Go generate is never run automatically by go build, go test, and so on. It must be run explicitly." Go has no build-script hook; nothing can fetch a file during `go install`. | `go help generate` |
| `go install pkg@version` "builds packages in module-aware mode, ignoring the go.mod file in the current directory"; the named module's `go.mod` "must not contain directives (replace and exclude)"; "Vendor directories are not used in any module." | `go help install` |
| Major ≥ 2 requires a `/vN` path suffix; "Major version suffixes are not allowed at major versions v0 or v1." So `v2026.10.0` would need the path `…-data/v2026`; `v1.202610.0` does not. | https://go.dev/ref/mod#major-version-suffixes |

### 1.2 Module zips and the proxy (verbatim)

> A module zip file may be at most 500 MiB in size. The total uncompressed size
> of its files is also limited to 500 MiB. go.mod files are limited to 16 MiB.
> LICENSE files are also limited to 16 MiB. These limits exist to mitigate
> denial of service attacks on users, proxies, and other parts of the module
> ecosystem. Repositories that contain more than 500 MiB of files in a module
> directory tree should tag module versions at commits that only include files
> needed to build the module's packages; videos, models, and other large assets
> are usually not needed for builds.

— https://go.dev/ref/mod#zip-path-size-constraints

Other constraints from the same section: files in `vendor/` and in
subdirectories with their own `go.mod` are excluded; symlinks are ignored; file
names are restricted to letters, digits and `!#$%&()+,-.=@[]^_{}~`.

| proxy.golang.org behaviour | Source |
| --- | --- |
| New tagged versions appear within a minute of an explicit `go get module@version`; a version requested before the tag existed can take 30 min. | https://proxy.golang.org/ FAQ |
| Caches deleted versions: "this bad release may still be available in the mirror even if it is not available at the origin. The same situation applies if you delete your entire repository." Use `retract` to hide. | https://proxy.golang.org/ FAQ |
| "proxy.golang.org does not save all modules forever … if proxy.golang.org is not able to detect a suitable license … only a temporarily cached copy". | https://proxy.golang.org/ FAQ |
| License detection: a recognised license (MIT, CC0, Unlicense…) in `LICENSE`/`COPYING`/… at the module root. | https://pkg.go.dev/license-policy |
| The proxy "just calls `go mod download`" (Go team, closed-source service). | https://github.com/golang/go/issues/47241#issuecomment-882724296 |
| A module may also be served from a static host: a `go-import` meta tag with VCS `mod` makes the go command (and therefore the proxy) fetch `@v/<v>.info`, `.mod`, `.zip` from any URL using the proxy protocol. | https://go.dev/ref/mod#serving-from-proxy |

### 1.3 Git LFS is invisible to Go

| Fact | Source |
| --- | --- |
| The go command builds a module zip from `git archive --format=zip`. | `$GOROOT/src/cmd/go/internal/modfetch/codehost/git.go:928` (go1.27.3) |
| GitHub's own archives: "By default, Git LFS objects are not included in these archives, only the pointer files to these objects." | https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/managing-repository-settings/managing-git-lfs-objects-in-archives-of-your-repository |
| "The Google-hosted `proxy.golang.org` does not smudge git-lfs pointer files in public repos." Open since 2021; the Go team: "changing the way Git behaves while downloading modules is a dangerous thing to do, since `sum.golang.org` cryptographically guarantees module contents stay stable". | https://github.com/golang/go/issues/47241 |
| `GOPROXY=direct` with git-lfs installed yields smudged files and therefore a checksum mismatch against sum.golang.org. | https://github.com/golang/go/issues/41708 |
| Proposal for a `.git-lfs` VCS qualifier: open, unimplemented. | https://github.com/golang/go/issues/47308 |

LFS is therefore not a scheme: a pointer file (~130 bytes) is what the proxy
embeds. Not considered further.

### 1.4 GitHub limits

| Limit | Value | Source |
| --- | --- | --- |
| Single file in git | warning above 50 MiB; blocked above 100 MiB | https://docs.github.com/en/repositories/working-with-files/managing-large-files/about-large-files-on-github |
| Repository size | "ideally less than 1 GB, and less than 5 GB is strongly recommended" | same |
| Release asset | each file under 2 GiB; up to 1000 assets; "no limit on the total size of a release, nor bandwidth usage" | https://docs.github.com/en/repositories/releasing-projects-on-github/about-releases#storage-and-bandwidth-quotas |
| LFS | 2 GB/file on Free; 10 GiB storage + 10 GiB bandwidth/month free, metered beyond; every pushed version counts in full | https://docs.github.com/en/repositories/working-with-files/managing-large-files/about-git-large-file-storage, https://docs.github.com/en/billing/concepts/product-billing/git-lfs |

### 1.5 goreleaser

| Fact | Source |
| --- | --- |
| `before.hooks` run once before the release; `after.hooks` once after; a failing hook aborts. | https://goreleaser.com/customization/general/hooks/ |
| `builds[].hooks.pre/post` run **per build target** (per OS/arch). | https://goreleaser.com/customization/builds/hooks/ |
| Requires a clean tree: "git is in a dirty state" aborts; generated files must be gitignored. | https://goreleaser.com/resources/errors/dirty/ |
| `gomod.proxy: true` builds from proxy.golang.org's zip of the tag, verified against sum.golang.org, so `go version -m` reports the module. A file that is not in the module zip is not in the build. | https://goreleaser.com/customization/builds/verifiable_builds/ |

### 1.6 How ultravin does it

ultravin's artifact "never lives in git or in the crates.io package"; a Cargo
build script resolves it at build time: `ULTRAVIN_DATA` path → `data/vpic.rkyv`
in the checkout → (default `download-data` feature) this version's GitHub
release asset into `~/.cache/ultravin`, blake3-checked against
`data/manifest.json` → else "a tiny but valid empty artifact" and
`Db::embedded()` refuses at runtime
(ultravin@dbc9777:crates/ultravin/build.rs:1-24). The month and hash are pinned
in `crates/ultravin/data/manifest.json` (`"month": "2026_10"`,
`"artifact_blake3": "6630690d…"`, `"artifact_bytes": 84124480`). Code releases
(`v3.0.2`) carry `vpic.rkyv` as an asset; data months get their own release
tags (`data-2026_10`, asset `answerkey-2026_10.jsonl.zst`) — `gh release list`,
2026-11-07. Go cannot copy this: there is no build script (§1.1), so the
scheme that downloads at build time is unavailable to `go install`.

## 2. Measurements

Machine: Apple M5 Pro, 48 GiB, Go 1.27.3, zstd 1.5.7, xz 5.8.4, git 2.54.0. Input:
ultravin's `vpic.rkyv` for three consecutive data months (release assets
`v2.2.1` = 2026_08, `v3.0.1` = 2026_09, `v3.0.2` = 2026_10; months confirmed
from each tag's `crates/ultravin/data/manifest.json`). The artifact format here
is undecided (#17); rkyv stands in for "a dense indexed binary".

### 2.1 Size and month-to-month growth

| Data month | bytes | Δ vs previous |
| --- | --- | --- |
| 2026_08 | 83,372,856 | — |
| 2026_09 | 83,728,544 | +355,688 |
| 2026_10 | 84,124,480 | +395,936 |

≈ 0.38 MB/month. At that rate the raw file crosses GitHub's 100 MiB block
(104,857,600) in ≈ 55 months (mid-2031) — [INFERENCE] linear extrapolation.

### 2.2 Compression of the 2026_10 artifact

| Codec | bytes | ratio | Go decompress (best of 3) | Go package |
| --- | --- | --- | --- | --- |
| none | 84,124,480 | 1.00 | 1.9 ms (memcpy) | — |
| gzip -6 | 20,787,447 | 4.05 | 204 ms stdlib / 147 ms klauspost / 139 ms pgzip | `compress/gzip`, `klauspost/compress/gzip`, `klauspost/pgzip` |
| gzip -9 | 20,609,255 | 4.08 | — | |
| zstd -3 | 19,292,673 | 4.36 | 54 ms | `klauspost/compress/zstd` v1.20.3 |
| zstd -19 | 15,109,332 | 5.57 | 64 ms (60 ms single-threaded) | same |
| zstd -22 --long=27 | 15,036,884 | 5.59 | — | |
| brotli -9 | 15,741,310 | 5.34 | 211 ms | `andybalholm/brotli` v1.2.8 |
| xz -6 | 11,379,448 | 7.39 | 1,070 ms | `ulikunitz/xz` v0.5.17 |

zstd -19 is the knee: 5.6× for 60 ms. xz buys 25 % more for 17× the time.

### 2.3 Git history

Three months committed as one path, `git gc --aggressive`, then
`repack -adf --window=250 --depth=50`:

| Measure | Value |
| --- | --- |
| Loose object per month (zlib) | 20.5–20.8 MB |
| Pack after 3 months | 61,992,870 bytes |
| Delta chains found | none (`%(deltabase)` all zero); 0 of 20,441 4 KiB blocks identical at the same offset between 2026_09 and 2026_10 |
| `zstd -19 --patch-from` 2026_09→2026_10 | 7,345,229 bytes (so a true delta exists; git's heuristic cannot find it in this layout) |

Pack growth ≈ 20.7 MB per committed month, with no delta benefit.

| After 24 months at one artifact/month | Raw artifact committed | zstd-19 artifact committed |
| --- | --- | --- |
| Sum of blob sizes ("without compression": what every historical checkout adds up to) | ≈ 2.12 GB (24 × 84.1 MB + growth) | ≈ 0.36 GB |
| Pack on disk / full clone | ≈ 496 MB (24 × 20.7 MB) | ≈ 363 MB (git cannot recompress zstd) |
| Shallow clone (`--depth 1`) | ≈ 21 MB | ≈ 15 MB |
| GitHub per-file warning (50 MiB) | every push | never |
| GitHub 1 GB recommendation reached | ≈ 4 years | ≈ 5.5 years |

[INFERENCE] 24-month figures are the 3-month measurement × 8.

### 2.4 Module zips, binaries, startup

| Variant | Module zip (deflate, what `go install` downloads) | Zip uncompressed vs 500 MiB | Binary (darwin/arm64) | Startup cost |
| --- | --- | --- | --- | --- |
| raw artifact embedded | 22,202,495 | 84 MB (17 %) | 87,203,266 | 0; `[]byte` is file-backed, max RSS 4.9 MB when untouched |
| zstd-19 artifact embedded | 15,003,757 | 15 MB (3 %) | 17,654,722 | 79 ms incl. zstd reader init; +84 MB heap |
| zero-byte placeholder | 660 | — | 2,429,778 | — |

Cold `go build` of the 87 MB binary: 1.44 s wall (`go clean -cache` first).
Zips made with `golang.org/x/mod/zip.CreateFromDir` v0.42.0, the go command's
own packer.

## 3. Schemes

Requirements: (R1) release binaries fully offline; (R2) `go install` works;
(R3) repo stays small; (R4) a data month can be pinned.

| | A. commit artifact in the code repo | B. placeholder + build-time generate + first-run download | C. separate data module | D. compressed embed | E. C + D |
| --- | --- | --- | --- | --- | --- |
| R1 offline release binary | yes | yes (goreleaser `before` hook fills `data/`) | yes | yes | yes |
| R2 `go install` | yes; 22 MB zip per code release | builds, but needs network and a writable cache on first run; fails air-gapped/Docker/`--read-only` | yes; 22 MB data zip cached across code releases (measured §6) | orthogonal | yes; 15 MB data zip |
| R3 repo small | no: +20.7 MB pack/month, 50 MiB warning every push, 100 MiB block ≈ 2031 | yes (manifest only) | code repo yes; data repo +20.7 MB/month | +15 MB/month wherever committed | code repo yes; data repo +15 MB/month, no warnings |
| R4 pin a month | commit = month | manifest hash + month | `require …-data v1.202610.0` | orthogonal | same as C |
| goreleaser `gomod.proxy: true` | works | **incompatible** (zip lacks the file) | works | works | works |
| Go module surface users | get data via `go get` | must run the download at init; untestable offline | get data via `go get`, can `go get -u` the month | — | same as C |
| Extra moving parts | none | download code, cache dir, hash check, error path, `.gitkeep`+gitignore trick | second repo, second tag, `go.mod` bump in the refresh PR | one decode at init, `klauspost/compress` dependency | both |

Notes per scheme:

- **A.** Simplest, and the proxy tolerates it indefinitely (84 MB ≪ 500 MiB).
  What fails is R3: §2.3. Every code-only release also re-ships the 22 MB zip
  to `go install` users. Committing the zstd file instead (A+D) removes the
  warnings but still grows the code repo 15 MB/month.
- **B.** Verified mechanics: `//go:embed data/*` over a committed `data/.gitkeep`
  with `data/vpic.bin` gitignored builds either way (§5); goreleaser's
  `before.hooks` (once, not per target) writes the file; gitignored files do
  not trip the dirty-tree check. The hash and month live in a committed
  manifest, as ultravin does. But a `go install` binary is not offline, and
  `gomod.proxy` verifiable builds are off the table.
- **C.** The data module is a Go package: `package vindata` with
  `//go:embed vpic.zst` and exported `Compressed []byte`, `Month`, `SHA256`. Its
  root needs a recognised `LICENSE` or the proxy keeps only a temporary copy
  (§1.2). `go install` resolves the main module's `go.mod`, downloads the
  pinned data zip, embeds in the data package, links — no network at run time.
  The data repo grows like A would have, but nobody develops in it, and each
  month can be an orphan commit so a tag deletion past the support window
  (#21) frees it; the proxy and sum.golang.org retain what they cached.
  Variant **C′**: serve the data module from a static host via a `mod`
  go-import tag (§1.2) — no git repo at all, zips are the release assets
  behind a tiny redirector. Needs a domain the project controls; keep as the
  escape hatch if the data repo's growth ever matters.
- **D.** zstd-19: 5.6× smaller zip, binary and git blob for 60–80 ms and 84 MB
  of heap at start. Raw embed is demand-paged and costs nothing at start
  (§2.4), so a server pays for compression once; a one-VIN CLI call pays it
  every time. If #17 picks a read-in-place format and measures that 80 ms
  matters for the CLI, flip the data module to raw: the zip grows 15→22 MB and
  the blob 15→84 MB, nothing else changes.

## 4. Recommendation: E

1. **Module `github.com/joshuasprow/nhtsa-vin-decoder-data`**, own repo, MIT
   `LICENSE` + `NOTICE` (NHTSA public-domain data), one package:
   `Month string`, `SHA256 string`, `Compressed []byte` (`//go:embed vpic.zst`),
   and a manifest JSON like ultravin's (dump URL, dump sha256, row counts) so
   the artifact importer's provenance travels with the bytes.
2. **Version = `v<artifact schema>.<YYYYMM>.<rebuild>`**, e.g. `v1.202610.0`.
   Major is the artifact schema version, so an old decoder can never resolve
   an incompatible layout (a schema break is `/v2` in the path); minor is the
   data month; patch is a re-import of the same month. Valid semver, no
   `/vN` suffix at v1 (§1.1).
3. **Main module pins** `require github.com/joshuasprow/nhtsa-vin-decoder-data v1.202610.0`.
   The refresh (#9) pushes the data tag, waits for
   `go get …-data@v1.202610.0` to populate the proxy (≤ 1 min, §1.2), opens the
   PR that bumps `go.mod`/`go.sum`, runs the gates, merges, tags the decoder.
   `--data <file>` keeps working for any month, from the data release asset.
4. **Decoder decompresses at init** with `klauspost/compress/zstd` (one
   `DecodeAll`, 60–80 ms, §2.2/§6). #17 may flip to raw if it measures that
   the CLI needs 0 ms starts; nothing else in this scheme moves.
5. **goreleaser** needs no hooks for data; `gomod.proxy: true` is available
   since every byte is in a module zip. Release assets still carry the
   artifact file for `--data` and `vin update` (#21).
6. **Data repo hygiene**: one orphan commit per month, tag on it; prune tags
   past the support window #21 sets. GitHub's 1 GB "ideal" arrives after
   ≈ 5.5 years without pruning (§2.3).

What this rules out: LFS (§1.3), committing into the code repo (A), and any
first-run download for `go install` builds (B). What stays open for #21:
whether a code release re-pins the latest month automatically, how long data
tags live, and `vin update` semantics.

## 5. Reproduction

```
# artifacts
gh release download -R blackthorn-interstellar/ultravin v3.0.2 -p vpic.rkyv   # 2026_10
gh release download -R blackthorn-interstellar/ultravin v3.0.1 -p vpic.rkyv -D m09
gh release download -R blackthorn-interstellar/ultravin v2.2.1 -p vpic.rkyv -D m08
# codecs
gzip -6 -c vpic.rkyv > vpic.gz6; zstd -19 vpic.rkyv -o vpic.zst19; xz -6 -k -c vpic.rkyv > vpic.xz6
# git growth
git init g && for m in m08 m09 .; do cp $m/vpic.rkyv g/data.bin; git -C g add .; git -C g commit -qm $m; done
git -C g gc --aggressive; git -C g count-objects -vH
git -C g rev-list --objects --all | awk '{print $1}' | git -C g cat-file --batch-check='%(objecttype) %(objectsize) %(objectsize:disk) %(deltabase)'
# embed sizes: a main package with `//go:embed data/vpic.bin` and `var artifact []byte`,
# built with the raw file, a zero-byte file and vpic.zst19; zips via x/mod/zip.CreateFromDir.
```

Decompression timings: a Go program reading each file into memory and timing
`gzip.NewReader`+`io.ReadAll`, `zstd.Decoder.DecodeAll`, `xz.NewReader`,
`brotli.NewReader`, best of three.

## 6. End-to-end proof of C+D with `go install`

A file-system GOPROXY (`file:///…/proxy`) holding `example.com/vindata
v0.202610.0` (module zip 15,004,649 bytes: `go.mod`, `LICENSE`, `data.go`
with `//go:embed vpic.zst`, the zstd-19 artifact) and `example.com/vin v0.1.0`
(zip 1,073 bytes; `go.mod` requires the data module; `cmd/vin` decodes
`vindata.Compressed` at start):

```
$ GOMODCACHE=$(mktemp -d) GOPROXY=file:///…/proxy,https://proxy.golang.org GOSUMDB=off \
  go install example.com/vin/cmd/vin@v0.1.0
go: downloading example.com/vin v0.1.0
go: downloading example.com/vindata v0.202610.0
go: downloading github.com/klauspost/compress v1.20.3
real 0m1.584s
$ ls -l $GOBIN/vin        # 18,384,578 bytes
$ $GOBIN/vin
2026_10 15109332 -> 84124480 <nil> 79.075292ms
$ du -sh $GOMODCACHE/cache/download/example.com/vindata   # 15M
```

`go mod tidy` in the main module resolved the data module and wrote its
`go.sum` lines; `go install` with a version suffix built from the proxy with
no network at run time. (`v0.…` was used in the proof; §4 recommends `v1.…`.)
