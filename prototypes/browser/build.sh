#!/usr/bin/env bash
# PROTOTYPE (#147): build the WebAssembly modules into prototypes/browser/build.
#
#   prototypes/browser/build.sh go       # build/go/{sw.wasm,core.wasm,wasm_exec.js}
#   prototypes/browser/build.sh tinygo   # build/tinygo/{core.wasm,wasm_exec.js}, plus sw.wasm (it builds, then panics: see #147)
#
# TinyGo comes from mise on demand (tinygo 0.44.0 and binaryen's wasm-opt
# version_133). wasm is 32-bit under TinyGo, and nitrovin-data's
# pkg/artifact (imported by pkg/contract) compares an int with
# math.MaxUint32, which does not compile there: the TinyGo build uses a
# patched copy of the module through a throwaway go.work.
set -euo pipefail
cd "$(dirname "$0")/../.."
root=$PWD
case "${1:-go}" in
go)
  out=prototypes/browser/build/go
  mkdir -p "$out"
  for m in sw core; do
    GOOS=js GOARCH=wasm go build -trimpath -ldflags='-s -w' -o "$out/$m.wasm" "./prototypes/browser/wasm/$m"
  done
  cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" "$out/"
  ;;
tinygo)
  out=prototypes/browser/build/tinygo
  mkdir -p "$out"
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  nd=$(go list -m -f '{{.Dir}}' github.com/joshuasprow/nitrovin-data)
  ndv=$(go list -m -f '{{.Version}}' github.com/joshuasprow/nitrovin-data)
  cp -R "$nd" "$tmp/nd"
  chmod -R u+w "$tmp/nd"
  perl -pi -e 's/if in\.size > math\.MaxUint32/if uint64(in.size) > math.MaxUint32/; s/if rows > math\.MaxUint32/if uint64(rows) > math.MaxUint32/' "$tmp/nd/pkg/artifact/encode.go"
  printf 'go 1.27\nuse %s\nreplace github.com/joshuasprow/nitrovin-data %s => %s/nd\n' "$root" "$ndv" "$tmp" >"$tmp/go.work"
  tg() { GOWORK="$tmp/go.work" mise x tinygo@0.44.0 github:WebAssembly/binaryen@version_133 -- tinygo "$@"; }
  for m in core sw; do
    tg build -target wasm -no-debug -o "$out/$m.wasm" "./prototypes/browser/wasm/$m"
  done
  cp "$(tg env TINYGOROOT)/targets/wasm_exec.js" "$out/"
  ;;
*)
  echo "usage: $0 go|tinygo" >&2
  exit 2
  ;;
esac
ls -l "$out"
