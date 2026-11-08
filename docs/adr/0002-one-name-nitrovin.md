# One name, nitrovin, for the repo, module, binary, packages and C library

The project was charted as `nhtsa-vin-decoder`. That name describes the project
well, but it is long to type, it puts a government agency's name on a project
the agency has nothing to do with, and it doesn't work as a C symbol prefix. The
plain name `vin` was free on every channel we ship (Homebrew, Scoop, winget,
ghcr.io, `go install`) and no major Linux distro installs a `vin` binary.
Even so, it can't be searched for (GitHub's `vin` results are visual-inertial
SLAM), it is taken on PyPI, npm and crates by other VIN and non-VIN projects,
and `vin_` is the prefix any VIN library would choose. We chose `nitrovin`. It
was the only candidate free on every package registry and as a GitHub user
name. It also follows ultravin's speed-plus-VIN pattern without reading as a
jab at it. A Go module path is the module's identity, so the repo was renamed
to `joshuasprow/nitrovin` before any Go code existed. Every other name follows
from it: `github.com/joshuasprow/nitrovin`, binary `cmd/nitrovin`, data module
`github.com/joshuasprow/nitrovin-data`, Homebrew `joshuasprow/tap/nitrovin`,
winget `JoshuaSprow.Nitrovin`, `ghcr.io/joshuasprow/nitrovin`,
`libnitrovin`/`nitrovin.h` with `nitrovin_*` symbols, and env prefix
`NITROVIN_`.

## Considered options

- `nhtsa-vin-decoder`: descriptive and already in place; long, uses the
  agency's name, and makes a poor binary name or C prefix.
- `vin` everywhere: shortest and free where it matters; can't be searched for,
  and a generic prefix in a shared C symbol namespace.
- A prefixed project with a `vin` binary (ripgrep/`rg`): two names to keep
  straight in docs, MCP configs and package manifests.
- `turbovin`, `hypervin`, `vindicator`: `hypervin` reads as one-upping
  ultravin; the others are taken somewhere `nitrovin` is not.

## Consequences

Go code lives under `pkg/` and never under `internal/`, with no package at the
repo root. Nothing is hidden from importers, so the v1 compatibility promise
covers every `pkg/*` package. A helper that isn't meant as API stays
unexported inside the package that uses it rather than getting a package of its
own. The data module follows the same rule (`nitrovin-data/pkg/artifact`).
