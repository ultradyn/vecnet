# Changelog

- 2026-11-04: reaction wakes are claimed before each Events callback or operator notification attempt (`deliveries.dispatching`, migration 0013, `Hub.BeginDispatch`/`EndDispatch`); an in-flight wake is not superseded, a failed one settles the withdrawal, a notified one becomes `pushed` (PROTOCOL §17.5).
- 2026-11-04: HTTP and `vecnet_react` enforce the 32-byte reaction emoji body cap before semantic validation; MCP documents optional `op` defaulting to `add` (`internal/httpapi/reactions.go`, `internal/mcpserver/tools.go`, PROTOCOL §4.3).
- 2026-11-03: HTTP and `vecnet_react` reject explicitly empty reaction priorities before target lookup; omitted priorities retain hub/client defaults (PROTOCOL §17.2).
- 2026-11-03: `vecnet_react` preserves body-shape validation and shares HTTP reaction semantic errors and check order in `Hub.React` (`internal/mcpserver/tools.go`, PROTOCOL §17.2).
- 2026-11-03: reactions: POST /v1/reactions, reaction Event, Message kind reaction, operator_view reason reaction (PROTOCOL §17).
