# The released image, by tag and digest. Cloudflare Containers cannot pull
# from ghcr.io, so wrangler deploy builds this and pushes it to Cloudflare's
# registry: the same layers. deploy.yml rewrites the FROM line to the release
# it deploys, after checking the digest is the one release.yml signed.
FROM ghcr.io/joshuasprow/nitrovin:v0.1.5@sha256:ae840befe66aa1501b36d9c611a9ff4d4ee0ecc4284c9aa6d8164e1884deab3c
# The image's command, plus CORS for browsers on any origin, on /v1 and /mcp
# only (docs/api/README.md).
CMD ["serve", "--addr", ":8080", "--cors", "*"]
