# Native launcher review fix1

Source baseline: `f09fad6bd959c47c5e035c61c3d7f1634cd8d65c` (review ancestry `a5e09a98506cbe373310d7921ba70a50c46b6171`). Fixed source: `af41a4f7` on `feat/launcher-codex-live`. The preceding ACP merge adds separate backend files only. This report does not claim final integration, live resume acceptance, rollout or release.

## Corrections

- C1: native file approval retains the authoritative grant root, advertised session scope, and matching item/turn file-change paths including move destinations. Unknown/malformed paths are refused. Managed-network context retains enforced kind/host/protocol; command text remains reported. Unrepresentable additional permission grants are refused. Real canonicalization, policy routing and parent bridge regressions assert the operator addressee and exact native response.
- C2: persist a random per-start ownership nonce before dispatch and supply native threadSource. Scan and validate every listing page before identity persistence or naming. Require a single global same-workspace candidate with the matching nonce; legacy no-id intent, foreign provenance, malformed/repeated paging, invisible outcome and multiple candidates remain unresolved.
- I2: durable allocated/start-dispatched/identified/settled phases distinguish positive pre-start absence from ambiguous native acceptance. Unknown starts throw through restart/retry/sweep and cannot lose their workspace. Proven pre-dispatch Git allocation failures remain reclaimable. Integration tests exercise actual startLaunch, reattachAll, retryPending and sweep.
- I3: start and every resume validate effective sandbox, reviewer, approval policy, canonical cwd and runtime workspace roots before instructions. Ask/auto writes remain within the job workspace, network remains disabled and implicit temporary writable roots are excluded. Ordinary turn and resume requests omit overrides. Replayed permissions while unverified decline. Existing handles revalidate after a failed policy check; an exact previously owned handle can interrupt/archive after a reconnect refusal without sending input.
- I5: project scratch is optional explicit boolean metadata. Absent/false preserves ordinary T3/Paseo/native repository aliases, including temporary; only true authorizes temp. Setup generates a distinct scratch alias on collision. Shared config/resolver/policy/presence and UI consume the same contract, handed to ACP before integration.
- I6: temp allocation canonicalizes its persistent parent and rejects /tmp descendants, including symlinks, before per-job allocation or native start. Worktree test overrides remain supported.
- I7: the fake no longer deduplicates clientUserMessageId. Separate accepted starts produce separate turns, so backend outgoing-call and uncertain receipt tests detect duplicate dispatch.

## Chronology and raw evidence

Production changes preceded execution of the new native regressions while the sole test slot was occupied. This was a lapse against the requested test-first order. The preserved exact pre-fix native source plus current shared contracts was later executed for concrete behavioral sensitivity; it is retrospective RED, not original TDD. No original test-first claim is made.

Commands (absolute Node on x-boss: `/home/xertrov/.local/opt/node-v22.23.3-linux-x64/bin/node`):

```sh
node --import /tmp/vecnet-native-red-xlF2sp/baseline-loader.mjs --test --test-concurrency=1 connectors/launcher/test/backends/codex-app-server.test.ts
node --test --test-concurrency=1 connectors/launcher/test/backends/codex-app-server.test.ts
(cd connectors/launcher && node node_modules/typescript/lib/tsc.js --noEmit -p tsconfig.json)
node --disable-warning=ExperimentalWarning --test --test-concurrency=1 connectors/launcher/test/config.test.ts connectors/launcher/test/policy.test.ts connectors/launcher/test/presence.test.ts connectors/launcher/test/daemon.test.ts
```

The four baseline native files were byte-compared against `git show f09fad6b:<path>` and all matched. Baseline index SHA-256: `d3453ac4ec48f0c74e0d124e690a0094379e5bc04804030d5da3d871f268e74e`.

One worker, sequential Node concurrency 1:

- Frozen baseline loader: `/tmp/vecnet-native-red-xlF2sp/`. `red-final.log`: exit 1, 29 passed / 75 failed / 104 total. The loader substitutes only the pre-fix native source; it does not swap production files.
- Current native `green-final.log` in the same directory: exit 0, 104/104 passed.
- `typescript.log` in that directory: full no-emit TypeScript exit 0.
- Shared config/policy/presence/daemon: `/tmp/vecnet-native-fix1-shared-green.log`, exit 0, 1,251/1,251 passed.

