# A Go port of ultravin's engine, not a fork of it

ultravin (MIT, Rust) already reproduces NHTSA's `spVinDecode` field for field
(1,683 of 1,703 fields on a 131-VIN inventory corpus on 2026-11-07; the 20
misses were one lookup string NHTSA renamed between data months). Forking it
would have given this project a proven engine, its parity gates and its
monthly refresh on day one. We chose to port the engine to Go instead, because
the point of the project is to own the update and distribution pipeline end to
end, and the owner's language is Go: a single static binary, trivial cross-
compilation, a C-shared library for .NET callers, and no second toolchain to
keep. The cost is that parity has to be re-proven, so ultravin becomes the
reference implementation and the fast oracle rather than the code, and every
documented upstream defect it registers is adopted with its evidence rather
than rediscovered.

## Considered options

- Fork ultravin: fastest to a correct release; Rust toolchain and someone
  else's architecture for the life of the project.
- Port to TypeScript from the partial port in snydersalvage/agent-tools #41:
  runs in browsers and Workers, but no static binary and no C ABI.
- Port to Go: chosen.
