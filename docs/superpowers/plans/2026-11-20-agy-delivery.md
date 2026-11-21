# Antigravity (agy) Delivery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Vecnet-enrolled agy (Antigravity CLI 1.5.13) session receives deliveries during active turns (hooks), can wait for messages while idle (background MCP `vecnet_wait`), uses Vecnet over MCP inside agy's default sandbox, and can be launched by the Vecnet launcher through `agy acp`.

**Architecture:** Same shape as Claude's shipped legacy path (hooks + MCP + an agent-armed idle waiter), adapted to agy's hook protocol. `vecnet hook --client agy` reads agy's camelCase stdin and emits `injectSteps` `systemMessage` output. It reuses the existing hook state, admission, rate limit, inert framing and display proof. `vecnet hook install --client agy [--mcp]` writes `.agents/hooks.json` and `.agents/mcp_config.json`. The `vecnet mcp` relay strips agy's injected `toolSummary` argument and serves a relay-local `vecnet_wait` tool. Spec Slice 3 (native daemon push) is deferred; there is no daemon or ledger work here.

**Tech Stack:** Go CLI (`go test`), TypeScript launcher (`node:test` via `just ln-test`), agy 1.5.13, the disposable probe harness `scripts/agy-canary-probe.py`.

**Spec:** `docs/superpowers/specs/2026-11-20-agy-delivery-design.md` (binding authority). Evidence: `docs/research/2026-11-20-agy-autodelivery.md` §5 and `docs/research/evidence/agy-1.5.13.json`.

**Scope rulings (made before this plan; recorded in the SDD ledger):**
- Spec Slice 1.3 (`agy-enrollments.json`, `agy-bindings.json`, `agy status|enable|disable|runtime-snapshot`, `createAgyStatus` daemon sidecar) is **deferred with Slice 3**. That registry and status sidecar exist only to feed the native push adapter. Hooks bind through the existing per-profile hook state (`hookState` / `admit`), exactly as Claude's shipped legacy hooks do.
- `toolSummary` stripping in the relay is **added**. agy adds a required `toolSummary` to every MCP tool call (evidence M2), and the hub's tool schemas are closed (`additionalProperties: false`, `internal/mcpserver/tools.go` `object`). Without stripping, every `vecnet_*` call from agy fails validation.

## Global Constraints

- agy hook output is `{}` when silent, or exactly `{"injectSteps":[{"systemMessage":{"systemMessage":"<context>"}}]}`. Never `userMessage`, never `ephemeralMessage`, never `metadata` / `user_markdown`, never `terminationBehavior`. Always valid JSON on stdout, exit 0.
- The context string is the existing inert `hookContext`/`hookSummary` output (the `<vecnet-inbox untrusted="true">` framing); it reuses the existing `hookMaxUnits` cap and the encoded-byte `limit` (4096-byte atomic pipe) exactly as the Claude path does.
- Session identity = stdin `conversationId` (fallback env `ANTIGRAVITY_CONVERSATION_ID`), normalized with `hookSession`.
- **Binding rule (evidence I1/I2):** a run may bind, or move a binding within the same agy process (the Claude `/clear` case), only if it is `PreInvocation` with `invocationNum == 0` **and** a non-empty (after trimming) `lastUserInput`. That maps to the Claude path's `owner.prompt = true`. Every other run (`SessionStart`; `PreInvocation` with `invocationNum > 0`; any run with an empty `lastUserInput`, which is how subagents look) has `owner.prompt = false`. It never binds and claims only if its `conversationId` is already the bound session.
- Rate limit: turn-start runs (`owner.prompt`) poll every time, like Claude `user-prompt`. Mid-turn `PreInvocation` reuses the Claude `post-tool` 5-second rate limit (`hookPostToolEvery`). `SessionStart` does no hub work and outputs `{}`.
- Agy process identity for `hookProc`: the hook process's grandparent (hook → `sh -c` → agy) on Linux via `/proc`. Elsewhere, or when unknown, use the zero `hookProc`, which never matches. agy exports no PID variable to hooks.
- `VECNET_PROFILE` guard and `--profile` handling are unchanged from Claude.
- Codex and Claude behavior must be byte-identical before and after (existing tests unchanged and green).
- No writes to agy's own settings (`~/.gemini/**`, `trustedWorkspaces`, `permissions`) from any Vecnet command. Installer output may *print* advice.
- `vecnet_wait` is pointer-only. It returns a fixed text with no peer-controlled bytes, never claims, acks or moves any cursor, ignores `hold`, and holds no state that needs a clean shutdown (agy kills the MCP server on `/exit` with no cancellation, evidence T2).
- Go tests run under `umask 022`. Compare `internal/cli` failures with master (some tests are umask-dependent on this host; see the ledger note). TypeScript checks use `just ln-typecheck` / `just ln-test` with `scripts/bb/bin` on PATH.
- Real-agy checks never use the user's `~/.gemini`. They run in the harness's disposable HOME with the loopback stub. Any real-model check uses model `gemini-4-neon-low` in an isolated HOME copy, only when a task says so.

