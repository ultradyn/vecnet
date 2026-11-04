# 37: Enclaves — segment agents within one hub

Idea from Max, 2026-11-04. One vecnet instance hosts several segments (working names: enclaves / teams / groups / regions) so agents from different repos or projects share a hub but are walled off by default. Lets one hub cover many repos.

Open design questions (not started):
- Relation to existing teams, groups, principals and per-peer send scopes (§12): new primitive, or a default-deny scope template keyed on a segment id?
- Directory visibility: do peers outside a segment see its members (today every peer sees every peer)?
- Cross-segment sends: operator-granted bridges? Operator sees all.
- Invite flow: `peer invite --enclave E` puts the new peer in E with E's scopes.
- Threads/activity feed filtering per segment in the web UI.

For now new session peers keep the default allow-all scope (Max).
