# Building the C-shared surface across OS/arch without a toolchain per target

Ticket: [#14](https://github.com/joshuasprow/nhtsa-vin-decoder/issues/14).
Date: 2026-11-07. Terms per `GLOSSARY.md` (surface, artifact, refresh).

## Answer

**Use `zig cc` as the one C cross-compiler, from a single `ubuntu-latest`
job, for every target but musl.** It produced all seven libraries of the
asked matrix from this darwin/arm64 box and from a Linux container (the
darwin dylibs with no Apple SDK), and the Linux and macOS outputs were
loaded and called. musl is dropped from the matrix because Go's `c-shared`
output cannot be `dlopen`ed under musl at all — a Go runtime defect open
since 2015, reproduced today on Go 1.27.3 with both zig and Alpine's own
gcc — and `dlopen` is how ctypes and .NET load a library.

Recommended matrix (six artifacts, one job):

| GOOS/GOARCH | `CC` | File | Verified |
| --- | --- | --- | --- |
| linux/amd64 | `zig cc -target x86_64-linux-gnu` | `libvin.so` | built on macOS and Linux; loaded via ctypes in debian:bookworm (amd64 emulated) |
| linux/arm64 | `zig cc -target aarch64-linux-gnu` | `libvin.so` | built; loaded via ctypes in debian:bookworm (arm64) |
| darwin/arm64 | `zig cc -target aarch64-macos.11.0` | `libvin.dylib` | built on Linux with no SDK; loaded via ctypes on this Mac |
| darwin/amd64 | `zig cc -target x86_64-macos.11.0` | `libvin.dylib` | built on macOS and Linux; not loaded (no x86_64 host here) |
| windows/amd64 | `zig cc -target x86_64-windows-gnu` | `libvin.dll` | built; PE headers inspected (exports `vin_decode`, imports UCRT only); not loaded |
| windows/arm64 | `zig cc -target aarch64-windows-gnu` | `libvin.dll` | built on macOS and Linux; PE inspected; not loaded |

Dropped: linux/amd64-musl, linux/arm64-musl (see "musl").

## Why cgo breaks cross-compilation, in one paragraph

`-buildmode=c-shared` "Build[s] the listed main package ... into a C shared
library. The only callable symbols will be those functions exported using a
cgo //export comment" (`go help buildmode`, go1.27.3). cgo "is disabled by
default when cross-compiling"; "When cross-compiling, you must specify a C
cross-compiler for cgo to use ... set the CC environment variable any time
you run the go tool" ([cmd/cgo docs](https://pkg.go.dev/cmd/cgo), read via
`go doc cmd/cgo` on go1.27.3). The Go linker then hands the final link to
that `CC`, so a C compiler and a libc for the target are needed per target —
not a Go port. All six asked platforms are in Go's `c-shared` support list:
`go1.27.3:src/internal/platform/supported.go:163-173` (`linux/amd64`,
`linux/arm64`, `darwin/amd64`, `darwin/arm64`, `windows/amd64`,
`windows/arm64`); `go tool dist list -json` marks all six `CgoSupported`,
and windows/arm64 alone as not first-class.

## The four approaches

