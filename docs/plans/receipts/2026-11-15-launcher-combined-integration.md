# Launcher combined integration — PR55

Production candidate: `76cb3bf9476f50ad954e6d082e049b0835267269`.
Includes current master/PR54 `18f7997344f9d486792cb21b3687dc126eb78817`.
The historical feature checkpoints and initial gates remain in
`2026-11-15-launcher-modern-port.md`; they are not final candidate evidence.

## Completed source and review

Native Codex and ACP job backends, profile installation, embedded/npm assets,
web onboarding and exact launch retry/reload recovery are ported to the modern
exact-root/generation authority. Blank source selects normal managed installation;
explicit custom source remains optional. Independent whole-feature, onboarding,
maintenance, repair-wiring and combined integration reviews passed after fixes.

Managed installation registers configured canonical non-scratch project roots.
Automatic repair validates saved metadata, unit/config/software and manager
identity, stages current assets and a private CLI, and saves durable old/new
publication intent. Enablement and stopped state are preserved; only previously
active intent authorizes restart. Failed/failed recovery requires that validated
pending intent; initial and final inspection remain strict. Custom, legacy,
unregistered or uncertain installations retain actionable pending status.

The combined review found missing registry convergence after a successful helper
upgrade. Caller completion now carries inspected preimage and verified replacement,
compares the complete current artifact generation/set under the registry lock,
and publishes replacements and applied digest atomically. Hook repair uses the
same completion CAS; explicit hook installation still registers immediately.
Concurrent additions/removal/adoption and helper failures retain current evidence.

## Observed gates

- Exact caller RED reproduced the single-root and both shared-root failures,
  1.490s. Corrected Local/Launcher/CodexHooks controls PASS19.977s and selected
  registry/launcher convergence races PASS15.148s.
- Failed-restart RED reproduced failed/failed refusal. Full managed repair
  controls PASS19.922s; selected recovery races PASS80.393s.
- Final merged launcher strict TypeScript and Node suite: 2095/2095 PASS,
  zero skipped. UI and smoke/lifecycle controls: 580/580 PASS, zero skipped.
- Both npm architectures built and packed after the registry correction.
  Installed-package tests: 4/4 PASS, 6.285s.
- Disposable browser journey passed default blank-source onboarding, real hub
  enrollment/discovery, unavailable-host recovery, mobile layout and exact
  request retry after held navigation/reload/lost responses; nine screenshots.
  Browser source is unchanged by the final Go registry correction.
- Retained CLI SHA256:
  `6922e3a3df907241cb5b19b9e2b1b6ea63284f59bc038c5b3e0baac4ba21183c`.
  Retained demo SHA256:
  `c64071e75f787cbbd3727801d4924c97e0b207f28d51f8c24a6898a7c2cda141`.

Full Go repository gate PASS: 40 packages with tests, no package/test failures;
CLI136.912s, runtime1.180s, HTTP44.981s, hub109.476s. Build-box command exited0
in277.3s. Exact production bytes equal candidate76cb3bf9; subsequent runner
changes affect acceptance scripts only. Disposable execution results follow.

## Recovery inventory and limits

All 91 registered worktrees on xsm were inspected read-only. Recovered historical
actor/manager patches target omitted hub-scoped harnesses and have no applicable
source residual in this port. Their unresolved-execution contract is preserved:
missing process projection cannot authorize cleanup. The new fixture manager
retains ambiguous intent and validates exact process identity before leader-only
teardown. Separate unfinished Windows hook-pipe tests, thumbnail integration and
other unrelated historical WIP are preserved remotely, not folded into these PRs.

This campaign merges source and runs disposable acceptance. It does not publish
npm releases, replace real user services, clear retained real backlog, or claim
real-systemd activation from a fixture adapter.
