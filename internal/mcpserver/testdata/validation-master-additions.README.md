# Master call-schema capture

`validation-before-projection.json` remains the frozen original 23-tool baseline.
`validation-master-additions.json` adds only the three new tools from unprojected
master `6a3a17ee9d7f55d7072ccc9f48655619a60380ec`.

The additions were captured independently in `/tmp/vecnet-mcp-baseline-r4`, a
`git archive` of that master, using a temporary `TestCaptureMasterCallSchemas`.
The test loaded envelope definitions, ran master's `narrowAttachments` and
`compileInput` on its `toolSpecs`, decoded the full pre-projection input schemas,
then replaced each declared `hubValues` property with `{"type":"string"}` exactly
as master's call-validator builder does. It removed only description, title and
comment prose. Definitions, required fields and additional-property rules remain.
No diet projection code or tables were used to construct the fixture.

Capture command (executed on x-tuf, not locally):

```sh
BB_HOST=x-tuf BB_JOBS=4 BB_ROOT=/tmp/vecnet-mcp-baseline-r4 BB_LANE=mcp-diet-baseline-r4 just bb go test -count=1 -run '^TestCaptureMasterCallSchemas$' ./internal/mcpserver/
```

The equivalence cases include semantic-invalid strings that must pass shape
validation so the hub can check authority first. Separate wire tests pin that
authority and semantic refusal order for peer, principal and operator callers.