| Approach | Works today for the six? | Runner(s) | Build time, all targets | Toolchain weight | Gotchas |
| --- | --- | --- | --- | --- | --- |
| **zig cc cross** | Yes, verified (above) | one `ubuntu-latest` | 3–4 s per target warm, 7–12 s on first use of a zig target (it compiles the target's libc shims and caches them), M-series host; ~2–4 min per job on a 4-core runner [INFERENCE] | zig tarball ~50 MB; `mlugg/setup-zig` caches it | musl broken (Go, not zig); darwin minos must be pinned in the target triple; no Windows/darwin load test without a runner of that OS |
| **xgo** | Yes by design: `-buildmode` passes through and `.so/.dylib/.dll` extensions are chosen per OS ([build.sh](https://github.com/techknowlogick/xgo/blob/1f0a5a9/docker/build/build.sh#L38-L45), [L206](https://github.com/techknowlogick/xgo/blob/1f0a5a9/docker/build/build.sh#L206)); not run here | one `ubuntu-latest` + Docker | image pull dominates: `techknowlogick/xgo:latest` is **4.2 GB** (Docker Hub API, `full_size`, updated 2026-10-20) [INFERENCE: 2–5 min pull] | osxcross + macOS 15.5 SDK, mingw-w64, llvm-mingw for windows/arm64 ([toolchain Dockerfile](https://github.com/techknowlogick/xgo/blob/1f0a5a9/docker/toolchain/Dockerfile#L81-L139)) | no musl target at all (`grep -i musl` over build.sh and Dockerfile: nothing); darwin via osxcross means redistributing Apple's SDK in a third-party image; one maintainer; goreleaser "make[s] no promises" about it ([limitations/cgo](https://goreleaser.com/resources/limitations/cgo/)) |
| **native runner per OS** | Yes: `ubuntu-24.04`, `ubuntu-24.04-arm`, `macos-15`/`macos-latest` (arm64), `macos-15-intel`, `windows-2025`, `windows-11-arm` all exist as standard hosted runners ([GitHub docs](https://docs.github.com/en/actions/reference/runners/github-hosted-runners#standard-github-hosted-runners-for-public-repositories)) | six jobs | each job ~1–2 min (checkout + setup-go + one 2–10 s build) [INFERENCE]; wall time is the slowest job | none beyond the image's own clang/gcc/mingw | macOS minutes bill at $0.062/min vs $0.006 Linux ([pricing](https://docs.github.com/en/billing/reference/actions-runner-pricing)) — free on a public repo; an unpinned native macOS build stamps the host OS as the minimum (observed: `minos 27.0` on this box) unless `MACOSX_DEPLOYMENT_TARGET` is set; Windows needs mingw (Go wiki: "you'll also need to first install a gcc compiler (for instance, mingw-w64)" — [go.dev/wiki/cgo](https://go.dev/wiki/cgo)); six places to keep in sync |
| **goreleaser-native** | goreleaser sets `-buildmode`, picks the OS extension and ships the `.h` ([builders/go](https://goreleaser.com/customization/builds/builders/go/#building-shared-or-static-libraries)); but it does not bring a compiler: "Compiling with CGO is tricky ... won't work 'out of the box'" ([limitations/cgo](https://goreleaser.com/resources/limitations/cgo/)) | depends on which of the three it wraps | as the wrapped approach | its own cgo page offers Pro split/merge (native per OS), the `goreleaser-cross` Docker image, or zig — the [example-zig-cgo](https://github.com/goreleaser/example-zig-cgo/blob/6a7570e/.goreleaser.yaml) repo runs on `macos-latest`, uses Apple clang for darwin and zig only for linux (musl) and windows | goreleaser is orchestration, not an answer to the toolchain question; its zig example picks musl for Linux, which is exactly the target that cannot be dlopened as c-shared |

Decision drivers in order: correctness of the output (musl eliminates
itself), one place to maintain (one job beats six), no Apple SDK
redistribution (zig ships only libSystem `.tbd` stubs, xgo ships the SDK),
build minutes (zig: one Linux job).

## What was measured

Environment: go1.27.3 darwin/arm64; zig 0.17.1 via `mise install zig@latest`
(8.5 s); zig 0.15.2 inside `golang:1.27` (debian) for the Linux-host run.
Library under test: one `//export vin_decode(*C.char) *C.char`, no
dependencies. Timings are wall-clock single runs, Go std cache warm.

| Target | Host | Build | Size, default | Size, `-trimpath -ldflags="-s -w"` |
| --- | --- | --- | --- | --- |
| darwin/arm64 (Apple clang) | macOS | 1.5 s | 1,315,010 | 1,213,890 |
| darwin/arm64 (zig, `.11.0`) | macOS | 3.8 s warm | — | 1,217,810 |
| darwin/arm64 (zig) | Linux | 3 s | — | 1,217,842 |
| darwin/amd64 (zig) | macOS / Linux | 7 s / 3 s | 1,367,031 | 1,255,081 |
| linux/amd64-gnu (zig) | macOS / Linux | 8 s / 3 s | 2,781,760 | 1,532,368 |
| linux/arm64-gnu (zig) | macOS | 8 s | 2,710,576 | — |
| linux/amd64-musl (zig) | macOS | 11 s | 2,776,976 | — |
| linux/arm64-musl (zig) | macOS | 12 s | 2,706,040 | — |
| linux/arm64-musl (Alpine gcc, native) | alpine (docker) | 1.25 s | 1,951,352 | — |
| windows/amd64 (zig) | macOS | 10 s | 2,734,592 | 1,524,736 |
| windows/arm64 (zig) | macOS / Linux | 9 s / 4 s | 2,632,192 | 1,390,592 |

Other observations:

- glibc symbol versions needed by the hello library: at most `GLIBC_2.3.2`
  (`readelf --dyn-syms`), with or without pinning `-target
  x86_64-linux-gnu.2.17`. The Go runtime pulls in almost nothing; any glibc
  distro from the last 20 years loads it.
- The Windows DLLs import `KERNEL32.dll` and `api-ms-win-crt-{heap,runtime,
  stdio,string}-l1-1-0.dll` only (pefile): UCRT, the same CRT as MSVC and
  .NET, no `msvcrt.dll`. Export table: `vin_decode`, undecorated.
- The Linux-built darwin/arm64 dylib carries an ad-hoc **linker-signed**
  signature (`codesign -dv`: `flags=0x20002(adhoc,linker-signed)`) and
  `LC_BUILD_VERSION minos 11.0` from the `.11.0` suffix; the Apple-clang build
  on this box stamped `minos 27.0`. macOS arm64 refuses unsigned code, so the
  ad-hoc signature is what makes the zig output loadable at all.
- `cgo` compiles every `.c` in the package directory: a harness `call.c`
  left beside `main.go` broke the build with `duplicate symbol '_main'`.
  Keep C test harnesses out of the library package.

## musl

Both the zig-built and the Alpine-native `.so` fail identically:

```
dlopen: Error relocating ./libvin.so: : initial-exec TLS resolves to dynamic definition in ./libvin.so
```

(observed on alpine:3.21 arm64, Go 1.27.3; Python's ctypes shows it as a
plain segfault.) Linking the library at program start (`gcc -lvin`) instead
of dlopen also segfaulted here, both builds. The cause is in Go, not zig:
Go emits initial-exec TLS accesses, which musl's loader only honours for
libraries present at exec, "not if they will be loaded ... via dlopen"
(musl's author, [golang/go#54805](https://github.com/golang/go/issues/54805#issuecomment-1253972041)).
Status as of today: #54805 and [#13492](https://github.com/golang/go/issues/13492)
(opened 2015) are open; a 2026-07-24 comment reproduces the crash on Go
1.22–1.26; the fix CL [696635](https://go-review.googlesource.com/c/go/+/696635)
is still `NEW` (updated 2026-11-06); the general-dynamic TLS proposal
[#71953](https://github.com/golang/go/issues/71953) was removed from the
proposal process on 2025-09-03.

Consequence: no `libvin.so` for Alpine/musl in v1.0. Alpine callers get the
CLI/stdio/HTTP surfaces (a `CGO_ENABLED=0` static binary runs on musl
without any of this). Re-test when CL 696635 lands; the matrix then gains
two lines with `-target {x86_64,aarch64}-linux-musl`, which already build.

## Gotchas by platform

### macOS: signing and notarization

- Apple's notary service accepts "macOS apps, non-app bundles ... disk
  images ... flat installer packages" and ZIPs; requires a Developer ID
  certificate, hardened runtime, secure timestamp
  ([Notarizing macOS software](https://developer.apple.com/documentation/security/notarizing-macos-software-before-distribution)).
- A bare dylib can be notarized inside a ZIP but **cannot be stapled**:
  "Although tickets are created for standalone binaries, it's not currently
  possible to staple tickets to them"
  ([Customizing the notarization workflow](https://developer.apple.com/documentation/security/customizing-the-notarization-workflow#Staple-the-ticket-to-your-distribution)).
  Gatekeeper then needs the online ticket on first load of a quarantined
  copy. Apple also warns "Avoid huge, non-executable data files" in
  notarized uploads — the 85 MB artifact inside a dylib is exactly that.
- Apps with the hardened runtime load quarantined plug-ins only if notarized
  ([same page](https://developer.apple.com/documentation/security/notarizing-macos-software-before-distribution#Notarize-plug-ins));
  a `curl`ed file carries no quarantine flag, a browser download does.
  .NET and Python hosts are not hardened-runtime by default, so the common
  ctypes/P/Invoke path loads the ad-hoc-signed dylib as-is (verified for
  ctypes here).
- Signing and notarizing **from Linux** is possible with `rcodesign`
  ("Signing Mach-O binaries ... notarizing, and stapling ... from anywhere
  you can get the Rust crate to compile. Linux, Windows, and macOS",
  [apple-codesign README](https://github.com/indygreg/apple-platform-rs/blob/0ebbd2e/apple-codesign/README.md)),
  so the one-job design survives adding Developer ID signing later. That is
  the supply-chain item on the map, not v1.0's.

### Windows: ABI for .NET P/Invoke

- "On x64, ARM, and ARM64 architectures, there is only one calling
  convention, so specifying one explicitly is unnecessary"
  ([.NET calling conventions](https://learn.microsoft.com/en-us/dotnet/standard/native-interop/calling-conventions#platform-default-calling-convention)).
  The MSVC-vs-mingw question is moot for the matrix's two Windows targets;
  it only bites on x86 (32-bit), which is not shipped. `[LibraryImport]` /
  `[DllImport("libvin")]` with default `CallingConvention.Winapi` is right
  ([CallingConvention enum](https://learn.microsoft.com/en-us/dotnet/api/system.runtime.interopservices.callingconvention)).
- mingw-built DLLs link the UCRT (verified above), so memory allocated by
  the DLL's `C.CString` is freed by a `vin_free` export, never by the
  caller's CRT — the API-contract ticket should state that.
- A Go c-shared DLL cannot be unloaded: `dlclose`/`FreeLibrary` is
  unsupported ([golang/go#11100](https://github.com/golang/go/issues/11100),
  open). .NET callers must not `NativeLibrary.Free` it; .NET's default
  `DllImport` never does.
- windows/arm64 is not a first-class Go port (`go tool dist list`), so a
  release-blocking bug there has no Go-team SLA; the DLL builds and inspects
  correctly but was not executed here.

### Linux: glibc baseline and the Go runtime

- `-target x86_64-linux-gnu` without a version suffix already yields a
  library needing only `GLIBC_2.3.2`; pin `.2.17` anyway so an accidental
  future libc call cannot raise the floor silently (zig accepts
  `<arch>-linux-gnu.<glibc version>`, listed by `zig targets`).
- A c-shared library starts the Go runtime inside the host process at load:
  "For -buildmode=c-shared the Go runtime will initialize signals when the
  shared library is loaded"; it installs handlers for the synchronous
  signals and SIGPIPE and keeps existing ones with `SA_ONSTACK`
  (`go doc os/signal`, "Non-Go programs that call Go code", go1.27.3).
  Out of scope here; the API-contract ticket owns it.

## Does the C-shared build need the 85 MB artifact too?

Measured with `//go:embed` of an 85,000,000-byte random file in the same
hello library (warm cache, `-trimpath -ldflags="-s -w"`):

| Build | Without embed | With 85 MB embed | Output size |
| --- | --- | --- | --- |
| darwin/arm64, Apple clang, c-shared | 1.5 s | 0.9–2.0 s | 86,888,610 |
| linux/amd64, zig, c-shared | 6.0 s | 5.5–6.8 s | 86,537,280 |
| windows/amd64, zig, c-shared | 3.1 s warm (10 s first use) | 5.0 s | 86,529,024 |
| linux/amd64, `CGO_ENABLED=0` exe (reference) | — | 1.4 s | 86,245,536 |

Link time: unaffected within noise. The external linker copies the bytes once
into `.rodata`; at run time the loader maps them lazily, so an embedded
artifact costs no RSS until pages are touched. The 3–6 s of a zig build is
zig compiling runtime/cgo's C files and its libc shims for the target, not
the artifact.

What the artifact does cost is the **release**: six libraries × ~87 MB ≈ 520
MB per release, each refresh, versus ~9 MB lean. Two consequences worth a
decision in the API-contract ticket rather than here:

- A single multi-RID NuGet package (`runtimes/<rid>/native/libvin.*`) is
  impossible with embedding: NuGet.org "allows packages up to 250MB"
  ([NuGet FAQ](https://learn.microsoft.com/en-us/nuget/nuget-org/nuget-org-faq)).
  Per-RID packages or a lean library + separately fetched artifact both fit.
- Apple's notarization guidance says to keep large non-executable data out
  of the signed object (above).

Options, both buildable from the same source: (a) embed, matching #5's
"curl && run" default for the binary, and accept the per-RID packaging; (b)
lean library with `vin_open(const char *artifact_path)` and the artifact
shipped beside it, the way `--data <file>` already must work. Build-tag the
embed so one `go build -tags noembed` flips it. The recommendation of this
document: **(a) for the GitHub Release assets, (b) for any package-manager
form of the library**, decided when that contract is written.

## Proposed workflow shape

```yaml
jobs:
  c-shared:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v5
      - uses: actions/setup-go@v6
      - uses: mlugg/setup-zig@v2          # pinned by SHA in practice
      - run: |
          set -e
          build() { CGO_ENABLED=1 GOOS=$1 GOARCH=$2 CC="zig cc -target $3" \
            go build -trimpath -ldflags="-s -w" -buildmode=c-shared -o dist/$1-$2/libvin.$4 ./cmd/libvin; }
          build linux   amd64 x86_64-linux-gnu.2.17   so
          build linux   arm64 aarch64-linux-gnu.2.17  so
          build darwin  amd64 x86_64-macos.11.0       dylib
          build darwin  arm64 aarch64-macos.11.0      dylib
          build windows amd64 x86_64-windows-gnu      dll
          build windows arm64 aarch64-windows-gnu     dll
  smoke:                                  # loads each library on its own OS
    needs: c-shared
    strategy:
      matrix:
        include:
          - { os: ubuntu-24.04,     lib: linux-amd64 }
          - { os: ubuntu-24.04-arm, lib: linux-arm64 }
          - { os: macos-15-intel,   lib: darwin-amd64 }
          - { os: macos-latest,     lib: darwin-arm64 }
          - { os: windows-2025,     lib: windows-amd64 }
          - { os: windows-11-arm,   lib: windows-arm64 }
    runs-on: ${{ matrix.os }}
    steps: [ download artifact, python3 -c "ctypes.CDLL(...).vin_decode(...)" ]
```

Build on one Linux runner; **test** on the six native runners (every label
above exists today, cited in the table). The smoke matrix is what closes the
"not loaded here" gaps for windows/* and darwin/amd64, and it is cheap:
seconds per job. goreleaser can wrap the build step later (`buildmode:
c-shared`, per-target `env: CC=...` via `overrides`) for the `.h` packaging
and checksums; it adds nothing to the toolchain question.

## What this unblocks

- The C-shared surface build slice: toolchain decided (zig), matrix decided
  (six targets, musl deferred with a tracked upstream CL), two gotchas
  turned into contract requirements (`vin_free` for DLL-allocated memory;
  no unload).
- The API-contract ticket: the embed-vs-`vin_open` choice for the library,
  with sizes.
- The release-policy and supply-chain tickets: Developer ID signing of the
  dylibs can be added from Linux with `rcodesign`; standalone dylibs cannot
  be stapled, so notarization buys only the online ticket.

## Sources

| Claim | Source |
| --- | --- |
| cgo cross-compile rule, `//export` | `go doc cmd/cgo` on go1.27.3 (= https://pkg.go.dev/cmd/cgo) |
| c-shared semantics | `go help buildmode`, go1.27.3 |
| c-shared platform list | go1.27.3 `src/internal/platform/supported.go:163-173` |
| Windows cgo needs mingw | https://go.dev/wiki/cgo |
| musl dlopen failure | https://github.com/golang/go/issues/54805, https://github.com/golang/go/issues/13492, https://go-review.googlesource.com/c/go/+/696635 |
| dlclose unsupported | https://github.com/golang/go/issues/11100 |
| zig ships libc for the eight triples | `zig targets` (0.17.1) `.libc` list; https://ziglang.org/learn/overview/ "Zig ships with libc" |
| goreleaser c-shared, cgo stance, zig example | https://goreleaser.com/customization/builds/builders/go/, https://goreleaser.com/resources/limitations/cgo/, goreleaser/example-zig-cgo@6a7570e |
| xgo buildmode, toolchains, image size | techknowlogick/xgo@1f0a5a9 `docker/build/build.sh`, `docker/toolchain/Dockerfile`; Docker Hub tags API |
| hosted runner labels, pricing | https://docs.github.com/en/actions/reference/runners/github-hosted-runners, https://docs.github.com/en/billing/reference/actions-runner-pricing |
| notarization, stapling | https://developer.apple.com/documentation/security/notarizing-macos-software-before-distribution, https://developer.apple.com/documentation/security/customizing-the-notarization-workflow |
| rcodesign from Linux | indygreg/apple-platform-rs@0ebbd2e `apple-codesign/README.md` |
| P/Invoke calling convention | https://learn.microsoft.com/en-us/dotnet/standard/native-interop/calling-conventions, https://learn.microsoft.com/en-us/dotnet/api/system.runtime.interopservices.callingconvention |
| NuGet 250 MB | https://learn.microsoft.com/en-us/nuget/nuget-org/nuget-org-faq |
| ultravin's comparable surface: a Rust `cdylib` Python wheel (`crates/ultravin-py`, `crate-type = ["cdylib"]`), built by maturin with Linux targets cross-compiled in manylinux/musllinux containers on `ubuntu-22.04` ("No zig/CFLAGS ... every cross target builds with the default cross-gcc") and macOS/Windows on native runners; musl works there because Rust has no initial-exec TLS problem | blackthorn-interstellar/ultravin@dbc9777 `.github/workflows/release.yaml:142-182`, `crates/ultravin-py/Cargo.toml:12` |
