#!/usr/bin/env bash
# Writes nitrovin's MCP Registry entry, server.json, for one release:
#
#   scripts/mcp-server-json.sh <file> <version>
#
# .goreleaser.yaml runs it before the build, so the file is checksummed and
# uploaded with the release; release.yml signs it and publishes it to
# registry.modelcontextprotocol.io. The entry is the ghcr.io image at the
# release tag, which the registry accepts because the image carries the
# io.modelcontextprotocol.server.name label with the same name. The image runs
# `serve` by default, so the entry passes `mcp`. Its title and description are
# the decode_vin tool's own words from docs/api/mcp-tools.json (the
# description is the tool description's first clause: the registry allows 100
# characters).
set -euo pipefail

out=$1
version=$2
mkdir -p "$(dirname "$out")"
jq --arg version "$version" '
  .tools[0] as $tool
  | ($tool.description | split(", ")[0]) as $description
  | if ($description | length) > 100 then
      error("server.json: description is \($description | length) characters, the registry allows 100: \($description)")
    else . end
  | {
      "$schema": "https://static.modelcontextprotocol.io/schemas/2025-12-11/server.schema.json",
      name: "io.github.joshuasprow/nitrovin",
      title: $tool.title,
      description: $description,
      version: $version,
      websiteUrl: "https://github.com/joshuasprow/nitrovin",
      repository: {url: "https://github.com/joshuasprow/nitrovin", source: "github"},
      packages: [{
        registryType: "oci",
        identifier: "ghcr.io/joshuasprow/nitrovin:v\($version)",
        transport: {type: "stdio"},
        packageArguments: [{type: "positional", value: "mcp"}]
      }]
    }
' docs/api/mcp-tools.json >"$out"
