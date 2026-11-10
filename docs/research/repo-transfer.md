# Research: transferring the nitrovin repos to marcy-cloud

Ticket: [#182](https://github.com/joshuasprow/nitrovin/issues/182) on [Map #179](https://github.com/joshuasprow/nitrovin/issues/179). Researched 2026-11-10, read-only.

Sources: GitHub Docs [Transferring a repository][transfer] (source: [github/docs][transfer-src]), [About permissions for GitHub Packages — About repository transfers][pkg-perm], [Configuring a package's access control and visibility][pkg-access], and read-only `gh api` calls against `marcy-cloud` and the four repos. Claims marked `[INFERENCE]` are reasoned, not documented or observed.

## What moves with a repo

From [Transferring a repository][transfer]:

- **Moves:** issues, pull requests, wiki, stars, watchers, releases, projects, settings; git history and contributions.
- **Stay associated:** webhooks, services, **secrets** and **deploy keys**.
- **Issue assignees** (user → org): kept only for org members; others are cleared. Josh is an org member, so his assignments survive.
- The original owner is added as a collaborator; the org's default repository permission and member privileges then apply (`marcy-cloud` default repo permission: `write`, observed).
- **Packages:** "may be transferred, or may lose their link to the repository, depending on the registry" (see ghcr below).
- Not listed on the page, so `[INFERENCE]`: labels, milestones, release assets, Actions variables, environments, repository rulesets and Actions run history move as repository data/settings; sub-issue and blocked-by links are issue-to-issue and survive because the issues move with their numbers intact. Check each after the move.

Observed on the four repos today: no webhooks, no wiki in use, 0 stars/watchers, no Pages. One environment (`nitrovin.app`, no protection rules) and one ruleset (`main`: `deletion`, `non_fast_forward`, no bypass actors) on `nitrovin`. One read-write deploy key per repo. GitHub App installations are per account: the repo is not covered by `marcy-cloud`'s installations unless that installation is set to all repos or the repo is added (`marcy-cloud` has sonarqubecloud, cursor, posthog installed; nitrovin needs none).

## Redirects

- "All links to the previous repository location are automatically redirected"; `git clone/fetch/push` redirect too, but GitHub recommends `git remote set-url`. [transfer]
- **Redirects end** if a new repo or fork is created at the old `joshuasprow/<name>`: "the redirects ... will be permanently deleted." [transfer]
- Name retirement: a repo with >100 clones or Actions uses in the prior week has `OWNER/NAME` permanently retired. [transfer] It does not affect redirects.
- Pages are not redirected (none here).
- `[INFERENCE]` web, API (`api.github.com/repos/...` returns 301), release download and `raw.githubusercontent.com` URLs follow the same repo redirect; not documented per host. Close with `curl -sI` against each after the move.
- Go: `[INFERENCE]` the module proxy caches by path; old `github.com/joshuasprow/...` versions stay fetchable from `proxy.golang.org`, and a fresh fetch of the old path through a redirect fails the go.mod path check for versions declaring a new path. The vanity-path decision makes this moot.

## ghcr packages

- ghcr (Container registry) has granular permissions, so packages are **scoped to the account, and the account does not change when the repo moves**. The repo link is removed, and "Actions workflows associated with the repository will lose access to the package." [pkg-perm]
- So `ghcr.io/joshuasprow/nitrovin` stays under `joshuasprow` with all tags and its cosign `.sig`/attestation tags intact; `marcy-cloud/nitrovin`'s `GITHUB_TOKEN` cannot push to it. GitHub Docs describe no way to transfer a container package between owners; the image is recreated by the first release from the org (matches the clean-cutover decision).
- **Pushing to `ghcr.io/marcy-cloud/nitrovin`:** a package first published with the `org.opencontainers.image.source` label pointing at the repo is linked and **inherits the repo's access**, so the workflow's `GITHUB_TOKEN` with `packages: write` can publish it — unless the org disabled automatic inheritance for new packages. [pkg-access] The org must also allow members/Actions to create public packages for the image to be public (org Packages settings).
- **Gap:** the CLI token lacks `read:packages`. Close with `gh auth refresh -h github.com -s read:packages` then `gh api 'user/packages?package_type=container'` and `gh api 'orgs/marcy-cloud/packages?package_type=container'`; package-creation visibility and inheritance are UI-only: `https://github.com/organizations/marcy-cloud/settings/packages`.

## Org settings that could block workflows

Observed (`gh api orgs/marcy-cloud`): plan `team`, 2FA requirement **off**, members can create public repos, web commit signoff not required. Actions minutes for public repos are free; the org's billing usage shows Linux minutes fully discounted.

Not readable with the current token (403/404 "needs admin:org"): allowed actions, default `GITHUB_TOKEN` permissions, fork-PR approval, org rulesets, org secrets/variables. `nitrovin` today runs with repo default `read` and `can_approve_pull_request_reviews: true` (the refresh opens and merges PRs).

Risks to check:

- **Allowed actions:** if limited to GitHub-owned/verified, third-party actions in the workflows (goreleaser, sigstore, etc.) fail.
- **Default workflow permissions / "allow Actions to create and approve PRs":** an org-level off overrides the repo setting the refresh depends on.
- **Org rulesets** targeting all repos could block the refresh's push or merge.
- **Deploy keys:** org owners can disable deploy keys org-wide, which breaks every cross-repo push below.

Close with `gh auth refresh -h github.com -s admin:org`, then read-only: `gh api orgs/marcy-cloud/actions/permissions`, `.../actions/permissions/workflow`, `.../actions/permissions/selected-actions`, `.../actions/permissions/fork-pr-contributor-approval`, `gh api orgs/marcy-cloud/rulesets`, and the deploy-key policy at `https://github.com/organizations/marcy-cloud/settings/deploy_keys` (UI).

## Cosign keyless identity

- Fulcio puts the workflow ref in the certificate SAN: today `https://github.com/joshuasprow/nitrovin/.github/workflows/release.yml@refs/tags/<tag>`, issuer `https://token.actions.githubusercontent.com` (`.goreleaser.yaml` release notes, `release.yml`). After the move it becomes `https://github.com/marcy-cloud/nitrovin/.github/workflows/release.yml@refs/tags/<tag>`, same issuer `[INFERENCE: from GitHub's OIDC `job_workflow_ref` claim]`.
- Existing bundles and image signatures are immutable and logged in Rekor, so pre-move releases stay verifiable **with the old identity string**; the old image signatures stay with the image under `joshuasprow`.
- `deploy.yml` builds the identity from `${{ github.repository }}`, so it follows the owner automatically; `release.yml`'s `IMAGE`, `.goreleaser.yaml` and README hard-code `joshuasprow` and need the edit.

## Cross-repo writes (deploy keys)

- Deploy keys stay attached to their repo through the transfer [transfer]; the private halves are repo secrets in `nitrovin`, which also stay. `[INFERENCE]` So the keys keep working once both sides move, provided the org allows deploy keys.
- The push URLs are hard-coded `git@github.com:joshuasprow/homebrew-tap.git` / `scoop-bucket.git` (`.goreleaser.yaml`) and `DATA_REPO` in `refresh.yml`/`scripts/refresh.sh`. `[INFERENCE]` git-over-SSH redirects should carry them during the gap, but update them in the same cutover. `refresh.yml`'s own pushes use `$GITHUB_REPOSITORY` and follow automatically.

## Gaps

| Gap | Step that closes it |
|---|---|
| Org Actions policy, workflow permissions, org rulesets | `gh auth refresh -s admin:org`, then the read-only `gh api` calls above |
| Org deploy-key and package-creation policy | Read the org settings pages above |
| Current ghcr package state | `gh auth refresh -s read:packages`, `gh api 'user/packages?package_type=container'` |
| Per-host redirect behavior (API, release assets, raw) | `curl -sI` each old URL after the transfer |
| Labels/milestones/variables/environments/rulesets intact | Compare `gh api` listings before and after the transfer |

[transfer]: https://docs.github.com/en/repositories/creating-and-managing-repositories/transferring-a-repository
[transfer-src]: https://github.com/github/docs/blob/main/content/repositories/creating-and-managing-repositories/transferring-a-repository.md
[pkg-perm]: https://docs.github.com/en/packages/learn-github-packages/about-permissions-for-github-packages#about-repository-transfers
[pkg-access]: https://docs.github.com/en/packages/learn-github-packages/configuring-a-packages-access-control-and-visibility
