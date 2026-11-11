# Published identities name the domain where they can, the owner where they must

nitrovin, nitrovin-data, the Homebrew tap and the Scoop bucket move from
`joshuasprow` to the `marcy-cloud` organization
([Published identities after the move](https://github.com/joshuasprow/nitrovin/issues/185),
on [the migration map](https://github.com/joshuasprow/nitrovin/issues/179)).
Every identity a user or tool holds onto names its owner somewhere, and a move
renames whatever names the GitHub owner. So each identity names the domain
nitrovin already runs, `nitrovin.app`, wherever the system that issues it
allows that, and follows the owner only where it doesn't. The next move then
renames nothing a user typed.

| Identity | Before | After | Why |
| --- | --- | --- | --- |
| Go modules | `github.com/joshuasprow/nitrovin`, `github.com/joshuasprow/nitrovin-data` | `nitrovin.app/nitrovin`, `nitrovin.app/data` | Vanity paths: the Worker answers `?go-get=1` under `/nitrovin` and `/data` with `go-import` tags pointing at the repos; `/` and the site's routes are untouched. |
| MCP Registry name | `io.github.joshuasprow/nitrovin` | `app.nitrovin/nitrovin` | Domain namespace, authenticated by an Ed25519 key in a DNS TXT record on `nitrovin.app`; the private key is a release-only environment secret. |
| Container image | `ghcr.io/joshuasprow/nitrovin` | `ghcr.io/marcy-cloud/nitrovin` | ghcr packages belong to the account and can't be transferred. The first release from the org creates it, linked by its `org.opencontainers.image.source` label, public. |
| Signing identity | `joshuasprow/nitrovin`'s `release.yml` | `marcy-cloud/nitrovin`'s `release.yml` | Fulcio certifies the workflow that signs; it follows the repo. |
| Homebrew, Scoop | `joshuasprow/tap`, `joshuasprow/scoop-bucket` | `marcy-cloud/tap`, `marcy-cloud/scoop-bucket` | The repos move with their names. |
| Winget | `JoshuaSprow.Nitrovin` | unchanged | It names the publisher, not the repo owner. Only its homepage, support, licence and release-notes URLs move. |

Nothing old is kept alive (the map's clean cutover; nitrovin's only consumer
is its author). Releases published before the move stay where they are and
stay verifiable: their tags, archives and image under `joshuasprow`, signed by
the old identity, which the README and docs quote once for them. The old MCP
entry is marked deprecated, its message naming the new one; the registry has
no rename.

The first release under the new paths is the next minor (v0.4.0 unless
another ships first). nitrovin-data continues `v1.YYYYMM.n` under
`nitrovin.app/data`. Old-path tags are not retracted: under the new path they
already fail the module path check. The old path's `go install …@latest`
breaks once the first new-path tag exists, and a `// Deprecated:` release
there would never be read, so none is made.

## Considered options

- Everything follows the owner (`github.com/marcy-cloud/…`,
  `io.github.marcy-cloud/nitrovin`): no DNS key and no Worker route, and
  every identity renamed again on the next move.
- The root module path `nitrovin.app`: a shorter `go install`, but the Worker
  would answer `?go-get=1` on every path, `/` included, and the module's
  package paths would share the site's namespace. Untested.
- Mirroring the image to the old name, or a forwarding tap: kept-alive old
  channels the clean cutover rules out.
- Renaming the winget package now, before either open PR merges: cheap only
  today, and it buys an identifier that doesn't name a person at the cost of
  a fresh review.

## Consequences

- proxy.golang.org must reach nitrovin.app to fetch any new version,
  the monthly data tag included; versions it already holds keep serving.
  nitrovin.app's availability is now part of `go install`.
- The order is fixed by the paths: the Worker's go-get answer ships and
  deploys first, then nitrovin-data is tagged on `nitrovin.app/data`, then
  nitrovin is renamed, requires that tag and is tagged.
- `release.yml` swaps `mcp-publisher login github-oidc` for `login dns`; the
  image's `io.modelcontextprotocol.server.name` label,
  `scripts/mcp-server-json.sh` and the release check change together.
- After the move, `deploy.yml` can't redeploy a release made before it (the
  new image has none of its tags); rolling back past the move is
  `wrangler rollback` or a new release.
