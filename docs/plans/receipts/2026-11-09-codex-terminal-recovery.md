# Codex terminal-only recovery: component verification receipt

Recorded 2026-11-09T02:54:54.727779+00:00. Base: 3d06953f21299b268d84649ea3a488b62807ddad. Branch: fix/paused-peer-wake.
#61 remains open. These are synthetic build-box results, not proof of the reported eleven-message incident or installed behavior.

## Behavior and scope

A terminal turn notification can arrive without an idle-status notice. The engine now reads the already loaded thread authoritatively, retries failed observations with bounded/coalesced scheduling, respects stale-response and policy fences, waits for claim/receipt settlement, and reconsiders eligible queued deliveries. Repeated idle observations do not keep a thread alive indefinitely; the first genuine completion transition establishes a new idle checkpoint. No cold-load, approval response or refusal bypass is added.

## Exact executable source pins

- connectors/launcher/src/codex/engine.ts: cbc1b95363687bb5280330c9c26a5673678f5183d80d92a3d3a4d1d3c3e29986
- connectors/launcher/test/codex-engine.test.ts: ec92d2fcc63f1892742b647770752c65a96064230619f78689714213ceb3ddb6
- connectors/launcher/test/fake-codex-appserver.ts: 8749cbdd10105dd4cefd5b2b1594acae91c6921c7dbf9f077faeeced003e4ac7

## Current parent-owned gates

| Gate | Actual / bb exit | Result | Raw SHA-256 |
| --- | --- | --- | --- |
| Focused terminal recovery | 0 / 0 | 17 passed; 0 failed/skipped/cancelled | dfeff0aa33a66e19c81793230909accc1fcc9fd60b20fc8dfd8890b7b9330da8 |
| Canonical launcher strict TypeScript and full launcher/hub-client units | 0 / 0 | 2170 passed; 0 failed/skipped/cancelled | 9fbc2eea6959b0a3ec24c4d6af44b783ee50fb445e6f041d29c0d37b8d60f0ba |
| Launcher E2E with real Go hub | 0 / 0 | 44 passed; 0 failed/skipped/cancelled | 613d66bc3a82641ba87738171c72d3cfa5f333b5bb4a73e240667efcd523690c |

All runs: x-boss, isolated cos-pw1 lane and temporary HOME/XDG/CODEX_HOME, canonical physical owned TMPDIR. Focused and strict/full use one job; E2E owns both jobs. Each gate froze 2151 source files before/after, and two exact-owned source/TMP process scans were empty after each gate. Closing documentation changes occur after those gates; executable pins above are unchanged.

Parent drivers: run-fix3b.py fix3b-green, run-fix3b-full.py fix3b-full, run-fix3b-e2e.py fix3b-e2e under .superpowers/sdd/2026-11-09-paused-peer-wake. Canonical strict command is just ln-typecheck (launcher configuration includes hub-client); full units use Node --test --test-concurrency=1 over launcher, launcher/backends and hub-client test files; E2E is just ln-e2e.

## Genuine RED and retained failures

- red: actual 1, bb ['1'], 1 failing top-level cases and 6 passing controls; raw 36010f4667eb835a2a86a3a32106f02a33e55a83203a01806bc058dde1257ae4.
- fix1-red: actual 1, bb ['1'], 2 failing top-level cases and 13 passing controls; raw c674dabdcee647ccf8aff09536c5585d4f9fc8990e2d04395dbadced6a9b5a64.
- fix2-red: actual 1, bb ['1'], 1 failing top-level cases and 15 passing controls; raw 9b1b33fcffef5e525f903553b9daa1bf28042c795b5e0d2950f432883fbaafe1.
- fix3-red: actual 1, bb ['1'], 1 failing top-level cases and 16 passing controls; raw 16423d278fa28f21828a434a41d26a8f472ea760899ffd8c7754872ee0c012fb.
- Previous fix3 canonical strict failed before units with TS2339/TS7006 in the new async-captured test snapshot. Actual/bb exit 1; raw 2ac34844f716b24a1557de16189b7658fa85a1ffd30dc256c40674e8710091a8. Test-only correction ec92 preserves the original snapshot and assertions. This failure is not relabeled as passing.

## Focused regression and control names

- F2 terminal-only completion drains queued wake_idle exactly once after an authoritative idle read
- F2 terminal-only completion does not assume waitingOnApproval is idle
- F2 terminal-only completion does not assume waitingOnUserInput is idle
- F2 terminal-only completion does not assume systemError is idle
- F2 terminal-only completion does not assume notLoaded is idle
- F2 terminal-only completion queued before stop cannot restart delivery after shutdown
- F2 stale terminal-only completion cannot idle a newer running turn
- F2-I1 initial completion read fails recovers queued wake_idle exactly once without a later notice
- F2-I1 gated idle read with duplicates then trailing read fails recovers queued wake_idle exactly once without a later notice
- F2-M1 blockedreadstop closes the RPC and finishes before opening the response gate
- F2-M2 finite burst has at most one completion read in flight and one trailing read
- F2-M2 newer started invalidates a captured idle completion read
- F2-M2 newer waiting invalidates a captured idle completion read
- F2-M2 newer unload invalidates a captured idle completion read
- F2-M2 newer archive invalidates a captured idle completion read
- F2-I3 terminal-only completion of a long active turn starts queued wake_idle exactly once before idle dormancy
- F2-I2 unchanged idle completion polls during pending receipt confirmation cannot renew idle dormancy

## Review and remaining obligations

The same independent GPT-6.1-Sol reviewer accepted the failed-read, idle-age, first-transition and test-typing corrections with no open Critical/Important findings. Fresh whole-component source review passed SpecPASS/QualityPASS with C0/I0/M1. M1 is deferred in issue61: add a launcher-origin terminal-only notLoaded mutation regression; the production allowLoad guard was independently checked as correct. Full merged-tree Go/UI/screenshots/protocol/release gates remain integration obligations; these component gates do not claim them.

F1 definite-rejection retry classification remains a separate slice. The affected peer identity, incident-time status, eligible priority/count and installed connector have not been established for xpp-icant-native. The related owner report only supports four retained deliveries (one hold, three wake_idle), and does not establish the eleven-message case. Keep issue61 open.

No real thread, service installation, authority policy, release candidate, master branch or deployment was changed by this component.
