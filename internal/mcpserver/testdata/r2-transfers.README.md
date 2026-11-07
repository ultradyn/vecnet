# R2 transfer schema capture

The original discovery diet fixtures remain unchanged. These supplemental fixtures
capture the three R2 blob tools from unprojected r2-build `225b3f4` independently
of this merge and its projection implementation.

An isolated `git archive 225b3f4` in `/tmp/vecnet-r2-capture-225b3f4` used a
temporary `TestCaptureR2CallSchemas`. It loaded that commit's envelope definitions,
ran `narrowAttachments` and `compileInput` on its `toolSpecs`, and applied any
`hubValues` exceptions exactly as its call-validator builder does. Output schemas
came directly from that commit's `toolSpec.output`. Only description, title and
comment prose was removed. No projection code or tables exist in that archive.

Capture command (on x-p35, never locally):

```sh
BB_HOST=x-p35 BB_JOBS=2 BB_ROOT=/tmp/vecnet-r2-capture-225b3f4 BB_LANE=r2-schema-capture just bb go test -count=1 -run '^TestCaptureR2CallSchemas$' ./internal/mcpserver/
```

`validation-r2-transfers.json` explicitly supersedes the two disk-only blob call
schemas and adds blob status. The equivalence test keeps all original disk cases
and adds grant, completion, status, phase mixing, type, bounds and required-field
cases. `output-r2-transfers.json` supersedes the two disk-only blob output schemas
and adds status. All unrelated schemas still use the frozen diet fixtures.