The native test worker explicitly reported no test children and released its slot. These gates preceded the later ACP integration checks.

## Real daemon policy-shape evidence

Actual supported Codex 0.166.0 on x-boss accepted four new zero-input threads, one per mode, returning the exact threadSource nonce, cwd, effective reviewer/approval/sandbox and runtime roots. The per-thread sandbox_workspace_write config correctly excluded /tmp/TMPDIR writes and network access. All four exact threads were archived; zero prompts, turns or tools were dispatched. Private receipt: `~/.local/state/vecnet-integration/native-policy-fix1/probe-pxl5k4/receipt.json`.

An earlier separate zero-input resume probe received a definitive no-rollout error before any prompt. Its exact id was unsubscribed (notLoaded), and intent/ownership/cleanup metadata was retained in probe-HQ5igC. This is not model or resume acceptance evidence. A fresh real model/restart canary remains required after scoped review.

Owned launcher intake remains stopped with no active jobs; separate automatic delivery remains running. No foreign thread or service was signalled. CoS owns independent scoped re-review and final rollout.

## Tested source hashes

- `df9e3d2885f5985250a8151daad89bc5a4c8e6595ce77fc7bdbe6cdda1156aaa` `connectors/launcher/src/backends/codex-app-server/index.ts`
- `1591fadceb9ed58805a15dd22a4e6220725af79a6b1f0f169c3a2871120e3494` `connectors/launcher/test/backends/codex-app-server.test.ts`
- `3563579a8a97937c2d9b7ba838539c51c2cf2973d5213a774f8913975d071349` `connectors/launcher/test/fake-native-codex.ts`
- `448f9192c818f819f3a48c5831b75ee8f7b804dbdd5d7d5e8694cfc3507a5b15` `connectors/launcher/src/config.ts`
- `4b56c5738c2a0aea85a192093c6dc210b8f67b6fc754f4281523bead865a96db` `connectors/launcher/src/policy.ts`
- `c992be8900349db1b4343b25e768429d8b80e60df6deb5f54c9b371519b102a1` `connectors/launcher/src/presence.ts`

## Native RPC identity and temp-prompt follow-up

After the initial fixed snapshot, source inspection found the native adapter also used an unbound wire RPC id as the parent bridge request identity. A new real-parent-bridge regression reused a completed id for a changed file grant root: the earlier requester acceptance incorrectly bypassed a new operator card. `/tmp/vecnet-native-rpc-id-red.log` records the intended assertion failure (0/1 pass), before production changes. Native bridge identity now combines the wire id with a SHA-256 of the complete method and normalized permission draft, including authoritative paths, raw scope and grant options. Exact wire response ids remain unchanged. Changed operations cannot inherit the old bridge grant. No live exploit was observed.

ACP's branchless temp workspace also exposed a shared prompt mismatch. `/tmp/vecnet-temp-prompt-red.log` records the intended failing real buildLaunchSpec assertion before production change. Explicit branch instructions now apply only to worktrees; project-root and temp preserve the decision footer and no-push instruction. Native temp still initializes its owned Git branch independently.

Follow-up source: `e3861a09378ec09de4d6e5980270cca8122d0e5c`. Both new regressions plus existing native/launch tests: `/tmp/vecnet-native-rpc-id-prompt-green.log`, exit 0, 115/115 pass; full TypeScript `/tmp/vecnet-native-rpc-id-prompt-tsc.log`, exit 0. Commands use the same absolute Node, --test-concurrency=1 and the two test paths `connectors/launcher/test/backends/codex-app-server.test.ts` and `connectors/launcher/test/launch.test.ts`; RED used the respective test-name patterns `reused RPC id` and `instructs a branch only`. Native backend tested SHA-256: `05bfe7542e8cf6a01e4600ec62b66d4b7fe52761b49b83d64c19f4644bee485b`. Main worker drained after these sequential checks. Fresh model/intake remains held until scoped review.
