# Tech stack for hub, connectors and CLI

Type: research
Status: open
Blocked by:

## Question

Max delegated this. Decide language/runtime, web framework, SQLite access layer, MCP server library, SSE handling, blob storage layout, and how the `vecnet` CLI is distributed, with an IGC matrix. Constraints: pag-chat and webhook2mcp are Node/TypeScript; an MCP SDK must exist; one person operates it; anything compiled must build on x-alpha not xsm (AVX-512 stamp hazard); dev server ports are generated, not defaults. Deliverable: `docs/research/stack.md` + draft ADR in `docs/adr/`.
