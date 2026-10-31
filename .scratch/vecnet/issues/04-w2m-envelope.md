# webhook2mcp job envelope and signing, shown to Max

Type: research
Status: open
Blocked by:

## Question

Extract webhook2mcp's envelope (`docs/grokbot-contract.md`, `examples/grokbot-envelope.schema.json` in `~/src/w2m-ops-admin`): the twelve required fields, the optional `job_request`, the `job.status` report with strictly increasing `sequence`, the 2xx-vs-job.status receipt split, retry/terminal status codes, and the Standard Webhooks signing headers. Produce a short readable summary with the actual JSON shapes so Max can see it (he asked), and list what vecnet should borrow verbatim vs adapt. Deliverable: `docs/research/w2m-envelope.md`.