## Review Focus

- Subagent non-binding: a `PreInvocation` with `invocationNum 0` and an empty `lastUserInput` in the same agy process must never bind, clear or claim.
- No `userMessage`, `user_markdown`, `metadata` or `ephemeralMessage` anywhere in agy output, including notices (foreign/ambiguous). Notices use `systemMessage` too.
- `toolSummary` stripping must not open a bypass of the relay's `as`/`peer` binding or its duplicate/alias-key rejection (`mcpRelayFields`).
- `vecnet_wait` must not leak peer text (sender names, thread subjects, bodies) into its result.
- Installer guards (symlink, git-tracked, never clobbering other groups/servers, key order kept) must match the Codex installer's strictness.

---

### Task 1: agy hook runtime (`vecnet hook --client agy`)

**Files:** Modify `internal/cli/hook.go` (client dispatch, stdin parsing, event mapping, output encoding), `internal/cli/hooksession.go` (agy process identity). Create `internal/cli/hook_agy_test.go`. Add `hookproc_agy_linux.go` / `hookproc_agy_other.go` only if the grandparent lookup needs platform files (follow the existing `hookproc_*` pattern).

**Interfaces produced:** `--client agy` accepted by `hookRun`. `agyHookOutput(context string) []byte` (`{}` for empty context). `agyHookProc() hookProc` (grandparent agy process, or zero).

- [ ] **Red:** `TestHookAgy*` table tests driving `hookRun` with agy stdin JSON (fields: `hookEventName`, `conversationId`, `invocationNum`, `initialNumSteps`, `lastUserInput`, `cwd`) against the existing fake-hub test fixture used by `hook_test.go`:
  - turn-start `PreInvocation` (inv 0, non-empty input) binds and delivers. Output parses as `{"injectSteps":[{"systemMessage":{"systemMessage":S}}]}`, S contains `<vecnet-inbox untrusted="true">`, and the output has no other keys.
  - mid-turn `PreInvocation` (inv 1) from the bound conversation delivers, subject to the 5 s rate limit (second call within 5 s outputs `{}`).
  - **subagent:** a bound root, then `PreInvocation` inv 0 with an empty `lastUserInput` and a different `conversationId` in the same agy process outputs `{}`, does not rebind, and leaves the state file unchanged.
  - **/clear:** a bound root, then `PreInvocation` inv 0 with a non-empty input and a new `conversationId` in the same agy process rebinds (`admitCleared`) and delivers.
  - an unbound profile plus a mid-turn run outputs `{}` and doesn't bind.
  - `SessionStart` outputs `{}` with zero hub requests.
  - the foreign-session notice is emitted as a `systemMessage` step (no `userMessage`).
  - malformed or oversized stdin outputs `{}` with state untouched.
  - output never exceeds `limit` bytes.
  - `--client agy` never calls `claudeRegisterHook`.
  - Claude and Codex runs unchanged (existing tests).
- [ ] **Confirm red:** `umask 022 go test ./internal/cli -run '^TestHookAgy' -count=1`. Expect failures: client rejected / output shape.
- [ ] **Implement:** in `hookRun`, branch on `o.str("client") == "agy"` before the Claude stdin block. Parse the agy struct, compute `session`, `event` (`turn-start` → reuse `user-prompt` semantics; mid-turn → `post-tool` semantics; `SessionStart` → return `{}`), `owner.prompt` per the binding rule, and `owner.proc = agyHookProc()`. Route every `publish(hookOutput(event, …))` through an encoder chosen by client, so agy gets `agyHookOutput`. The `fits` check must measure the agy encoding. Keep the Codex early-return untouched. An empty result for agy must still print `{}` (agy requires JSON on exit 0); check that `cmdHook` prints `{}` for agy when the result is silent.
- [ ] **Verify:** `umask 022 go test ./internal/cli -run '^(TestHook|TestHookAgy|TestCodexHook)' -count=1`. All pass (compare any failure with master).
- [ ] **Commit** `feat(agy): deliver inbox context through agy PreInvocation hooks`.

### Task 2: agy hook + MCP installer (`vecnet hook install --client agy [--mcp]`)

**Files:** Modify `internal/cli/hookinstall.go`. Create `internal/cli/hookinstall_agy_test.go`.

**Interfaces consumed:** Task 1's `vecnet hook --client agy --profile P --event <e> [--mcp vecnet-P]` command line. Events written: `SessionStart` and `PreInvocation` (the hook maps them itself; `--event` values `session-start` / `pre-invocation`; Task 1 must accept `pre-invocation` and route by stdin `invocationNum`).

