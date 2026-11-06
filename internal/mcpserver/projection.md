# MCP discovery projection

`tools.go` builds and compiles the original input schemas, including the existing
hub-value exceptions, before calling `projectTool` on the discovery entry. The
projection copies that entry and changes only `tools/list`. It never changes the
envelope schema, prechecks, handlers or their validators. Round three adds a
separate MCP ingress adapter that normalizes RFC 3339 timestamp arguments to
canonical UTC milliseconds before those original checks.
An omitted discovery property is still accepted or rejected exactly as before.

The listing retains common call shapes, required fields, enums and defaults.
`origin` is omitted because MCP ingress ignores it. Attachment verification
evidence is omitted because callers cannot supply it. References are inlined.
Paging minimum/maximum bounds stay visible and come from the call schemas.
Other string syntax, numeric and size limits, attachment retention conditions, and
job/approval payload details are documented by `vecnet_guide` on demand. The
base64 character cap stays visible for upload clients. Output schemas retain all
their original validation keywords. The property-name allowlist has 15 entries.

The original peer catalog measured 32,469 compact JSON bytes. Round one reduced
it to 18,305 bytes; round two uses **19,499 bytes**, within the accepted
19,500-byte cap, with plain sentences and explicit action and paging guidance.
`vecnet_send` is 2,816 bytes, below its original 3,000-byte target.
Keeping the projected shapes and metadata but emptying all 106 descriptions still
takes 13,746 bytes. The original 14,000-byte target leaves only 254 bytes for 23
tool descriptions and 83 property descriptions, so its exception remains accepted.
All tool descriptions are at most 177 bytes. Property descriptions are at most
70 bytes except the 99-byte priority guidance, which preserves the required
choice, immediate wake cost, and hold behavior. The explicit allowlist is unchanged.
The regression budgets freeze the achieved total plus 10% and each tool plus 15%.

Annotations and all four output contracts remain. The minimal OAuth
`securitySchemes` declaration is retained for ChatGPT account linking, even
though it is an extension rather than a base MCP Tool field. The server uses
`2026-07-28` for current requests and `2025-11-25` for legacy sessions, with
`2025-06-18` compatibility negotiation; the same tools are listed in both eras.
See [OpenAI plugin authentication](https://developers.openai.com/plugins/build/auth),
[Apps SDK tool descriptors](https://developers.openai.com/apps-sdk/reference/),
and [MCP 2025-11-25 schemas](https://modelcontextprotocol.io/specification/2025-11-25/schema).
The local discovery contract is also recorded in `docs/PROTOCOL.md` section 4.3.

The validation fixture was captured from the failing budget run before this
projection, with prose removed and the existing hub-value shape exceptions
applied. Every tool has representative valid/invalid cases against that fixture
and its live call validator, including hidden origin, attachment evidence,
numeric boundaries, unknown properties, missing required fields, and values
whose semantic rejection belongs to the hub. A separate wire test verifies that
a supplied valid origin is still ignored and an invalid origin is still refused.
Output validation contracts are compared in full after removing descriptions.

Round three explicitly accepts RFC 3339 timestamp spellings previously refused
without milliseconds or UTC Z: threads.since, presence.at, send/reply.expires_at,
send/reply attachments[].expires_at, and send.approval_request.expires_at.
Fractional seconds are optional and truncate to milliseconds; numeric offsets
convert to UTC. HTTP and envelope wire formats remain unchanged. The frozen
validation fixture stays unchanged; the equivalence table records these
formerly refused inputs separately. Arbitrary metadata and payloads are not
timestamp arguments and are preserved.

Top-level tool argument `_meta` is discarded as protocol metadata before
prechecks, normalization, validation and handling; diagnostics record only a
boolean presence flag. This branch has no `clamp_rewrites`/`original_value`
builder. Validation error data contains only the path and omits original
values entirely, including scalars. Regression tests guard against composite
echoes and ensure nested application metadata is preserved.
