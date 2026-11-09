# Operator route statistics — CLI component evidence

Ready for review, component only. The master-based route-statistics branch adds `vecnet route stats <peer_id|route_id|sub_id> [--json]` through the existing selected-profile client and dispatcher. JSON preserves the full shared DTO; human output keeps sampled route attempts separate from retained-delivery evidence, removes terminal control characters and keeps null distinct from measured zero. Invalid arity is refused before client selection. The route-level and stats-level option terminators preserve literal flag-looking selectors.

The helper is the exact reviewed157-line selected C28058abb source. The three unchanged selected fixtures total1365lines. Parent commands.go integration is exactly8added/1removed lines for help, two early dispatch branches and route usage. Existing startup, parser, client and verified follow machinery remain unchanged. Only the exact410/hub_moved pair preserves the notice control payload; verification, one retry, profile/pin backup and refusal paths use the existing implementation.

Parent-owned x-boss evidence used a separate frozen tree at each stage:2142 entries for the API prerequisite,2143 after adding the helper, and2143 with the reviewed commands.go change for GREEN. Behavioral RED and GREEN have different commands.go bytes; each run independently verified its own before/after source equality.

- API prerequisite: seven missing helper-method diagnostics, zero executed tests. Actual1/bb1, raw97048842b1f494b897bf8f25c5381025c76d5f87bd205ed26532a56aec57b0cc. This is not behavioral RED.
- After the exact helper and before commands.go integration:41 genuine public-dispatch/relocation failing leaves,53 passing controls including all three helper roots and old route verbs. Actual1/bb1, raw7e8e2105690a9d76f20de030f26bfef1fdfe835d38c9e9aead6f313aa98ff223.
- Focused GREEN:102 passing test events across all12 named new/control roots, zero failures/skips. Actual0/bb0, raw5678d732397f7c89029bd054bf0520ade2a6218570275cbf3da294f54c1d1021.
- Full internal/cli:1199 passing test events, zero failures/skips. Actual0/bb0, raw6c18898b851564fb153257803644d68450473afea06bb70d8d3243dbf1ce4ed1.
- Fresh independent Sol fixture review, same independent helper review and final five-file CLI source review all Spec PASS / Quality PASS, C0/I0/M0. Each report discloses its source-only scope; runtime evidence is owned by the parent.

Every actual gate used x-boss with one job, a physical owned0700 temporary root and isolated synthetic HOME/XDG/CODEX/config state. Full before/after source hashes matched; actual process status and wrapper footer agreed; two owned process censuses were empty after each gate. Machine receipts and full source manifests remain in .superpowers/sdd/2026-11-09-route-stats-master in the retained worktree.

This commits only the CLI component atop B2b b0edbe71 on the feature branch. It does not merge original PR #3 or the full replacement into master and does not claim deployment. Normative documentation, reconciliation with the latest master, exact-tree integration gates/compatibility evidence, final whole-feature review and rollout remain outstanding. Native ancestry and its pending actor-policy approval remain excluded.