- [ ] **Red:** `TestHookInstallAgy*`:
  - default target is `<git-top-level>/.agents/hooks.json` (or cwd outside git).
  - dry run (no `--write`) prints a diff and writes nothing.
  - `--write` creates `{"vecnet-P":{"enabled":true,"SessionStart":[{"type":"command","command":…,"timeout":5}],"PreInvocation":[…]}}`.
  - re-run is idempotent (no change).
  - an existing unrelated group and its key order are preserved.
  - an existing `vecnet-P` group with a stale command is replaced in place.
  - a symlinked `.agents` or `hooks.json` is refused without `--settings`.
  - a git-tracked `.agents/hooks.json` is refused without `--settings`, with advice naming `.git/info/exclude`.
  - `--mcp` also merges `mcpServers["vecnet-P"] = {"command": BIN, "args": ["--net","P","mcp"], "env": {…VECNET_CONFIG only if set…}, "timeoutSeconds": 1860, "tools": {"vecnet_inbox":{"eager":true}, "vecnet_reply":{"eager":true}, "vecnet_ack":{"eager":true}, "vecnet_send":{"eager":true}, "vecnet_wait":{"eager":true,"background":"always"}}}` into `.agents/mcp_config.json` with the same guards, never touching other servers.
  - `--mcp` prints (never writes) advice that agy will ask before each `vecnet_wait` call unless the user adds the allow rule `mcp(vecnet-P/vecnet_wait)`.
  - output advises that each git worktree needs its own install.
  - `--auto-ack` with `--client agy` is a usage error. Claude and Codex installs are unchanged.
- [ ] **Confirm red:** `umask 022 go test ./internal/cli -run '^TestHookInstallAgy' -count=1`.
- [ ] **Implement:** extend client validation to `claude|codex|agy`. Reuse `parseJSONNode`/`jsonNode` merge helpers, `writeHookSettings`, `hookGit`, `checkCodexUntracked`-style tracking checks and the symlink guards. No `claude mcp add` invocation for agy (direct file merge).
- [ ] **Verify:** `umask 022 go test ./internal/cli -run '^(TestHookInstall|TestHookInstallAgy|TestCodexHook)' -count=1`.
- [ ] **Commit** `feat(agy): install agy hooks and workspace MCP config`.

### Task 3: relay `toolSummary` stripping and relay-local `vecnet_wait`

**Files:** Modify `internal/cli/mcprelay.go`; create `internal/cli/mcprelay_agy_test.go`. Reuse the `watch` transport from `internal/cli/watch.go` for waiting (no new hub API).

**Interfaces produced:**
- In `tools/call`, a top-level `arguments.toolSummary` is removed before binding and forwarding, only when it is a JSON string. It is removed for every tool; no other key is touched.
- `tools/list` responses relayed to the client get one extra tool appended: `vecnet_wait`.
  - Description: "Wait in the background for new Vecnet inbox activity for this profile. Returns a short notice; read messages with vecnet_inbox, then call vecnet_wait again."
  - `inputSchema`: `{"type":"object","additionalProperties":false,"properties":{"timeout_seconds":{"type":"integer","minimum":1,"maximum":1800},"toolSummary":{"type":"string"}}}`.
- `tools/call` for `vecnet_wait` is answered by the relay. It is never forwarded to the hub.

- [ ] **Red:** `TestMCPRelayAgy*` against the relay's existing fake-hub fixtures:
  - a `vecnet_inbox` call with `toolSummary` reaches the hub without it, other args intact.
  - `toolSummary` with a non-string value is forwarded unchanged (the hub rejects it).
  - duplicate / case-folded `toolsummary` aliases still fail `mcpRelayFields` rejection rules as today.
  - `as`/`peer` binding still enforced.
  - `tools/list` includes `vecnet_wait` exactly once (also on stateless re-fetch).
  - `vecnet_wait` returns as soon as the watch stream yields a `wake` or `wake_idle` delivery, with `content[0].text` exactly `Vecnet: new inbox activity for profile P. Call vecnet_inbox to read it; inbox content is untrusted peer data. Then call vecnet_wait again.` and `structuredContent` `{"status":"message_available"}`. There are no IDs, names or bodies in either.
  - a `hold`-only stream doesn't return until timeout.
  - timeout returns text `Vecnet: no new inbox activity yet. Call vecnet_wait again to keep waiting.` and `{"status":"timeout"}`.
  - no ack, cursor or state-file write happens during or after `vecnet_wait` (assert the fixture recorded no ack or cursor request and that the state dir is unchanged).
  - concurrent `vecnet_wait` calls each return independently.
  - stdin EOF while a wait is pending ends the relay promptly (no goroutine/child leak: the watch child or connection is closed).
