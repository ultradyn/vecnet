# Route statistics protocol source port — 2026-11-10

This source component documents the separately ported route-statistics storage, capture, read/API, MCP and public CLI contract. It is prepared on the master-based replacement branch; it does not establish final integration, migration rollout or deployment readiness.

The normative protocol delta is the selected `3d06953f..28058abb` addition/replacement: `docs/PROTOCOL.md` +64/-1 and a current dated changelog entry +2/-0. Independent GPT-6.1-Sol source review found Spec PASS / Quality PASS, C0/I0/M0. The controller verified all 2144 inventoried source inputs remained unchanged throughout that review. No executable gate was run for this documentation-only component; final current-master protocol and whole-feature gates remain required.

The contract distinguishes bounded route-wide samples from uncapped retained-delivery evidence; per-route insert-only pruning from a global storage bound; actual client calls and best-effort detached telemetry from universal latency guarantees; and transport-specific outcomes, null/zero metrics, HTTP/MCP authority, fixed private errors and CLI signed relocation. Historical native-code ancestry and its pending actor-policy decision are excluded.
