# nitrovin.app on Cloudflare: who owns it, and what moving it would take

Research for [#183](https://github.com/joshuasprow/nitrovin/issues/183) on [Map: move nitrovin to the marcy-cloud org](https://github.com/joshuasprow/nitrovin/issues/179). Read 2026-11-10 against `main` at `1091b73`, the repository's secrets and variables (names only), Cloudflare's API (read-only `GET`s with the credentials already issued for this deploy), public DNS and RDAP, and Cloudflare's and GitHub's docs. Nothing was changed. Account ids, emails and the Cloudflare login are left out on purpose; "the nitrovin account" below means the account `CLOUDFLARE_ACCOUNT_ID` names.

## Answer

- **One account holds all of it: Josh's personal Cloudflare account, the one `CLOUDFLARE_ACCOUNT_ID` names.** That account holds:
  - the zone `nitrovin.app`: `active`, Free Website plan, full setup, created 2026-11-10 14:28 UTC by the registration;
  - the R2 bucket `nitrovin-tfstate`, the only bucket in the account;
  - the three account-owned tokens: the CI token and the R2 state token that `terraform/token/` made (the names in `token/main.tf`), plus the short-lived bootstrap token from the last `token/` apply, still active until it expires on 2026-11-18 (the README says to delete it; expiry does the same a week later);
  - the Worker `nitrovin`, its Durable Object namespace and the container application `nitrovin-nitrovin` [INFERENCE: the read-only credentials can't list Workers, but `wrangler deploy` and `rollout-wait.sh nitrovin-nitrovin` succeed in `deploy.yml` with `CLOUDFLARE_ACCOUNT_ID`, e.g. run [42907841554](https://github.com/joshuasprow/nitrovin/actions/runs/42907841554)].

  The account is an old (2018), standard, personal account with two-factor enforced for its members. The README's bootstrap says the same ("Josh's own; not the company's", `deploy/cloudflare/README.md`).
- **The domain is on Cloudflare Registrar in that same account.** RDAP for `nitrovin.app` shows registrar CloudFlare, Inc. (IANA 1910), registered 2026-11-10, expiring 2027-11-10, statuses `add period` and `client transfer prohibited`, nameservers `aaden`/`alla.ns.cloudflare.com`, and no DS record (DNSSEC off, `delegationSigned: false`). `terraform/variables.tf` says the same: "registered on Cloudflare Registrar in the same account".
- **Who can administer it: Josh, as far as can be seen.** The member list needs a Super Administrator, or a token with *Account Settings Read*, and no credential that could be read for this research has it (`GET …/members` was 403). The R2 state credentials are in Josh's personal 1Password (`nitrovin tfstate (R2)`, the item the README creates). This Mac's `wrangler` login is a different Cloudflare login, and none of its accounts is the nitrovin account, so `npx wrangler whoami` here says nothing about nitrovin.app.
- **What deploy needs from GitHub:** three repository secrets and one repository variable, and none of them names the GitHub owner.
  - `CLOUDFLARE_API_TOKEN`: the CI token, scoped to the account (*Account Settings Read*, *Workers Scripts Write*, *Workers Containers Write*, *Cloudchamber Write*) and to the zone `nitrovin.app` (*Zone Read*, *Zone Settings Write*, *DNS Write*, *Workers Routes Write*, *Zone WAF Write*, *Dynamic URL Redirects Write*), no expiry (`terraform/token/main.tf`).
  - `R2_ACCESS_KEY_ID` / `R2_SECRET_ACCESS_KEY`: the R2 token scoped to objects in `nitrovin-tfstate`. The bucket holds `cloudflare/main.tfstate` (19.6 KB), `cloudflare/token.tfstate` (146 KB) and one copy under `history/main/`.
  - `CLOUDFLARE_ACCOUNT_ID` (variable): the account, and from it the R2 endpoint `https://<account id>.r2.cloudflarestorage.com` (`deploy.yml:46-48`).
  - The `nitrovin.app` environment has no secrets, variables or protection rules of its own.
  - GitHub's transfer docs say secrets "remain associated after the transfer" ([Transferring a repository](https://docs.github.com/en/repositories/creating-and-managing-repositories/transferring-a-repository)). Variables and environments aren't named there [INFERENCE: they are repository settings too and move with it; check with `gh variable list -R marcy-cloud/nitrovin` after the transfer, which is [#182](https://github.com/joshuasprow/nitrovin/issues/182)'s question].
- **What in the deploy path does name the owner** (all on the GitHub side, none in Cloudflare):
  - `IMAGE: ghcr.io/joshuasprow/nitrovin` (`deploy.yml:45`), the `FROM` default in `worker/Dockerfile:5`, `smoke.sh`'s usage comment, and the README's `gh … -R joshuasprow/nitrovin` commands.
  - The cosign identity is `https://github.com/${{ github.repository }}/…/release.yml@refs/tags/$tag` (`deploy.yml:131`), so it follows the repository by itself. That also means that after the move `deploy.yml` can't deploy a release built before the move: the new `IMAGE` has none of the old tags, and the old tags' signatures name `joshuasprow/nitrovin`. Under the clean cutover, rolling back past the move means `npx wrangler rollback` (Worker only) or a new release. The runbook ([#187](https://github.com/joshuasprow/nitrovin/issues/187)) should say so.
  - Cloudflare holds no GitHub connection: the Worker is deployed by `wrangler` from Actions, not Workers Builds, and the image reaches Cloudflare's registry via `wrangler deploy`'s push, not a pull from ghcr.io.

## Leaving the account where it is (re-point GitHub only)

Nothing on Cloudflare changes. After the repository transfer:

1. Check the three secrets and the variable arrived in `marcy-cloud/nitrovin` (they aren't tied to the owner, so the values stay valid).
2. Change `IMAGE`, the `Dockerfile` `FROM` default, `smoke.sh`'s comment and the README's `-R` commands to `marcy-cloud`.
3. The first release from `marcy-cloud` deploys through the same CI token, the same Terraform state and the same Worker, with no downtime.

What this leaves as it is: the account, its billing (Workers Paid, about $5–12 a month per the README) and its administration stay Josh's personally. If Marcy should share in that without moving anything, Cloudflare offers members on any account: a Super Administrator can invite members with policies, from *Administrator* (everything except members and billing) to *Super Administrator* (members, billing, account-owned tokens) ([Members and permissions](https://developers.cloudflare.com/fundamentals/manage-members/), [Manage](https://developers.cloudflare.com/fundamentals/manage-members/manage/), [Roles](https://developers.cloudflare.com/fundamentals/manage-members/roles/)).

One step further, still without moving a resource: hand the whole account over. Invite Marcy's owner as a Super Administrator, have them change the billing profile, and Josh leaves or stays ("If you are a Super Administrator … and you decide to leave the account, you can invite a new Super Administrator"; the account itself can't be deleted, [Manage](https://developers.cloudflare.com/fundamentals/manage-members/manage/#super-administrator-access)). This is only clean if the account holds nothing else of Josh's (see Gaps).

## Moving to another account (e.g. Marcy's)

No Cloudflare resource here moves between accounts in place. Each one is moved by Cloudflare's own process or recreated:

| Piece | What a move takes | Source |
| --- | --- | --- |
| Domain registration | Registrar's inter-account move: the target adds `nitrovin.app` as a website and picks a plan, the source submits the move, and the target accepts within 5 days. The domain must have been registered **more than 10 days ago (so not before 2026-11-20)**, DNSSEC must be off (it is), and after the move the registration is transfer-locked for 30 days. "No other configuration will be moved." | [Move a Cloudflare Registrar domain registration between accounts](https://developers.cloudflare.com/registrar/account-options/inter-account-transfer/) |
| Zone | A new zone in the target account, so a new zone id, and the old one goes to *Moved Away*, then *Deleted*. Certificates are reissued and DNS records recreated. Every Terraform resource in `terraform/` is zone-scoped and is recreated there. | [Move a domain between Cloudflare accounts](https://developers.cloudflare.com/fundamentals/manage-domains/move-domain/) |
| Worker, Durable Object, container | `wrangler deploy` into the target account recreates all three. A Durable Object namespace can only be transferred between Workers "in the same account". The `Nitrovin` object holds no application data (it is the `Container` class from `@cloudflare/containers`; `nitrovin-enam` is created again on the first request). The image goes to the target account's registry (`registry.cloudflare.com/<ACCOUNT_ID>/…`), which `wrangler deploy` pushes to anyway. The target needs Workers Paid. | [Durable Objects migrations](https://developers.cloudflare.com/durable-objects/reference/durable-objects-migrations/#transfer-constraints), [Image management](https://developers.cloudflare.com/containers/guides/image-management/) |
| R2 state and tokens | Re-run the README's bootstrap in the target account: `terraform/token/` makes a new `nitrovin-tfstate`, the R2 token and the CI token. Then either copy the two state files (165 KB, `aws s3 cp`) and `terraform state rm` the zone-bound resources, or start `main.tfstate` empty, since the target zone has none of them. New `CLOUDFLARE_API_TOKEN`, `R2_*` and `CLOUDFLARE_ACCOUNT_ID` go into the repository. Super Slurper or Sippy are for large migrations and aren't needed here. | `deploy/cloudflare/README.md` (Bootstrap), [R2 data migration](https://developers.cloudflare.com/r2/data-migration/) |
| Cut-over window | The custom domain (`cloudflare_workers_custom_domain`) can only attach once the zone is active in the target account [INFERENCE from the zone being a new object there]. So the order is: deploy the Worker to the target first (it serves nothing, `workers_dev: false`), complete the registrar move, then `terraform apply` attaches the apex and `www`. `nitrovin.app` is down for the minutes between, and so is any `?go-get=1` answer. | — |

Cost of moving compared with staying: a second bootstrap, new credentials, a downtime window, a date gate (not before 2026-11-20) and a 30-day lock afterwards, in exchange for the zone and billing sitting in a company account. Nothing about the GitHub move requires it: the GitHub side reaches the account only through the three secrets and the variable.

## What the vanity Go paths need from the Worker (either way)

Account-independent. The details are [#181](https://github.com/joshuasprow/nitrovin/issues/181)'s; from the Cloudflare side:

- **A branch at the top of `fetch` in `worker/src/index.ts`** (before `forward` and the cache, around line 38) that answers `GET …?go-get=1` under each module prefix (`/nitrovin…`, `/data…`) with a small HTML page carrying `go-import` (and optionally `go-source`) meta tags pointing at `https://github.com/marcy-cloud/<repo>`. Today `https://nitrovin.app/nitrovin?go-get=1` returns the container's 404. The Worker is where the owner name lives after the change, so a later move edits one file and every module path stays the same.
- **Nothing new in Terraform or the CI token:** the custom domain already sends every apex path to the Worker, `cacheable()` covers only `/v1/decode/*` and `/v1/info`, the rate limit covers only `/v1/decode*` and `/mcp`, and *Workers Scripts Write* already lets `deploy.yml` ship the code.
- **Ordering:** `deploy.yml` deploys the Worker code from `main` (the `workflow_run` checkout), not from the tag, so the meta tags go live with the next deploy after they merge. That must happen before the first release under a vanity path, because proxy.golang.org resolves the path on first request.
- **If the account moves,** move it before the module paths switch, or accept that `GOPROXY=direct` users and uncached proxy lookups fail during the cut-over window.

## Gaps

- **Members of the nitrovin account, and whether it holds anything besides nitrovin.** The credentials used here can't list members and are scoped to the `nitrovin.app` zone, so other zones, if any, are invisible. (R2 was visible account-wide: only `nitrovin-tfstate`.) To close: Josh, signed in to the nitrovin account, opens **Members** (who is there, with what roles) and **Domains**/**Workers & Pages** (anything that isn't nitrovin's). That decides whether a whole-account handover is possible.
- **Marcy's side: which Cloudflare account would receive it, and does it have Workers Paid.** Needed only if the account moves. To close: whoever is Super Administrator of the Marcy account checks **Billing → Subscriptions** for Workers Paid.
- **Variables and environments surviving the repo transfer:** not stated in GitHub's docs. To close: `gh variable list -R marcy-cloud/nitrovin` right after the transfer, before the first deploy ([#182](https://github.com/joshuasprow/nitrovin/issues/182) / [#187](https://github.com/joshuasprow/nitrovin/issues/187)).
- **The exact downtime of an account move** isn't measured. Avoided entirely by not moving the account.
