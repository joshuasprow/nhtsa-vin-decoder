# Go vanity import paths on nitrovin.app

Ticket: [#181](https://github.com/joshuasprow/nitrovin/issues/181), on the map
[#179](https://github.com/joshuasprow/nitrovin/issues/179). Date: 2026-11-10.
Go 1.27.3 on darwin/arm64. Everything was read-only: GETs against
proxy.golang.org, nitrovin.app and public vanity hosts, plus a throwaway
resolver under `/tmp` (§6).

**Answer.** The apex works. `nitrovin.app/nitrovin` and `nitrovin.app/data`
collide with no route the site serves. The Worker answers every `?go-get=1` GET
itself, before its cache and container branch, with one `go-import` meta tag
(`git https://github.com/marcy-cloud/<repo>`) for either prefix and any path
under it, and a 404 for anything else, including `/`. That takes about 20 lines
of TypeScript and changes no Terraform, cache rule, rate limit or container.
Renaming a module path starts a new module in the same repository: versions
carry on (`v0.3.0`, the next `v1.YYYYMM.n`), but old tags cannot be used under
the new path. Once the first new-path tag exists, `@latest` on the old path
fails with `module declares its path as`; `github.com/uber-go/zap` already
behaves this way through the real proxy. A final `// Deprecated:` release on
the old path does nothing in a same-repository rename (measured), so skip it.
An optional `retract` of the old tags in the new `go.mod` hides them from
listings. Users need no `GOPRIVATE` or `GONOSUMDB`. The new dependency to
accept: proxy.golang.org must reach nitrovin.app to fetch any new version, and
that includes the monthly refresh's data tag.

## 1. Facts from primary sources

### 1.1 What the go command asks a vanity host

| Fact | Source |
| --- | --- |
| For a module path without a VCS qualifier, the go command sends `GET https://<path>?go-get=1` (the `http://` fallback is used only with `GOINSECURE`). | https://go.dev/ref/mod#vcs-find |
| "The go command follows redirects but otherwise ignores response status codes, so the server may respond with a 404 or any other error status." | https://go.dev/ref/mod#vcs-find |
| The answer must be HTML with `<meta name="go-import" content="root-path vcs repo-url [subdirectory]">` in `<head>`, "early in the document … before any raw JavaScript or CSS". | https://go.dev/ref/mod#vcs-find |
| `root-path` "must be a prefix or an exact match of the requested module path. If it's not an exact match, another request is made for the prefix to verify the `<meta>` tags match." | https://go.dev/ref/mod#vcs-find |
| `repo-url` must use a secure scheme for git: `https`, `git+ssh` or `ssh`. GOVCS allows git for public and private modules by default. The optional fourth field (`subdirectory`) is recognized only by Go 1.25 and later; nitrovin doesn't need it, since both modules sit at their repository roots. | https://go.dev/ref/mod#vcs-find |
| Example: `golang.org/x/mod?go-get=1` → `<meta name="go-import" content="golang.org/x/mod git https://go.googlesource.com/mod">`. | https://go.dev/ref/mod#vcs-find |
| The VCS `mod` serves a module from a GOPROXY-protocol URL instead of a repository. nitrovin doesn't need it: GitHub stays the source. | https://go.dev/ref/mod#serving-from-proxy |

### 1.2 Deprecation and retraction

| Fact | Source |
| --- | --- |
| A module is deprecated by a `// Deprecated:` paragraph before the `module` directive in a new tagged release. | https://go.dev/ref/mod#go-mod-file-module-deprecation |
| "When the go command retrieves deprecation information for a module, it loads the go.mod file from the version matching the `@latest` version query … The go command loads the list of retracted versions from the same go.mod file." | https://go.dev/ref/mod#go-mod-file-module-deprecation |
| `retract` hides versions from the go command and pkg.go.dev; it takes effect when a newer version carrying it is published. | https://go.dev/ref/mod#go-mod-file-retract, https://proxy.golang.org/ FAQ |
| `go install pkg@version`: the module's `go.mod` "must not contain directives (replace and exclude)". nitrovin's `go.mod` has none (checked at `1091b73`). | https://go.dev/ref/mod#go-install |

### 1.3 proxy.golang.org, sum.golang.org, pkg.go.dev

| Fact | Source |
| --- | --- |
| "These services can only access publicly available source code. If you depend on private modules, set GOPRIVATE." A public vanity path therefore needs no setting. | https://proxy.golang.org/ (Environment setup) |
| New versions can lag: an explicit `go get module@version` makes one available "within one minute"; a version requested before its tag existed can take "up to 30 minutes" to expire from the cache. | https://proxy.golang.org/ FAQ |
| The mirror "tries to keep its cache up to date by refreshing recently accessed information before it expires", and it keeps cached versions after the origin disappears ("The same situation applies if you delete your entire repository"). | https://proxy.golang.org/ FAQ |
| Checksums stay in sum.golang.org even if a version leaves the mirror. | https://proxy.golang.org/ FAQ |
| pkg.go.dev reads `go-import` and `go-source` from `?go-get=1` for paths it does not recognize. "If [the repo URL] matches a known hosting site, use the URL templates corresponding to that site and ignore whatever's in the tag." So with a `github.com` repo URL, `go-source` is unnecessary. | `golang/pkgsite` `internal/source/source.go` (`moduleInfoDynamic`) and `meta-tags.go` (`fetchMeta`), master |

## 2. Serving the paths from the Worker

### 2.1 What gets requested (measured, §6)

`go install nitrovin.app/nitrovin/cmd/nitrovin@latest` in direct mode asked
the host for every prefix of the package path, the bare host included:

```
GET /nitrovin/cmd/nitrovin?go-get=1
GET /nitrovin/cmd?go-get=1
GET /nitrovin?go-get=1
GET /?go-get=1                     ← the host itself, as a candidate module path
GET /data?go-get=1                 ← the dependency
```

Through the default `GOPROXY`, proxy.golang.org sends these requests instead
of the user [INFERENCE: the proxy "just calls `go mod download`", golang/go#47241].
So the answer for any path under a module root must be that root's tag, and
every other path, `/` included, must carry no `go-import` tag.

### 2.2 Collisions

The container routes (`cmd/nitrovin/serve.go`, `route` and `ServeHTTP` at
`1091b73`) are `/`, `/copy.js`, `/healthz`, `/mcp`, `/ui` and `/ui/…`, and
`/v1/…`. `/nitrovin` and `/data` are free; today both return the container's
404 `application/problem+json` (`no route /data`, measured). `/?go-get=1` today
returns 400 `text/html` with `Cf-Cache-Status: BYPASS` and no `go-import` tag
(measured), so nothing goes wrong now. Still, `/` with a query is one of the
Worker's `cacheable` paths, so go traffic is better kept off the container
altogether.

The paths are permanent once published: the site can never serve `/nitrovin/…`
or `/data/…` itself. A new module must take a new top-level path segment.

### 2.3 The Worker change

Insert this at the top of `fetch`, before `nitrovin(env)` and the `cacheable`
branch:

```ts
// Go vanity import paths: the go command and proxy.golang.org ask
// https://nitrovin.app/<path>?go-get=1 for every prefix of a package path.
const MODULES: Record<string, string> = {
	"/nitrovin": "https://github.com/marcy-cloud/nitrovin",
	"/data": "https://github.com/marcy-cloud/nitrovin-data",
};

function goGet(url: URL): Response | undefined {
	if (url.searchParams.get("go-get") !== "1") return undefined;
	const root = Object.keys(MODULES).find((p) => url.pathname === p || url.pathname.startsWith(`${p}/`));
	if (!root) return new Response("not a Go module\n", { status: 404 });
	const meta = `<meta name="go-import" content="${url.host}${root} git ${MODULES[root]}">`;
	return new Response(`<!doctype html><html><head>${meta}</head><body></body></html>\n`, {
		headers: { "Content-Type": "text/html; charset=utf-8", "Cache-Control": "public, max-age=3600" },
	});
}
```

Notes:

- `url.host` keeps the root right under `wrangler dev`. In production it is
  `nitrovin.app`: `www` already 301s to the apex with path and query kept
  (`terraform/worker.tf`), and the go command follows redirects.
- No container, Cache API, Terraform or rate-limit change is needed. The rate
  limit covers only `/v1/decode…` and `/mcp` (`terraform/rules.tf`).
- `go-source` is optional (§1.3). `go.uber.org` sends one, `golang.org/x` does
  not (measured, §4).
- Optional: a browser GET of `/nitrovin` or `/data` without `go-get` could
  redirect to the repository or pkg.go.dev, as `go.uber.org/zap` does with a
  200 page. Nothing requires it.
- `deploy/cloudflare/worker/dev-test.sh` is where an assertion about the go-get
  answer would go.

### 2.4 Apex or `go.nitrovin.app`

`go.nitrovin.app/…` would need a second `cloudflare_workers_custom_domain`
(the same Worker or a separate one) and makes every import longer. Its one gain
is a namespace that never touches the site's paths. With no collision, and only
two reserved segments, the apex costs nothing extra. HSTS already includes
subdomains, so either works over HTTPS.

## 3. Renaming the modules

### 3.1 What happens (measured, §6; precedent, §4)

- **Versions continue in the same repository.** Module path is a property of
  each tag's `go.mod`. `nitrovin.app/nitrovin@v0.3.0` resolves once the tag's
  `go.mod` says `module nitrovin.app/nitrovin`, and the same holds for
  `nitrovin.app/data@v1.YYYYMM.n`. A tag can't be reused, so the first
  new-path version is the next one, not a re-tag.
- **Old tags are listed but unusable under the new path.**
  `go list -m -versions nitrovin.app/nitrovin` → `v0.2.0 v0.3.0`;
  `go install nitrovin.app/nitrovin/cmd/nitrovin@v0.2.0` →
  `module declares its path as: nitrovin.app/legacy but was required as: nitrovin.app/nitrovin`.
  Putting `retract [v0.1.0, <last old tag>]` in the new `go.mod` hides them:
  the listing became `v0.3.0` and `-retracted` showed all three (measured). The
  same works for the data module's `v1.202610.*`. This is cosmetic.
- **sum.golang.org and the proxy need nothing.** The first fetch of
  `nitrovin.app/data@<tag>` through the proxy records its hash like any public
  module. Hashes differ from the old path's, because the zip's file names
  include the module path. The data zip is 20,297,451 bytes today
  (`github.com/joshuasprow/nitrovin-data@v1.202610.2`, measured with a range
  GET), well under the 500 MiB limit.
- **The old path after the move.** GitHub redirects `joshuasprow/nitrovin` to
  `marcy-cloud/nitrovin`, so the old path reaches the same tags
  [INFERENCE: GitHub's documented transfer redirects, map #179]. Then:
  - `go install github.com/joshuasprow/nitrovin/cmd/nitrovin@latest` resolves to
    the first new-path tag and fails (`version constraints conflict … module
    declares its path as: nitrovin.app/nitrovin`).
  - `go get …@latest` fails the same way, and `go list -m -u` reports a bogus
    update to it.
  - Pinned old versions (`@v0.2.0`) keep installing, from the proxy's cache
    and from the origin.
  - This is the clean cutover the map chose.
- **A deprecation release on the old path does nothing.** With a
  `// Deprecated:` `v0.2.1` on the old path before `v0.3.0`,
  `go list -m -u -f '{{.Deprecated}}'` printed nothing on either version:
  deprecation is read from `@latest`'s `go.mod`, which is now the new path's
  (§1.2). It would work only while no new-path tag existed. A `retract` on the
  old path fails the same way. Skip both.

### 3.2 What changes in code

Found by searching both trees at `origin/main`:

- **nitrovin:**
  - `go.mod` (`module` and the data `require`), `go.sum`.
  - Imports in 66 `.go` files.
  - `pkg/nitrovin/nitrovin.go` `modulePath` and `pkg/ui/decode.go`
    `dataModule`. Both are compared against `debug.ReadBuildInfo()`, so a
    missed one silently turns the reported version into `(devel)` or loses the
    data version.
  - `refresh.yml` `DATA_MODULE` (its "Wait for proxy.golang.org" step polls
    `proxy.golang.org/$DATA_MODULE/@v/$TAG.info`), `scripts/refresh.sh`,
    `release.yml`, `import.yml`, `libnitrovin.yml`.
  - Docs, ADRs and `pkg/ui/decode.html` links: these name repository URLs, a
    separate change.
- **nitrovin-data:**
  - `go.mod`.
  - `pkg/embedded/embedded_test.go` import.
  - `scripts/build.sh` writes `importer.module` (`github.com/joshuasprow/nitrovin`)
    into `pkg/embedded/manifest.json`.
  - Doc comments in `pkg/artifact/doc.go` and `pkg/embedded/embedded.go`.
- **Pitfall:** `github.com/joshuasprow/nitrovin` is a prefix of
  `github.com/joshuasprow/nitrovin-data`. A blind replace turns the data path
  into `nitrovin.app/nitrovin-data`, so replace the data path first. Use
  `go mod edit -module` for `go.mod`.

### 3.3 Order the runbook (#185) must keep

1. **Worker first.** The go-get answer has to be live before any new-path tag
   is fetched. `deploy.yml` deploys a release tag (after `release.yml`, or
   `workflow_dispatch -f tag=`), so the Worker change ships in a release still
   on the old module path. Its `repo-url`s name `marcy-cloud`, so it is
   correct only after the transfer. Nobody asks for the vanity paths before
   step 2, so the order within the transfer is free.
2. **nitrovin-data on `nitrovin.app/data`:** tag the next `v1.YYYYMM.n`, then
   `curl https://proxy.golang.org/nitrovin.app/data/@v/<tag>.info` (the
   refresh's existing wait does this).
3. **nitrovin on `nitrovin.app/nitrovin`:** require that tag, then `go mod tidy`
   and tag the next `v0.x`. The workflows' `DATA_MODULE` changes in the same
   commit.
4. **Verify both ways:** `go install nitrovin.app/nitrovin/cmd/nitrovin@latest`
   with the default `GOPROXY` and with `GOPROXY=direct`.

## 4. Precedent (measured through the live services)

| Module | `?go-get=1` answer, or proxy behaviour |
| --- | --- |
| `go.uber.org/zap/zapcore` | `go-import` `go.uber.org/zap git https://github.com/uber-go/zap`, plus `go-source`: the root's tag for a subpackage path, served by Uber's vanity server. |
| `golang.org/x/mod/modfile` | `go-import` `golang.org/x/mod git https://go.googlesource.com/mod` only. |
| `github.com/uber-go/zap` (the path zap left) | proxy `@latest` = `v1.28.0`, whose `.mod` says `module go.uber.org/zap`; `go get github.com/uber-go/zap@latest` fails with `module declares its path as: go.uber.org/zap but was required as: github.com/uber-go/zap`. |
| `github.com/golang/lint` (moved to `golang.org/x/lint`) | `go install github.com/golang/lint/golint@latest` fails with the same `version constraints conflict`. |

## 5. Pitfalls

- **`GOPRIVATE`, `GONOSUMDB`, `GOINSECURE`:** none for users. Both repos and the
  host are public, and the answer is HTTPS (§1.3).
- **Caching:** the proxy caches versions, `list` and `@latest`
  (`Cache-Control: public, max-age=60` on `@v/list` and `@latest`, measured),
  with FAQ-documented lag of up to 30 minutes. How long the proxy itself keeps
  a `go-import` answer is not documented; it needs one only to fetch a version
  or a list it doesn't have. The Worker's `max-age` on its own answer only
  affects HTTP caches in between.
- **nitrovin.app down:**
  - **Default `GOPROXY`:** versions already in proxy.golang.org keep serving.
    New tags, and the refresh's "Wait for proxy.golang.org" step (15 minutes),
    fail until the Worker answers again [INFERENCE from §1.3].
  - **`GOPROXY=direct`, measured:** with a warm module cache,
    `go install nitrovin.app/nitrovin/cmd/nitrovin@v0.3.0` still failed
    (`loading deprecation for nitrovin.app/nitrovin: unrecognized import path`).
    `go install …@version` looks up `@latest` for the deprecation check.
  - The go-get answer lives in the Worker script, not the container, so a
    container rollout or crash doesn't affect it.
- **New coupling:** before the move, the monthly refresh needed only GitHub and
  the proxy. After it, the refresh also needs nitrovin.app's Worker.

## 6. The local experiment

All of it is under `/tmp/vanity-research/exp`, and nothing was published.

- **Repositories:** two bare git repositories served by `git daemon` on
  `127.0.0.1`.
  - `nitrovin`: `v0.2.0` with `module nitrovin.app/legacy`, standing in for
    `github.com/joshuasprow/nitrovin`; optionally a deprecated `v0.2.1`;
    `v0.3.0` with `module nitrovin.app/nitrovin`, requiring
    `nitrovin.app/data`, and in one run a `retract`.
  - `nitrovin-data`: `v1.202610.2` as `nitrovin.app/olddata`, `v1.202611.0` as
    `nitrovin.app/data`.
- **Resolver:** the §2.3 function, run under Bun as a plain-HTTP forward proxy
  (`HTTP_PROXY`) that logged every request. `HTTPS_PROXY` pointed at a closed
  port, so the go command fell back to `http://` under
  `GOINSECURE=nitrovin.app`. `GOPROXY=direct`, `GOSUMDB=off`, and a fresh
  `GOMODCACHE` for each run.
- **Results:** as quoted in §2.1, §3.1 and §5. A path outside the table
  (`nitrovin.app/olddata`) failed with
  `unrecognized import path … 404 Not Found`, and `/v1/info?go-get=1` got the
  404.

**Not measured:** proxy.golang.org and pkg.go.dev against a real
`nitrovin.app` answer. That needs the Worker change deployed. The check is the
§3.3 step 4 commands, plus `curl https://proxy.golang.org/nitrovin.app/data/@v/list`.
