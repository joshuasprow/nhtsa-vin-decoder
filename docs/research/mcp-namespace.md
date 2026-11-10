# MCP Registry: publishing nitrovin under `app.nitrovin`

Research for [#180](https://github.com/joshuasprow/nitrovin/issues/180) on [Map: move nitrovin to the marcy-cloud org](https://github.com/joshuasprow/nitrovin/issues/179). Sources read 2026-11-10 at [modelcontextprotocol/registry@7e8fa02](https://github.com/modelcontextprotocol/registry/tree/7e8fa024d7caeba23cfcfc62ca64b17b750fd4f7) and the live API at `registry.modelcontextprotocol.io`.

## Answer

- **Yes, a domain namespace is supported.** Domain auth grants names of the form `<reverse-domain>/*` and `<reverse-domain>.*/*`; owning `nitrovin.app` grants `app.nitrovin/*`, so `app.nitrovin/nitrovin` is a valid name. ([authentication.mdx](https://github.com/modelcontextprotocol/registry/blob/7e8fa024d7caeba23cfcfc62ca64b17b750fd4f7/docs/modelcontextprotocol-io/authentication.mdx); `internal/api/handlers/v0/auth/dns.go` grants both the exact and the subdomain pattern, per its tests in `dns_test.go`.)
- **Two verification methods, both key-pair based.** You generate an Ed25519 (or ECDSA P-384) key pair, publish the public key, and `mcp-publisher` signs a timestamp with the private key at login:
  - **DNS:** a TXT record `v=MCPv1; k=ed25519; p=<base64 pubkey>` on the **apex** (`nitrovin.app`, not `_mcp-auth.nitrovin.app`). Login: `mcp-publisher login dns --domain nitrovin.app --private-key <hex>`.
  - **HTTP:** the same record served as `text` at `https://nitrovin.app/.well-known/mcp-registry-auth`, `200` with no redirects, reachable from Google Cloud addresses (WAF/bot rules must let it through). Login: `mcp-publisher login http …`. The Worker could serve this, but DNS needs no code and no WAF exception.
  - Keys can also live in Google Cloud KMS or Azure Key Vault (`login dns google-kms` / `azure-key-vault`); neither is in nitrovin's stack.
  - Today `dig TXT nitrovin.app` returns nothing, so there is no MCPv1 or other TXT record to collide with.
- **Unattended in GitHub Actions: yes.** The registry's own [github-actions.mdx](https://github.com/modelcontextprotocol/registry/blob/7e8fa024d7caeba23cfcfc62ca64b17b750fd4f7/docs/modelcontextprotocol-io/github-actions.mdx) shows `mcp-publisher login dns --domain example.com --private-key ${{ secrets.… }}` followed by `mcp-publisher publish`. The private key lives as a GitHub secret (the doc recommends an environment secret limited to release tags, since the key can publish or overwrite anything under `app.nitrovin*`). Release would swap `login github-oidc` (`release.yml:301`) for `login dns`; the job then no longer needs `id-token: write` for the registry (cosign still does). The DNS record is a one-time manual step in whichever Cloudflare account holds `nitrovin.app`.
- **The OCI label must match.** For OCI packages the registry checks the image's `io.modelcontextprotocol.server.name` annotation and it **MUST** equal the `server.json` name ([package-types.mdx](https://github.com/modelcontextprotocol/registry/blob/7e8fa024d7caeba23cfcfc62ca64b17b750fd4f7/docs/modelcontextprotocol-io/package-types.mdx)). So `.goreleaser.yaml:185`, `scripts/mcp-server-json.sh` and the label check in `release.yml:178` change together to `app.nitrovin/nitrovin`. The image path (`ghcr.io/marcy-cloud/nitrovin`) is independent of the name.
- **The old entry cannot be renamed or redirected.** There is no rename, alias or "moved to" field in the API or CLI. Each name is its own server; the new name starts with a fresh version history. What exists is `mcp-publisher status --status <active|deprecated|deleted> [--all-versions] [--message …]` ([commands.md](https://github.com/modelcontextprotocol/registry/blob/7e8fa024d7caeba23cfcfc62ca64b17b750fd4f7/docs/reference/cli/commands.md)):
  - `deprecated`: still listed, with a warning and the message (e.g. "moved to app.nitrovin/nitrovin"). This is the closest thing to a pointer.
  - `deleted`: hidden from default listings, still fetchable with `include_deleted=true`, restorable. Nothing is ever removed for good ([faq.mdx](https://github.com/modelcontextprotocol/registry/blob/7e8fa024d7caeba23cfcfc62ca64b17b750fd4f7/docs/modelcontextprotocol-io/faq.mdx)).
  - Published version metadata is immutable, so old versions keep naming `ghcr.io/joshuasprow/nitrovin` either way.
- **Who can change the old entry's status after the move.** Status changes need `publish`/`edit` on `io.github.joshuasprow/*`. GitHub OIDC grants `io.github.<repository_owner>/*`, so after the transfer the `marcy-cloud` workflow gets `io.github.marcy-cloud/*` and can no longer touch the old entry. Josh can still do it by hand: `mcp-publisher login github` (device flow) always grants the personal namespace. Do it before or after the transfer; it does not depend on the repo.
- **Live state today:** `io.github.joshuasprow/nitrovin` has versions 0.1.5, 0.1.6, 0.1.7, 0.1.8 and 0.2.0 (latest), all `active`.

## Fit with the map's decisions

- Clean cutover: deprecate (or delete) all versions of `io.github.joshuasprow/nitrovin` with a message naming `app.nitrovin/nitrovin`, and publish the first marcy-cloud release under the new name. `deprecated` matches "releases already published keep working where they are"; `deleted` matches "nothing kept alive". Either is reversible.
- The new name never mentions an owner, so a future org move only changes the image path and the secret's home, not the registry name.
- The registry is still in preview; its docs warn of possible breaking changes or data resets before GA.

## Gaps

- **The DNS record can't be placed until the owner of `nitrovin.app`'s zone is known.** Closed by the Cloudflare research ticket (which account owns `nitrovin.app`).
- **Not exercised end to end.** No key was generated and no login was tried (read-only rule). Closed by the execution ticket: add the TXT record, run `mcp-publisher login dns --domain nitrovin.app` locally once, then `mcp-publisher validate` on a `server.json` named `app.nitrovin/nitrovin`.
- **macOS key generation:** Ed25519 via `openssl genpkey` needs OpenSSL 3 (`brew install openssl@3`), not the system LibreSSL (per authentication.mdx).
