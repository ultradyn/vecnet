# Native fix3 — LAST / DONE

Plan-Task: launcher-native-fix3. Source commit `4e62fc5e`; prior native fix2 `a57f804a`, supplemental `e3861a09`, combined ACP fixture merge `68d03684`. CoS R-L10 approved the additional shared parent-bridge canonical cache boundary and narrowly classified restart recovery. This report follows the tested source; independent review and actual owned-runtime acceptance remain required. No live exploit or live fix3 canary is claimed.

## Corrections

R-S1: requestPermission obtains the complete unredacted canonical permission once for routing and cache identity. Published copies are derived from that resolved operation. The new v2 key uses full SHA-256 over the same complete canonical permission, request identity, raw option semantics, expiry option, full route and effective addressee/operator/relay authority. No published 6 KiB/redacted copy or truncated digest determines authorization reuse. Prior request-id-only entries are ignored, without migrating their authority. Stable canonical replay keeps its original decision; changed canonical scope, method, scope or options requires another decision. Wire response ids and backend option mapping remain unchanged. `contract/permission.ts` is unchanged and no normalizer was duplicated.

R-F2: native attachment retains original error types and adds an internal symbol proof only for a typed effective-policy refusal or an unambiguous -32600 error exactly naming the owned thread as having no rollout. Before adding proof, native rereads durable ownership and requires identified provenance, full reference agreement, and no initial-sent/sending flag. Shared reattach consumes this proof only for a native job still launching/nonterminal and matching the recorded reference. It writes the failure fence and routes through recoverFailedLaunch. That remains the only dispatcher of optional settleOwned. Unknown RPC/transport errors, other backends and ordinary running jobs keep their previous pending behavior. The native input/approval policy fence is unchanged; no first prompt or unverified live handle is synthesized.

## Test-first evidence

Before any fix3 production edit, the same native test owner added 11 real socket/SQLite/parent-bridge cases. The RED command ran at native source from `68d03684` (later intervening commits were documentation only):

```sh
node --test --test-concurrency=1 --test-name-pattern=fix3 connectors/launcher/test/backends/codex-app-server.test.ts
```

Actual Node exit **1**, **2 pass / 9 fail / 11 total**, no skips. Five intended cache regressions failed (unchanged wire symlink retarget, legacy-only cache replay, option meaning, method and scope); four crash-before-catch/fence recovery matrix cases failed. Both transient controls passed, retaining the open job before later adoption. Native/bridge/recovery production hashes before and after RED were identical. The readonly canonicalizer hash was also unchanged.

RED log `/tmp/vecnet-native-fix3-red.log`; original actual-exit file, receipt and checksum inventory: `/tmp/vecnet-native-fix3-red.exit`, `.receipt.json`, `.sha256`. The receipt records command, cwd, start/end, source/test hashes before/after, actual exit and parsed counts. The test/fake bytes are identical at RED and covering GREEN. No production edit preceded observed behavioral RED.

The critical regression keeps every native wire field unchanged, including method, completed RPC id, item id and grantRoot. It first accepts the workspace symlink's ordinary target as requester, proves stable canonical replay, then retargets only that symlink into the fixture home's .ssh. It requires a distinct operator card and no third response before the operator answer. The legacy-only cache control seeds only the original unscoped key and requires a fresh card.

Crash tests bypass startLaunch's catch entirely: direct native launch durably identifies the thread, but no shared failure fence exists before restart. Retained broader policy and definitive no-rollout each cover available and persistently unavailable archive, SQLite restart, retryPending, eight-day retention and sweep. They assert settlement saved before failure/capacity release, no input/approval/question, no foreign operations, exact archive, pending resources while unavailable and cleanup only after proof. Transport-drop and unrelated RPC controls remain pending and later adopt the same thread.

## Covering GREEN and actual command exits

One sequential CX executable slot, no parallel build/test job. Covering Node command:

```sh
node --disable-warning=ExperimentalWarning --test --test-concurrency=1 connectors/launcher/test/backends/codex-app-server.test.ts connectors/launcher/test/launch.test.ts connectors/launcher/test/reattach.test.ts connectors/launcher/test/sweep.test.ts connectors/launcher/test/parent-bridge.test.ts
```

Actual exit **0**, **192/192 pass**, no skips, elapsed 2.333 seconds. Raw `/tmp/vecnet-native-fix3-green.log`; explicit subprocess-exit receipt `/tmp/vecnet-native-fix3-green.receipt.json`.

Full strict configured TypeScript:

```sh
cd connectors/launcher
node node_modules/typescript/lib/tsc.js --noEmit -p tsconfig.json
```

Actual exit **0**, elapsed 0.331 seconds. `/tmp/vecnet-native-fix3-tsc.log` is correctly empty on success; the separate `/tmp/vecnet-native-fix3-tsc.receipt.json` records the command, cwd and actual return code. Empty output alone is not the exit proof. Python capture wrappers re-emitted subprocess status; no bb wrapper was used, so no bb exit is claimed.

## Source and raw hashes

Captured after covering GREEN and verified against committed source; no production edits occurred between validation and commit:

```text
542a410f1e2d0c04dee41dccf9fee30c291828e6974e83381bbfaf67e73a4bf9  connectors/launcher/src/backend.ts
816fbb0497d80acbbf4ace1c11b015fde886f6ff941a9a4f5b95c8bc81fef936  connectors/launcher/src/parent-bridge.ts
9fce9c91ea2a04cce171125a9aba4129943785ad965b784a05ae9ce332c23d63  connectors/launcher/src/reattach.ts
188d66ce2c0cf2def1e61e655cda059795c4ede97280a96ec13207d39287f097  connectors/launcher/src/backends/codex-app-server/index.ts
023cb78de60cd1e08a376008ac89ac4f38b45a5418559565943da635494a5a7d  connectors/launcher/test/backends/codex-app-server.test.ts
52902aab19cf9988f78962a1160211363a05e24e4af8924ef4a4293729db95c4  connectors/launcher/test/fake-native-codex.ts
c200bb6e3f23aab48502b53fb0b4de5b0df3f1a6f5300ddaec859cec37dffcb4  connectors/launcher/src/contract/permission.ts (unchanged)
```

```text
877c41eef277d8e492d17239b68e0b5ff985c8a9b3ba8562bffdb28818525945  red.log
af4ef073d339d2ee75c1c81e006dd7b0b09ded27ecde0634ac9955555ec7a76f  red.receipt.json
e42c6e56fc56b96fe9ff3c0c9e056e046498815989d413553a12164c3e333b1b  green.log
d89e07cba7b7ab2ef2269b3c382b33b2e6c55c287f681ea6e760290dd04c7432  green.receipt.json
e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855  tsc.log
40632ca76b013efc7cdbf4ebb5ce8cea198a54ac2e154c898cb45cc70905c3b3  tsc.receipt.json
```

## STOP / drain / remaining gates

Native test owner is closed/STOPPED after RED; main covering Node and TypeScript exited and drained. No provider, service, credential, schema, SQL, dependency, policy or ACP production change was made in fix3. All three local source-owner contexts are closed. The sole CX executable slot returns to main for combined-source gates after this immutable handoff. Native intake remains stopped; local model and disposable Fly canaries remain held pending scoped/harness PASS. Actual xsm activation still requires its separate direct user approval. Whole-branch review, final Node/TS/npm source candidate, fresh owned live acceptance, final merged-source gates and CoS release remain owed. DONE for bounded native fix3 source/test/report.
