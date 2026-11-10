# winget after the move: `JoshuaSprow.Nitrovin`, its open PRs, and who submits

Ticket: [#184](https://github.com/joshuasprow/nitrovin/issues/184), on
[Map: move nitrovin to the marcy-cloud org](https://github.com/joshuasprow/nitrovin/issues/179).
Date: 2026-11-10. Feeds the grilling tickets #185 (published identities) and
#186 (credentials).

Sources are pinned where they move: `microsoft/winget-pkgs` at `f5b3c5e`
(master, 2026-11-10), `microsoft/winget-cli` at `7de3b02`. Everything here was
read through public pages and read-only API calls; nothing was posted.

## Answer

1. **The move doesn't force a rename.** winget-pkgs has no rule tying a
   `PackageIdentifier` to the GitHub owner of the installer URLs, and there is
   direct precedent for keeping a person's identifier when the repository moves
   to an org: `dbrgn.tealdeer` switched its installer URLs from
   `github.com/dbrgn/tealdeer` to `github.com/tealdeer-rs/tealdeer` in
   [#301119](https://github.com/microsoft/winget-pkgs/pull/301119), kept
   `Publisher: Danilo Bargen` and a personal `PublisherUrl`, and merged with no
   `Validation-Domain` label (§2.3). `JoshuaSprow.Nitrovin` names Josh as the
   publisher, not the GitHub owner, so it already satisfies the map's
   "decoupled from the owner" rule: a later move would rename nothing either.
2. **A rename is free now and expensive later.** Nothing is merged, so a new
   identifier costs only a fresh New-Package PR (which both open PRs already
   are) and the queue position of the open ones. After a merge, a rename means
   the FAQ's two-copy migration, and for a portable package even that doesn't
   carry installs across (§2.4). So the identifier has to be settled before
   either PR merges. If it changes, `MarcyCloud.Nitrovin` would couple it to
   the owner again; a project-named ID like `Nitrovin.Nitrovin` follows the
   `Alacritty.Alacritty` shape.
3. **Open PRs:** keep the ID and they can go on as they are. Before the
   transfer their URLs are direct; after it, `github.com/joshuasprow/nitrovin`
   answers 301 to `marcy-cloud` (GitHub's rename redirect), and whether
   validation accepts that extra hop is unverified (gap 1). Rename the ID and
   both should be closed as superseded (the pattern Josh already used on
   #475969, #476452 and #476802), with the first release from `marcy-cloud`
   opening the New-Package PR under the new ID and `marcy-cloud` URLs.
4. **Submitting from an org fork or a machine account is allowed but gains
   nothing.** Org-owned forks are routine (two such PRs merged on 2026-11-10), but the PR
   author is still the user whose token opened it; that token still has to be
   a classic one, because fine-grained tokens can't contribute to public repos
   where the user isn't a member; maintainers can't push to an org fork's
   branch; and Microsoft's CLA is signed by the PR's author. A machine account
   would need its own fork, its own classic token and its own CLA signature
   (§2.5). Josh's fork and his `public_repo` token stay as they are, as the
   map already decided.

What changes in `.goreleaser.yaml` under either choice: `homepage`,
`publisher_support_url`, `license_url` and `release_notes_url` move to
`github.com/marcy-cloud/nitrovin`; the installer URLs follow on their own, since
GoReleaser takes them from the release. `repository.owner: joshuasprow`
(the fork) and `token` stay. `publisher`, `publisher_url` and `author` stay
Josh's if the ID stays; they would be rewritten with it otherwise.

## 1. State of the open PRs (read 2026-11-10, ~23:00 UTC)

| | [#476535](https://github.com/microsoft/winget-pkgs/pull/476535) (0.1.7) | [#476885](https://github.com/microsoft/winget-pkgs/pull/476885) (0.2.0) |
| --- | --- | --- |
| Head | `joshuasprow:JoshuaSprow.Nitrovin-0.1.7` | `joshuasprow:JoshuaSprow.Nitrovin-0.2.0` |
| Labels | `New-Package`, `Validation-Completed`, `Validation-Executable-Error`, `Needs-Author-Feedback`, `Needs-CLA` | `New-Package` only (validation not reported yet) |
| `license/cla` status | success | success |
| Commits | 3, author and committer `joshuasprow` | 3, author and committer `joshuasprow` |
| Last bot word | "require a moderator to approve" (22:42) | none |

- #476535's first validation hit a Defender race ("Another scan already in
  progress"); admin `stephengillie` removed `Validation-Defender-Error` and
  added `Validation-Completed`, but `Validation-Executable-Error` stayed. That
  label is **administrator-only**: "Community moderator approval alone cannot
  resolve or waive these labels"
  ([ValidationFailureGuide.md](https://github.com/microsoft/winget-pkgs/blob/master/doc/ValidationFailureGuide.md#validation-executable-error)).
- `Needs-CLA` is stale on #476535: the `license/cla` status is green. Moderators
  clear it with the trigger `[Policy] CLA Signed`
  ([Moderation.md](https://github.com/microsoft/winget-pkgs/blob/master/doc/Moderation.md#marking-cla-as-signed)).
- Both manifests (on Josh's fork) are a `zip` with a `portable` nested
  `nitrovin.exe`, `PortableCommandAlias: nitrovin`, `Moniker: nitrovin`,
  `Publisher: Joshua Sprow`, `PublisherUrl: https://github.com/joshuasprow`,
  and every other URL on `github.com/joshuasprow/nitrovin`.
- No `JoshuaSprow`, `Nitrovin`, `MarcyCloud` or `marcy-cloud` publisher
  directory exists under `manifests/` yet, and no other PR mentions nitrovin.
  `marcy-cloud` has no `winget-pkgs` fork.

## 2. Facts from primary sources

### 2.1 What an identifier is

| Fact | Source |
| --- | --- |
| "Each package is uniquely identified by a 'PackageIdentifier,' typically in the format `Publisher.Package`." The directory is `manifests/<lowercase first letter>/<segment>/<segment>/<version>`, case-sensitive. | [doc/Authoring.md](https://github.com/microsoft/winget-pkgs/blob/master/doc/Authoring.md#what-is-a-package) |
| Validation fails when "the directory path and file names" don't "exactly match the `PackageIdentifier` and `PackageVersion`". | [doc/Validation.md](https://github.com/microsoft/winget-pkgs/blob/master/doc/Validation.md) step 02 |
| One PR = "one multi-file manifest set for a single `PackageIdentifier` and `PackageVersion`". | [doc/FirstContribution.md](https://github.com/microsoft/winget-pkgs/blob/master/doc/FirstContribution.md) |
| winget has no identifier redirect: "Support redirecting manifests" (a `Redirect.yaml` for when "a package id gets changed") is still an open proposal. | [winget-cli#1899](https://github.com/microsoft/winget-cli/issues/1899) |
| Project-named identifiers exist with the project as publisher: `Alacritty.Alacritty`, `Publisher: Alacritty`, URLs on `github.com/alacritty/alacritty`. | `manifests/a/Alacritty/Alacritty/0.17.0` |

### 2.2 What a rename costs in review

| Fact | Source |
| --- | --- |
| New identifiers go to the slowest queue: "New packages generally require more scrutiny because moderators must establish package identity, installer provenance, metadata accuracy, and policy compliance without prior manifest history." New versions of an established package "may require less scrutiny". | [doc/Moderation.md](https://github.com/microsoft/winget-pkgs/blob/master/doc/Moderation.md#before-you-request-assistance) |
| `Policy-*`, `Validation-Domain` and `Validation-Executable-Error` need an administrator; "Mentioning a community moderator will not advance an administrator-only review." | same, "GitHub Mentions" |
| The documented move for a package changing publisher: "two copies of the package must be added … one under the original package identifier and one under a new package identifier", the old copy carrying `AppsAndFeaturesEntries` with the old `Publisher` so installed copies upgrade once and then correlate to the new ID. | [doc/FAQ.md](https://github.com/microsoft/winget-pkgs/blob/master/doc/FAQ.md#what-should-i-do-if-a-package-is-being-published-by-a-new-publisher) |
| Removing the old ID's versions afterwards trips `Last-Version-Removal` / `Highest-Version-Removal`, whose bot reply asks for "the reason for removal". | `.github/policies/labelAdded.lastVersionRemoval.yml`, `labelAdded.highestVersionRemoval.yml` |
| Precedent, after merge: when jq moved from `stedolan` to `jqlang`, the same contributor added `jqlang.jq` 1.6 ([#108751](https://github.com/microsoft/winget-pkgs/pull/108751), "jq home was moved", flagged `Possible-Duplicate` for the identical SHA256) and deleted `stedolan.jq` 1.6 ([#108752](https://github.com/microsoft/winget-pkgs/pull/108752), "Moving: stedolan.jq 1.6"); both merged the same day. | the two PRs |
| Precedent, before merge: `LinusDierheimer.fastfetch`'s New-Package PR ([#94697](https://github.com/microsoft/winget-pkgs/pull/94697)) was closed unmerged; the package landed later as `Fastfetch-cli.Fastfetch` in a fresh New-Package PR ([#154718](https://github.com/microsoft/winget-pkgs/pull/154718)). | the two PRs |

### 2.3 Keeping the identifier when the URLs change owner

| Fact | Source |
| --- | --- |
| URL domain validation checks "whether the domain of the `InstallerUrl` is an approved, official source for the publisher"; failure labels are `Validation-Domain`, `Validation-Unapproved-URL`, `Validation-Indirect-URL`. "If a redirect is used, the final destination URL is from an approved domain." Common failure: "Using a redirect URL rather than the final resolved URL." | [doc/Validation.md](https://github.com/microsoft/winget-pkgs/blob/master/doc/Validation.md#04-url-domain-validation) |
| Policy: installer URLs "need to be discoverable on the publishers website". | [doc/Policies.md](https://github.com/microsoft/winget-pkgs/blob/master/doc/Policies.md#manifest-urls) |
| **Precedent:** `dbrgn.tealdeer` 1.7.2 used `github.com/dbrgn/tealdeer/...`; 1.8.0 ([#301119](https://github.com/microsoft/winget-pkgs/pull/301119), 2025-10-04) switched to `github.com/tealdeer-rs/tealdeer/...` and `PackageUrl` to the org, kept `Publisher: Danilo Bargen` and `PublisherUrl: https://bargen.dev`. Labels over its life: `New-Manifest`, `Azure-Pipeline-Passed`, `Validation-Completed`, `Moderator-Approved` (by admin `stephengillie`), `Publish-Pipeline-Succeeded`; no domain label was ever added. Three later versions followed on the same terms. | PR events and `manifests/d/dbrgn/tealdeer` |
| Other identifiers that outlived their repo's move to an org: `zyedidia.micro` (repo now `micro-editor/micro`; 2.0.15 merged 2026-01-10 with URLs still on `zyedidia/micro`, which now answer 301) and `StephanDilly.gitui` (repo now `gitui-org/gitui`; 0.28.1 URLs on `extrawurst/gitui`). When those repos moved relative to those PRs wasn't established, so they show tolerance of a kept ID, not of a redirect. | `manifests/z/zyedidia/micro`, `manifests/s/StephanDilly/gitui`, `curl -I` |
| GitHub release-asset URLs already redirect: `github.com/joshuasprow/nitrovin/releases/download/v0.2.0/…` answers 302 to `release-assets.githubusercontent.com`. After a transfer there is one more hop (301 to the new owner) in front of it. | `curl -I`, 2026-11-10 |

### 2.4 Installed copies under a new identifier

Moot today: nothing is merged, so no one has installed nitrovin through winget.
It matters only if the identifier changed after a merge.

| Fact | Source |
| --- | --- |
| A portable install's Apps & Features product code is `MakeSuitablePathPart(packageId + "_" + source)`, and its ARP entry records `WinGetPackageIdentifier` and `WinGetSourceIdentifier`. | `src/AppInstallerCLICore/Workflows/PortableFlow.cpp` (`GetPortableProductCode`, `VerifyPackageAndSourceMatch`) |
| "The Product Code will be populated with the 'PackageIdentifier' if it is not specified"; each package gets its own directory named after the identifier, and the command alias is a symlink in `WinGet\Links`. | winget-cli spec [#182 portable apps](https://github.com/microsoft/winget-cli/blob/master/doc/specs/%23182%20-%20Support%20for%20installation%20of%20portable%20standalone%20apps.md) |
| Installing a second package whose alias is already linked overwrites the link with the warning "Overwriting existing file: {0}". | `src/AppInstallerCLICore/PortableInstaller.cpp`; string `OverwritingExistingFileAtMessage` in `winget.resw` |

[INFERENCE] So a renamed portable package never upgrades the old install:
`winget upgrade` keeps tracking the old ID, `winget install <new ID>` adds a
second copy and takes over the `nitrovin` alias, and the old ARP entry stays
until uninstalled. The FAQ's two-copy recipe is written for installers whose
ARP entries carry a publisher and product code of their own; it doesn't obviously
reach a portable whose product code is the old identifier.

### 2.5 Who can submit, and with what

| Fact | Source |
| --- | --- |
| Org-owned forks are in routine use: [#476774](https://github.com/microsoft/winget-pkgs/pull/476774) (fork `co2-lab`, author `adrielcodeco`) and [#476614](https://github.com/microsoft/winget-pkgs/pull/476614) (fork `tremor4986`, author `alexkoala`) merged on 2026-11-10. Both show `maintainerCanModify: false`. | GraphQL search of the last 100 merged PRs |
| "Allow edits from maintainers" is offered "on user-owned forks" only. Josh's PRs currently have `maintainer_can_modify: true`. | [GitHub Docs](https://docs.github.com/en/pull-requests/collaborating-with-pull-requests/working-with-forks/allowing-changes-to-a-pull-request-branch-created-from-a-fork); PR API |
| GoReleaser pushes to `repository.owner/name` (any owner) and opens the PR against `pull_request.base`; it first "Tr[ies] to sync the … fork". `pull_request.token` (v2.18) may differ from `repository.token`. | GoReleaser docs, [includes/prs.md](https://github.com/goreleaser/goreleaser/blob/main/www/content/includes/prs.md), [includes/repository.md](https://github.com/goreleaser/goreleaser/blob/main/www/content/includes/repository.md) |
| Fine-grained tokens can't be used "to contribute to public repos where the user is not a member", so a token that opens PRs on `microsoft/winget-pkgs` is a classic one. | [GitHub Docs, PAT limitations](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens#fine-grained-personal-access-tokens-limitations) |
| A classic token "will grant access to all repositories within the organizations that you have access to". An org can "Restrict access via personal access tokens (classic)", which blocks them from org-owned resources. | same page; [Setting a PAT policy for your organization](https://docs.github.com/en/organizations/managing-programmatic-access-to-your-organization/setting-a-personal-access-token-policy-for-your-organization) |
| WinGet Releaser, the common Actions route, also requires "a _classic_ Personal Access Token (PAT) with the `public_repo` and `workflow` scopes" (`workflow` for syncing upstream workflow files into the fork); it defaults to a fork under the project's own account or org, with `fork-user` for a bot account. | [vedantmgoyal9/winget-releaser README](https://github.com/vedantmgoyal9/winget-releaser) |
| Machine accounts are allowed: "set up by an individual human who accepts the Terms on behalf of the Account … used exclusively for performing automated tasks"; at most one free machine account besides your free personal account. | [GitHub Terms of Service](https://docs.github.com/en/site-policy/github-terms/github-terms-of-service), "Account Requirements" |
| The CLA bot "will confirm whether or not the user who opened the PR has already signed"; it is signed with `@microsoft-github-policy-service agree`, or `… agree company="your company"` "on behalf of a company". | [microsoft/ContributorLicenseAgreement README](https://github.com/microsoft/ContributorLicenseAgreement#commands) |
| The org's `bypassUsers` list (e.g. `wingetbot`, `dotnet-winget-bot`) is Microsoft-curated; pre-approved bots go through Microsoft's `cla-approved-bots` repo. | [microsoft/.github policies/cla.yml](https://github.com/microsoft/.github/blob/main/policies/cla.yml) |
| CLA §4: if a submission is made for an employer, or "anyone else for whom You are acting", "You must secure permission from Your employer … the term 'You' … will refer to You and the employer collectively". | [microsoft/.github CLA/microsoft.yml](https://github.com/microsoft/.github/blob/main/CLA/microsoft.yml) |
| "You will only need to do this once across all Microsoft repositories using our CLA." | [winget-pkgs README](https://github.com/microsoft/winget-pkgs#contributing) |

What that means for each submitter:

| Submitter | Fork | Token | CLA | Notes |
| --- | --- | --- | --- | --- |
| **Josh, today** | `joshuasprow/winget-pkgs` | Josh's classic `public_repo` token | Josh's, already signed (`license/cla` green) | Moderators can push fixes to the PR branch. Recommended to keep. |
| Josh, org fork | `marcy-cloud/winget-pkgs` | still Josh's classic token, which also needs to reach `marcy-cloud` | Josh's | PRs still authored by Josh; no maintainer edits; breaks if `marcy-cloud` restricts classic tokens. Gains nothing. |
| Machine account | its own fork (or the org's) | the machine account's classic `public_repo` token (+ `workflow` to sync the fork) | the machine account signs, individually or `company="…"` | Set `commit_author` to the machine account so the commits link to it, as the current comment in `.goreleaser.yaml` explains for Josh. The token is still a person-owned credential, held by the account's human owner. |

## 3. Options for #185

| | Keep `JoshuaSprow.Nitrovin` | Rename to `Nitrovin.Nitrovin` (or similar) before any merge |
| --- | --- | --- |
| Coupled to the GitHub owner? | No: names the publisher. A later move changes URLs only. | No. |
| Coupled to Josh? | Yes, as the `Publisher`. Accurate while he publishes it. | No; `Publisher` would be the project. |
| Precedent | `dbrgn.tealdeer` (§2.3) | `Alacritty.Alacritty` shape; fastfetch pre-merge rename (§2.2) |
| Review cost | Open PRs continue; later versions are New-Manifest (lighter queue). | Close both PRs; one fresh New-Package review, as now. |
| Code changes | four URLs | four URLs, plus `package_identifier`, `publisher`, `publisher_url`, `author`, `commit_msg_template`, `branch` |
| Reversible later? | Only via the two-copy migration, which doesn't move portable installs. | Same, once merged. |

Recommendation: keep `JoshuaSprow.Nitrovin`. It meets the decoupling rule
as it stands and is the only option with no review cost. Rename only if #185
wants the winget name to stop naming Josh, and do it before either PR merges.

## 4. Gaps

1. **Does validation accept installer URLs that 301 through a moved GitHub
   repo?** The documented rule on redirects (§2.3) is ambiguous, and no
   precedent fixed the timing. It matters only if #476535/#476885 are still
   open, or get re-validated, after the transfer. Closes when: after the
   transfer, the labels of the next validation run on either PR are read
   (`Validation-Indirect-URL`/`Validation-Domain` or not). Avoided entirely if
   both have merged or been closed by then.
2. **Does `marcy-cloud` restrict classic tokens?** Only matters for the org-fork
   option. Closes when: an org owner reads Settings → Personal access tokens →
   Tokens (classic) on `marcy-cloud` (read-only view).
3. **How moderators treat closing New-Package PRs for a new ID.** Nothing is
   documented beyond the fastfetch precedent. Closes when: the replacement PR
   is opened and its first moderator comment is read.
