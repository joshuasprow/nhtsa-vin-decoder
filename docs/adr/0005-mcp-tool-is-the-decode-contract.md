# The MCP tool is the decode contract, unchanged

`nitrovin mcp` exposes one tool, `decode_vin`. Its input is the contract's
request (`vin`, `model_year`, `full`, `shape`, `now`) with the contract's
defaults, and its result is the contract's decode. A reader might expect an
assistant-facing tool to get a trimmed output tuned for language models, or a
default of `shape=nitrovin`, which lists only decoded elements. We kept one
contract instead. A decode copied out of a chat is byte-for-byte the decode
`nitrovin decode` prints for the same `now`. The assistant can still ask for
`shape=nitrovin`. Under ADR 0004's versioning, a per-surface default or a
second output shape would have to stay for all of v1. The cost is tokens: the
default `vpic` layout sends 144 keys in 2026_10, most of them `""`.

The result carries the decode twice: as `structuredContent` and as the same
compact JSON in one text block. Clients disagree about which one the model sees
(Claude Code passes `structuredContent` and drops the text; Cursor drops
`structuredContent`). Identical bytes mean the model gets the same answer in
either. A refused request is a tool error (`isError: true`) whose text is the
contract's Problem, so the model can correct the call. The SDK's own argument
check would produce a different message than every other surface does, so the
server registers the tool without it and validates the request in the shared
decode layer.

## Considered options

- An MCP default of `shape=nitrovin`: fewer tokens and readable names, but a
  default that differs by surface.
- A trimmed assistant subset: a second output contract to version and test.
- The `-o text` rows in the text block, with the decode as
  `structuredContent`: cheaper to read, but the model sees different content
  depending on the client.
- Extra tools (`validate_vin`, an info tool) or a data-month resource: each
  repeats part of the decode, which already carries `data_month`,
  `check_digit_valid` and `error_codes`. Any of them can be added later within
  v1.

## Consequences

`docs/api/mcp-tools.json` bundles copies of `decode.schema.json`'s
definitions, because MCP forbids fetching a `$ref` over the network. A build
check must keep those copies equal to the source.