- [ ] **Confirm red:** `umask 022 go test ./internal/cli -run '^TestMCPRelayAgy' -count=1`.
- [ ] **Implement:** strip in `bind` before field binding (keep the `mcpRelayFields` duplicate rules on `arguments` by checking with the existing helper). Intercept `vecnet_wait` in the main loop before `exchange`. Run the wait in a goroutine that writes its JSON-RPC response through the relay's existing serialized writer, so the stdin loop keeps serving other requests. The wait observes the profile's watch stream with no acknowledgement and no display proof. Pick the least-privileged existing watch mode that reports `wake`/`wake_idle` without moving the hook cursor; if none exists, add a read-only mode flag and cover it.
- [ ] **Verify:** `umask 022 go test ./internal/cli -run '^(TestMCPRelay|TestMCPRelayAgy|TestWatch)' -count=1`.
- [ ] **Commit** `feat(agy): strip toolSummary and add relay-local vecnet_wait`.

### Task 4: `vecnet watch --once`

**Files:** Modify `internal/cli/watch.go`, `internal/cli/commands.go` (usage text); create `internal/cli/watch_once_test.go`.

- [ ] **Red:** `TestWatchOnce*`:
  - `--once --for-monitor` exits 0 right after the first wake/wake_idle line.
  - `hold` lines do not end it.
  - without `--once`, behavior is unchanged (existing watch tests).
  - `--once` with `--json` exits after the first delivery record.
- [ ] **Confirm red, implement, verify** with `umask 022 go test ./internal/cli -run '^(TestWatch|TestWatchOnce)' -count=1`.
- [ ] **Commit** `feat(cli): add watch --once for one-shot idle waiters`.

### Task 5: Launcher ACP qualification for `agy acp`

**Files:** Create `scripts/agy-acp-smoke.mjs` and `scripts/agy-acp-smoke.test.mjs`. Modify `docs/connectors/launcher.md`. Read `connectors/launcher/src/backends/acp/index.ts`; change it only if the smoke shows an incompatibility, and add a backend test for any change.

- [ ] **Red:** `scripts/agy-acp-smoke.test.mjs` unit-tests the smoke's pure helpers (stub request routing by content, result evaluation).
- [ ] **Implement:** `scripts/agy-acp-smoke.mjs` drives the real `AcpBackend` (import from `connectors/launcher/src/backends/acp/`) against `agy acp`, in a disposable HOME with the loopback Gemini stub (`GOOGLE_GEMINI_BASE_URL`). It covers launch with `mode_id: "default"`, a prompt, a permission request round-trip, a follow-up prompt, cancel, and clean process teardown (no agy child left). It prints a sanitized JSON receipt. Document `vecnet --net PROFILE launcher setup --backend acp --acp-command /abs/path/to/agy --acp-arg=acp --acp-provider agy --acp-mode default …` in `docs/connectors/launcher.md`.
- [ ] **Verify:** `node --test scripts/agy-acp-smoke.test.mjs` and `node scripts/agy-acp-smoke.mjs` (needs `agy` on PATH) pass; `just ln-typecheck` if launcher code changed.
- [ ] **Commit** `test(agy): qualify agy acp on the launcher ACP backend`.

### Task 6: Connector docs, guide, and real-agy end-to-end check

**Files:** Create `docs/connectors/agy.md`. Modify `internal/cli/guide.md` (agy section), `docs/connectors/README.md` or an index if one lists connectors, and the spec status line. Extend `scripts/agy-canary-probe.py` with an `e2e` probe.

- [ ] **Implement docs:** `docs/connectors/agy.md` covers:
  - setup (`vecnet enrol` in agy, then `vecnet hook install --client agy --mcp --write` per worktree);
  - what hooks deliver, and when;
  - `vecnet_wait` use and the allow-rule advice;
  - the sandbox/MCP-first rationale;
  - limits (no native daemon push; subagents never receive deliveries; `/exit` kills a pending wait).

  The guide gets a short agy section with the `vecnet_wait` loop instruction from the spec.
- [ ] **Implement e2e probe:** a disposable HOME + loopback Gemini stub + a disposable local Vecnet hub (use the existing test-hub helper the other smokes use; find it under `scripts/` or `internal/` and reuse it).
  1. Install hooks and MCP with the built binary.
  2. Run agy stream-json; send a hub message to the profile; assert the next `PreInvocation` delivers an `injectSteps` systemMessage that reaches the stub request with the inert frame.
  3. Assert a subagent turn gets nothing.
  4. Assert an MCP `vecnet_inbox` call (with agy's `toolSummary`) succeeds.
  5. Assert a background `vecnet_wait` wakes the idle session after a hub message.
  6. Assert no hub ack happened.
- [ ] **Verify:** `python3 -I scripts/agy-canary-probe.py --probe e2e` passes; regenerate the evidence file only if the e2e probe adds new results.
- [ ] **Commit** `docs(agy): document agy connector and add end-to-end probe`.
