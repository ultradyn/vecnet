# Native failed-launch cleanup fix2

Source: `a57f804a` on `feat/launcher-codex-live`; native review baseline `3496717f`, supplemental native RPC-identity/prompt source `e3861a09`, ACP integration `4b7ce8ee`. This report records bounded correction and synthetic verification, not final review, live acceptance or rollout.

The remaining I3 acquisition deadlock is corrected by an optional internal `Backend.settleOwned(ref, mode): Promise<void>`. CoS R-L8 approved exactly the shared `backend.ts` contract and `launch.ts` failed-launch dispatch. Only `recoverFailedLaunch` uses it; other backends retain their existing reattach/settle fallback. Native cleanup requires the exact durable id/tag, identified/settled phase, launcher ownership nonce and agreement with every generated reference field before any RPC. It never returns an unverified live handle or resumes a thread.

For a durable zero-input launch, cleanup archives the exact owned id directly. If initial input may have been dispatched, it first reads history and interrupts the active turn. A successful archive is recorded as settled before cleanup resolves or the launcher reports failure/releases capacity. Unavailable or ambiguous operations keep the failure fence, capacity and workspace pending. The effective-policy checks on every input and approval path are unchanged. No foreign thread is loaded or modified.

## Test chronology and evidence

The native test owner first added retained effective-policy mutation and definitive zero-input no-rollout refusal to the real socket fake, plus real `startLaunch`, SQLite restart, `reattachAll`, `retryPending` and sweep regressions. Before production edits, the following focused command failed all five cases (exit 1): both available-cleanup cases stayed nonterminal, both unavailable-then-restored retries stayed unreachable, and the optional cleanup method was absent.

```sh
node --test --test-concurrency=1 --test-name-pattern=fix2 connectors/launcher/test/backends/codex-app-server.test.ts
```

Raw RED: `/tmp/vecnet-native-fix2-red.log`. The direct method test was subsequently aligned with the approved void-return contract (removed a transcript-result assertion); the available-cleanup archive assertion was strengthened to exactly one. The four failing launcher behavior cases were preserved. Production changes followed the observed RED.

Covering GREEN, exit 0, 139/139, no skips:

```sh
node --disable-warning=ExperimentalWarning --test --test-concurrency=1 connectors/launcher/test/backends/codex-app-server.test.ts connectors/launcher/test/launch.test.ts connectors/launcher/test/reattach.test.ts connectors/launcher/test/sweep.test.ts
```

Raw GREEN: `/tmp/vecnet-native-fix2-green.log`. Full TypeScript exit 0, `/tmp/vecnet-native-fix2-tsc.log`:

```sh
cd connectors/launcher
node node_modules/typescript/lib/tsc.js --noEmit -p tsconfig.json
```

Tests assert persisted settlement before the terminal report, released capacity only after settlement, pending workspace retention across eight days/restart/sweep while archive is unavailable, later retry resolution, exactly owned archive, no initial/follow-up input, no approvals/questions, no foreign operations, persisted settlement after another restart, and retention cleanup only after proof. Tampered backend/host/id/tag/workspace/branch and missing provenance reject before RPC. The no-rollout case never synthesizes a first prompt or successful resume.

The earlier supplemental `e3861a09` real-parent-bridge reused-RPC-id regression and branchless-prompt test remain included in covering GREEN. Their separate original RED evidence and chronology remain in `launcher-native-fix1-report.md`; fix2 does not replace those claims.

## Source freeze

SHA-256 at covering GREEN and committed source:

```text
4c20663fd8324522ea95292281844fd88f981fadf529427fe47c97b0eacb51f6  connectors/launcher/src/backend.ts
60520fc03d079c4c980bfe7c73703c22d9656780edd31eacbf971373c05e44b1  connectors/launcher/src/launch.ts
744086b84cb2de46d7e80def887114bf214d5c7df9c048eb90e36532bb46681c  connectors/launcher/src/backends/codex-app-server/index.ts
ea868c6428b6667f7de7771a82e9d8251a05c57fbe064c8bc7eb956678887763  connectors/launcher/test/backends/codex-app-server.test.ts
66da1b7eb52eb84889d9e3b270158757acada82f680d2370d5c456cdb71d421e  connectors/launcher/test/fake-native-codex.ts
```

The sole CX native test worker and main covering/TypeScript commands exited and drained before slot handoff to the same UI owner. Native intake remains inactive/MainPID 0; no provider canary was run for fix2. Historical real Codex 0.166 zero-input no-rollout/archive refusals remain unresolved evidence, not disappearance or a successful cleanup claim. Fresh owned real-runtime acceptance and same Lens review of fix2, its two shared seams and the supplemental e386 correction remain release gates. Frozen automatic-delivery source is untouched; its separate owner reports running. DONE for this bounded source/test/report handoff.
