# MCP discovery projection

`tools.go` builds and compiles the original input schemas, including the existing
hub-value exceptions, before calling `projectTool` on the discovery entry. The
projection copies that entry and changes only `tools/list`. It never changes the
envelope schema, prechecks, argument normalization, handlers or their validators.
An omitted discovery property is still accepted or rejected exactly as before.

The listing retains common call shapes, required fields, enums and defaults.
`origin` is omitted because MCP ingress ignores it. Attachment verification
evidence is omitted because callers cannot supply it. References are inlined.
String syntax, numeric and size limits, attachment retention conditions, and
job/approval payload details are documented by `vecnet_guide` on demand. The
base64 character cap stays visible for upload clients. Output schemas retain all
their original validation keywords. The property-name allowlist has 15 entries.

The peer catalog measured 32,469 compact JSON bytes before and 18,305 after;
`vecnet_send` fell from 9,654 to 2,689 bytes. The 3,000-byte send target is met;
the 14,000-byte total target is **not met**. This is the practical minimum selected
for this change, not a proof of an absolute byte minimum. Further reduction would
require dropping more calling guidance, supported argument shapes, or client
metadata; this change retains those rather than claiming the total target passed.
As a check, retaining the projected shapes and metadata but emptying all 106
descriptions still takes 13,746 bytes. A 14,000-byte catalog would leave only 254
bytes of text for 23 tool descriptions and 83 property descriptions. That cannot
carry the required action selection and calling rules. Property descriptions
already top out at 38 bytes and the self-explanatory-name allowlist stays small.
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
