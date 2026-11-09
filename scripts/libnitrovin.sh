#!/usr/bin/env bash
# Build libnitrovin, the C library (cmd/libnitrovin), for every shipped
# target with zig cc as the one C cross-compiler, from any host
# (docs/research/c-shared-matrix.md on research/c-shared-matrix).
# CI (.github/workflows/libnitrovin.yml) and a workstation run the same command.
#
#   scripts/libnitrovin.sh <dir> [<goos>/<goarch>...]
#
# Writes <dir>/<goos>-<goarch>/libnitrovin.{so,dylib,dll} and nitrovin.h, the
# header cgo generates for the library, for the given targets, or all six:
# linux, darwin and windows on amd64 and arm64. musl is not a target: a Go
# c-shared library cannot be dlopen-ed under musl (golang/go#54805).
#
# Pinned floors: glibc 2.17 (an accidental newer libc call cannot raise it
# silently) and macOS 11.0 (minos; zig signs the dylib ad hoc, which macOS
# arm64 requires). Windows links the UCRT. The library carries no data; the
# version it reports is Go's VCS stamp of the checkout (v0.1.0 at that tag,
# as the release builds it), or "(devel)" under GOFLAGS=-buildvcs=false.
set -euo pipefail

dir="${1:?usage: scripts/libnitrovin.sh <dir> [<goos>/<goarch>...]}"
shift
targets=("$@")
[ ${#targets[@]} -gt 0 ] || targets=(linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64)

# Go's os package links -lresolv on darwin (internal/syscall/unix), for three
# DNS functions this library never calls, and zig ships libSystem's stub but
# not libresolv's. A text stub (.tbd: names only, no Apple code) lets the
# link record /usr/lib/libresolv.9.dylib, which every macOS has.
stubs="$(mktemp -d)"
trap 'rm -rf "$stubs"' EXIT
cat >"$stubs/libresolv.tbd" <<'EOF'
--- !tapi-tbd
tbd-version:     4
targets:         [ x86_64-macos, arm64-macos, arm64e-macos ]
install-name:    '/usr/lib/libresolv.9.dylib'
current-version: 1
exports:
  - targets:         [ x86_64-macos, arm64-macos, arm64e-macos ]
    symbols:         [ _res_9_nclose, _res_9_ninit, _res_9_nsearch ]
...
EOF

for target in "${targets[@]}"; do
  goos="${target%/*}" goarch="${target#*/}"
  case "$goarch" in
    amd64) arch=x86_64 ;;
    arm64) arch=aarch64 ;;
    *) echo "libnitrovin.sh: no such target $target" >&2; exit 2 ;;
  esac
  case "$goos" in
    linux) triple="$arch-linux-gnu.2.17" ext=so ldflags="-Wl,-soname,libnitrovin.so" ;;
    darwin) triple="$arch-macos.11.0" ext=dylib ldflags="-L$stubs -Wl,-install_name,@rpath/libnitrovin.dylib" ;;
    windows) triple="$arch-windows-gnu" ext=dll ldflags= ;;
    *) echo "libnitrovin.sh: no such target $target" >&2; exit 2 ;;
  esac
  out="$dir/$goos-$goarch"
  mkdir -p "$out"
  start=$SECONDS
  CGO_ENABLED=1 GOOS="$goos" GOARCH="$goarch" CC="zig cc -target $triple" CGO_LDFLAGS="$ldflags" \
    go build -trimpath -ldflags='-s -w' -buildmode=c-shared \
    -o "$out/libnitrovin.$ext" ./cmd/libnitrovin
  mv "$out/libnitrovin.h" "$out/nitrovin.h"
  echo "$out/libnitrovin.$ext: $(wc -c <"$out/libnitrovin.$ext" | tr -d ' ') bytes, $((SECONDS - start)) s ($triple)"
done
