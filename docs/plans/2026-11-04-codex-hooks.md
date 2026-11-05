# Codex Hooks Implementation Plan

> **For agentic workers:** executed by `docs/plans/vecnet-core-build.workflow.mjs` with `docs/plans/codex-hooks.workflow-args.json` (written by the coordinator from this plan). Each task runs in its own worktree from the dependency map and sees only its own section plus the Global Constraints. Steps use checkbox (`- [ ]`) syntax.

**Goal:** A Codex CLI session receives vecnet deliveries in context at its safe hook points, through the same `vecnet hook` that serves Claude Code. Codex's hook protocol is the same as the one `hookRun` already speaks: stdin `hook_event_name`, stdout `{"hookSpecificOutput":{"hookEventName","additionalContext"}}`, and `hooks.json` nesting identical to Claude settings. So the work is small: add `SessionStart`, add an installer target, write a connector doc, and run an opt-in live smoke test.

**Architecture:**
- **Hook (Task 1):** `vecnet hook` gains the event `session-start` (stdin `SessionStart`). Once admitted it uses the prompt poll path, with output named `SessionStart`. It cannot automatically bind a fresh session; that waits for `UserPromptSubmit` or an explicit rebind window. Other stdin events stay silent without an explicit --event; preserve the existing explicit-event override. Nothing else in `hook.go` changes: the wrapper, `hookTrust`, caps, pipe write, identity binding (issue 33) and `--net` hints are reused byte for byte.
- **Installer (Task 2):** `hook install --client codex` writes `SessionStart`, `UserPromptSubmit` and `PostToolUse` handlers into `<git top-level>/.codex/hooks.json` through the existing order-preserving merge. It refuses that default when git tracks the file. It prints the `/hooks` trust step and never touches Codex trust state.
- **Docs (Task 3):** `docs/connectors/codex.md`, plus Codex bullets in PROTOCOL §13.9. No new section.
- **Smoke (Task 4):** `just codex-smoke` runs one canary through a real `codex exec` against a demo hub with private state. It is opt-in and coordinator-run, and skips cleanly without `codex` or a prepared Codex home.

**Tech Stack:** Go 1.26 (`CGO_ENABLED=0`), stdlib only; Node ESM for the smoke (reusing `scripts/ui/lib.mjs` `bootDemo`).

**Spec:**
- `.scratch/vecnet/codex-review/bundle-review.md`, the review of codex-llmp's bundle (`plan/codex-integration-production` @ 349f475). This plan is that review's §6 replacement, adopted by the coordinator. The bundle's binding, reservation, resync and bound-call machinery and its managed app-server tier are out of scope; the app-server notes go to the launcher's P4 `codex-app-server` plugin.
- Codex hooks, <https://learn.chatgpt.com/docs/hooks> (read 2026-11-04):
  - **Context events:** `SessionStart`, `UserPromptSubmit`, `PostToolUse`, `PreToolUse` and `SubagentStart` may return `additionalContext`, with output `{"hookSpecificOutput":{"hookEventName","additionalContext"}}`.
  - **Context is developer-role:** plain stdout on `SessionStart` and `UserPromptSubmit` "is added as developer context".
  - **Stdin:** carries `session_id`, `transcript_path`, `cwd`, `hook_event_name`, `model`, plus `turn_id`, `source` (`startup|resume|clear|compact|fork`), `prompt`, and `tool_input`/`tool_response`.
  - **Subagents:** "Subagent hooks use the parent session id". `agent_id` is sent only on `SubagentStart`/`SubagentStop` and model-request hooks.
  - **Spill threshold:** `additionalContextLimit` defaults to about 2,500 tokens. Above it, Codex saves the full text under `<temp_dir>/hook_outputs/<session_id>/<uuid>.txt` and shows a head-and-tail preview. A value of `0` passes the full text.
  - **Timeouts:** `timeout` defaults to 600 s for most hooks. `SessionEnd`/`Interrupt` default to 1 s, at most 3 s.
  - **Lifecycle output:** `Stop`, `SubagentStop` and `Interrupt` require JSON on exit 0. Exit 2 blocks or continues on several events.
  - **Matcher:** a regex. `"*"`, `""` or omitted matches all. It applies to `tool_name` (PostToolUse) and `source` (SessionStart) and is ignored for UserPromptSubmit.
  - **Config locations:** `~/.codex/hooks.json`, `<repo>/.codex/hooks.json` and the `config.toml` equivalents.
  - **Trust:** project hooks load only when the project `.codex/` layer is trusted. Non-managed hooks are trusted by hash in `/hooks` (Settings > Hooks in the app), and new or changed hooks are skipped until trusted. `--dangerously-bypass-hook-trust` skips trust for one invocation.
- Codex app-server, <https://learn.chatgpt.com/docs/app-server>: background only. Out of scope here.
- PROTOCOL §2.3, §2.10, §13.7, §13.9 and §14. `docs/connectors/claude-code.md`.

**Starts from:** verified `master` fcc71a1 (2026-11-05), including MCP-first 0f273d0, identity binding (issue 33), session binding and the `agent_id` skip (issues 38/39), qualified `--net` hints and graceful shutdown. Rebase and recheck these contracts before implementation.
- **Preflight (coordinator):** inspect `hookRun`, `newHookHintMCP`, `hookOwner`/`hookState.admit`, `mergeHookSettings` and `hookCommand`; the current signatures below include MCP-first's `mcp string`. Add `.scratch/vecnet/build-codex-hooks/` to `.gitignore`.
- **Rebase points:** `internal/cli/commands.go` `usageText`, `CHANGELOG.md`, PROTOCOL §13.7/§13.9, and `justfile`. Launcher and MCP-first changes are already merged: preserve them. Loop-judge still plans §19; this plan allocates no PROTOCOL section and no migration.

## Integration notes

Read the in-flight [hub preferences](2026-11-05-hub-preferences.md), [DB compaction](2026-11-05-db-compaction.md), [R2 blobs](2026-11-05-r2-blobs.md) and [admin parity](2026-11-05-admin-parity.md) plans before rebasing. All can overlap `commands.go`, protocol/help/changelog and `justfile`; prefs/R2 also share `scripts/ui/lib.mjs` with the smoke. Preserve merged harness helpers and CLI's SQLite-free dependency graph. No hub admin operation is added here: hook install/rebind are local host operations, not remote admin APIs. Do not add an MCP tool that writes another host's hook/trust files. The existing Claude `--mcp` installer lane remains intact; Codex MCP registration is outside these four tasks.

## Dependency map

```json
{"1":[],"2":[1],"3":[1,2],"4":[1,2]}
```

**Build settings:**
- `taskPrefix: "cx"`, worktree `.worktrees/codex-hooks`, branch `codex-hooks` off `master`, status files in `.scratch/vecnet/build-codex-hooks/`.
- Suggested routes: Tasks 1 and 2 (Go) → Sol; Task 3 (docs) → Opus; Task 4 (Node) → Opus.
- **Shared files:**
  - `internal/cli/commands.go` (`usageText`): 1 → 2.
  - `internal/cli/hook*_test.go` helpers: 1 → 2.
  - Tasks 3 and 4 share no file. Task 3 names the recipe `just codex-smoke` that Task 4 creates.

## Global Constraints

- **Never block or fail a Codex turn.** After dispatch, `vecnet hook` exits 0 on every path and its stdout is empty or one JSON object plus `\n` (PROTOCOL §13.9 "Never block a turn"). It never emits `decision`, `continue`, a permission verdict, or exit 2.
- **No lifecycle handlers.** Nothing installs or handles `Stop`, `SubagentStop`, `Interrupt` or `SessionEnd`: Codex requires JSON from the first three and caps the last at 3 s. `SubagentStart` and `PreToolUse` are not installed either.
- **Explicit timeout.** Every installed Codex handler sets `"timeout": 5` (seconds), because Codex otherwise waits 600 s. The internal budget stays `hookBudget = 1500 * time.Millisecond`.
- **No spill to disk.** Every installed Codex context handler sets `"additionalContextLimit": hookCodexContextLimit`, where `hookCodexContextLimit = 3 * hookMaxUnits` (18000). An `additionalContext` is at most `hookMaxUnits = 6000` UTF-16 units. That is at most 18000 UTF-8 bytes, and a token covers at least one byte, so the output stays under the threshold and Codex never writes peer text to `<temp_dir>/hook_outputs/`. On a Linux pipe the output is also capped at `hookPipeAtomicBytes = 4096` bytes.
- **Developer-role context.** In Codex this channel is developer context, so the trust line `hookTrust` and the inert wrapper (`inert`, `<vecnet-inbox untrusted="true">`) stay byte-identical, and `testdata/hook-hostile.golden` does not change. Peer strings never become commands, paths or recipients.
- **Shared core unchanged.** No edits to `hookState`, `hookBinding`, `hookOwner`, `admit`, `registerSession`, `bind`, `resolveIdentity`, `newHookHint`, `hookContext`, `hookSummary`, `statePath`, `stateFile`, the purge recovery, caps, rate rule or publish-before-commit order. Keep the early agent_id skip, VECNET_PROFILE filter and `owner.prompt: event == "user-prompt"`. Existing MCP-bound and unbound hints, including --net/--as/config qualification, stay unchanged. No Codex-specific state, binding, poller, hint format, hub endpoint, migration or PROTOCOL section.
- **One agent per profile (B1), using existing admission.** Preserve the silent early skip whenever stdin has agent_id. Codex tool hooks that omit it and share the parent session_id cannot be distinguished from the parent; an already-bound session can claim there. Codex has no CLAUDE_PID/CLAUDE_CODE_SESSION_ID guarantee: use stdin identity, do not infer process takeover, and use local hook rebind when changing sessions. SessionStart never enables automatic binding. A subagent claim remains unacked and readable through `vecnet --net P inbox`. Tasks 3/4 document and observe this; no new detection machinery.
- **Acks stay explicit.** Publishing to stdout is never a read, processed receipt or ack. `--auto-ack` remains the same opt-in as for Claude, and the installer never adds it by default.
- **The installer never edits trust or VCS state.** It does not write Codex trust hashes, use or suggest `--dangerously-bypass-hook-trust` outside the disposable smoke, or edit `.gitignore` or `.git/info/exclude`.
- **Tests and builds:** use `BB_HOST=x-tuf BB_JOBS=2 just bb` for every build/test (docs/BUILDBOX.md), never build on xsm; CGO_ENABLED=0 and two workers per task, the full gate in Task 3, and never a live `~/.codex` or `~/.config/vecnet`. A test marked **(guard)** passes before its task and must keep passing; every other listed test is proposed and fails first. Each task commit carries its two-digit `Plan-Task: NN` trailer (01–04).

## Review Focus

1. **Never block a turn on any Codex event.** The three supported and eleven silent Codex event names listed in Task 1, Codex-shaped stdin and a 900 KiB `tool_response` exit 0. Each gives empty stdout or one valid JSON object, with no hub call for non-context events. Pinned by Task 1's `TestHookCodexEventsSilent` and `TestHookCodexStdinShapes`, and the guard `TestHookNeverFailsATurn`.
2. **Peer text stays inert in developer context.** The output is byte-identical across `session-start`, `user-prompt` and `post-tool` except `hookEventName`, and the hostile golden is unchanged. Pinned by Task 1's `TestHookSessionStartSameContext` and the guard `TestHookHostileIsInert`.
3. **No spill and no hang.** Each installed handler has `timeout: 5` and `additionalContextLimit: 18000`, the limit covers the caps, and a maximal item under `SessionStart` fits 4096 bytes. Pinned by Task 2's `TestHookInstallCodexPrintsStanza` and `TestCodexContextLimitCoversCaps`, and Task 1's `TestHookSessionStartMaxItemFits`.
4. **Nothing beyond the three context events is installed.** There are no `Stop`/`Interrupt`/`SessionEnd`/`SubagentStart`/`PreToolUse` keys. Pinned by Task 2's `TestHookInstallCodexPrintsStanza`.
5. **No personal profile leaks into a shared file.** A git-tracked default `.codex/hooks.json` is refused byte-identical, explicit `--settings` writes it, a symlinked `.codex` is refused, and foreign hooks survive in order. Pinned by Task 2's `TestHookInstallCodexRefusesTrackedDefault`, `TestHookInstallCodexRefusesSymlinkDefault` and `TestHookInstallCodexPreservesForeign`.
6. **Claude is unchanged.** Claude stanza bytes, targets and messages are identical. Pinned by the guard tests `TestHookInstallPrintsStanza`, `TestHookInstallDefaultsToLocalAtGitRoot`, `TestHookInstallDefaultRefusesSymlinkRedirect` and `TestHookEventFromStdin`.
7. **No auto-ack in the live run.** After the canary is shown in Codex context, it is still listed by `vecnet --net smoke inbox`. Pinned by Task 4's smoke assertion.

---

### Task 1: `vecnet hook` accepts `SessionStart`

**Depends:** none

**Files:**
- Modify `internal/cli/hook.go`: only the stdin switch in hookRun, the event check and hookOutput name mapping; leave owner.prompt unchanged.
- Modify `internal/cli/commands.go`: only the usageText hook lines, retaining the current runtime --mcp hint flag and install help.
- Modify `internal/cli/hook_test.go`, `internal/cli/hooksession_test.go` and `internal/cli/hook_e2e_test.go` by adding tests.

**Interfaces:**
- **Consumes, unchanged, from master fcc71a1:**
  - `func (a *app) hookRun(args []string, nowTime func() time.Time, afterPoll func(), publish func([]byte) error, limit int) hookResult`.
  - `func hookOutput(event, context string) []byte`.
  - `func newHookHint(profile, as, configEnv string) hookHint` (compatibility wrapper) and live path `func newHookHintMCP(profile, as, configEnv, mcp string) hookHint`.
  - `hookContext`, `hookSummary`, `hookTrust`, `statePath` and `stateFile.update`.
  - The constants `hookMaxUnits` and `hookPipeAtomicBytes`.
- **Produces:**
  - **Event values:** `--event` accepts `user-prompt | post-tool | session-start`. Stdin `hook_event_name: "SessionStart"` maps to `session-start`, whatever its `source`.
  - **Behaviour:** keep stdin parsing, identity resolution, registry and admission before polling. session-start does not set hookOwner.prompt: a new session is silent until UserPromptSubmit (an automatic binding defers its first claim on existing cursor state; VECNET_PROFILE=P permits a declared prompt binding to claim immediately). An admitted session, no-session manual run or explicit rebind window polls without post-tool rate limiting, records LastPostToolPoll, summarises empty cursor state or shows new deliveries. Foreign/ambiguous starts follow the existing silent non-prompt path.
  - **Output name:** `hookOutput("session-start", ctx)` → `hookEventName: "SessionStart"`. `post-tool` → `PostToolUse` and anything else → `UserPromptSubmit`, as today.
  - **Other events:** without --event, any other `hook_event_name` returns before `connect`, so there is no hub request, no state write and stdout is empty (today's default branch, kept).
  - **`usageText`:** `hook [--event user-prompt|post-tool|session-start] [--profile P] [--auto-ack] [--mcp vecnet-P]`; describe Claude Code / Codex and retain the MCP hint explanation.

- [ ] **Step 1: Write the failing tests**

```go
// hook_test.go (package cli)
func TestHookOutputSessionStart(t *testing.T)       // json.Unmarshal(hookOutput("session-start","x")): only top-level key hookSpecificOutput; hookEventName=="SessionStart"; additionalContext=="x"
func TestHookSessionStartPolls(t *testing.T)        // counting fake, state PollCursor "e1.1", LastPostToolPoll = hookNow() (a post-tool now would be rate-limited); one delivery pending → `hook --event session-start` makes 1 /v1/events call, prints hookEventName "SessionStart", context contains hookTrust and `<vecnet-inbox untrusted="true">`; a second post-tool at the same hookNow makes no call
func TestHookSessionStartFirstRun(t *testing.T)     // empty state, fake as in TestHookFirstRunSummarisesBacklog → stdin {"hook_event_name":"SessionStart","source":"startup"} with no session_id prints additionalContext == hookSummary(newHookHint("p","",""),2,false) byte for byte under "SessionStart"; PollCursor seeded
func TestHookSessionStartSameContext(t *testing.T)  // same fake page and fresh state copies, three runs with session-start / user-prompt / post-tool → the three additionalContext strings are byte-identical
func TestHookSessionStartMaxItemFits(t *testing.T)  // one item with a 300-rune quote-heavy astral body and a 32-rune hostile name → len(hookOutput("session-start", hookContext(…))) <= hookPipeAtomicBytes
func TestHookCodexEventsSilent(t *testing.T)        // counting fake; stdin for each of PreToolUse, PermissionRequest, PreCompact, PostCompact, SubagentStart, SubagentStop, Stop, SessionEnd, Interrupt, PreModelRequest, PostModelRequest, with Codex fields (session_id, turn_id, cwd, model, stop_hook_active), both without and with agent_id (so the early skip cannot mask event-routing regressions) → exit 0, stdout "", 0 requests, no state file created
```

```go
// hook_e2e_test.go (package cli_test, testhub.Start(t))
func TestHookCodexStdinShapes(t *testing.T)         // set VECNET_PROFILE to the test profile and seed UserPromptSubmit with session_id s1; B sends A "codex hello" --wake; stdin {"session_id":"s1","transcript_path":"/x","cwd":"/w","hook_event_name":"SessionStart","model":"m","source":"resume","permission_mode":"default"} → one JSON line, hookEventName "SessionStart", context has `text="codex hello"` and "vecnet --net "; then stdin PostToolUse with tool_input {} and a 900 KiB tool_response string (under the 1 MiB stdin cap) → exit 0, stdout "" (already shown)
```

Proposed admission tests in hooksession_test.go: `TestHookSessionStartAdmission` covers fresh/unbound silence, declared prompt binding, later admitted start, foreign/ambiguous silence and explicit rebind. `TestHookSessionStartAgentIDSkip` covers no state/registry/hub changes for agent_id; `TestHookSessionStartMCPHints` compares bound/unbound hints with current output. No-session poll/summary tests above deliberately omit session_id.

Guards also include existing `TestHookStateIdentityMismatchResets`, `TestHookStateCredentialRotationKeeps`, `TestHookHintsQualifiedWithProfile`, `TestHookHintMCPBound`, `TestHookHintUnboundUnchanged`, `TestHookMCPNameInvalidFallsBack`, `TestHookSubagentClaimsNothing`, `TestHookBindingMovesOnlyExplicitly`, `TestHookTwoSessionsOneDirectory` and `TestWatchKeepsHookBinding`.

Guards: `TestHookEventFromStdin`, `TestHookNeverFailsATurn`, `TestHookHostileIsInert`, `TestHookMaxItemAlwaysFits`, `TestHookRateLimitPostTool`.

- [ ] **Step 2:** `BB_HOST=x-tuf BB_JOBS=2 just bb CGO_ENABLED=0 go test -p 2 ./internal/cli/ -run 'SessionStart|CodexEvents|CodexStdin'` fails.
- [ ] **Step 3:** Implement. The diff to `hook.go` is a few lines; keep it limited to routing/output, leaving admission and MCP hint selection unchanged.
- [ ] **Step 4:** `BB_HOST=x-tuf BB_JOBS=2 just bb CGO_ENABLED=0 go test -p 2 ./internal/cli/` passes. `git diff master -- internal/cli/testdata/` is empty.
- [ ] **Step 5: Commit** `feat(hook): accept SessionStart for Codex; other Codex events stay silent` (trailer `Plan-Task: 01`)

---

### Task 2: `vecnet hook install --client codex`

**Depends:** 1

**Files:**
- Modify `internal/cli/hookinstall.go`, `internal/cli/hookinstall_test.go` and `internal/cli/hookinstall_internal_test.go`.
- Modify `internal/cli/commands.go`: only the `hook install` usage line.

**Interfaces:**
- **Consumes, unchanged:** `parseJSONNode`, `jsonNode.encode`, `detectIndent`, `oursHook`, `hookOwnedCommand` (its `--event ([^ ]+)` already matches `session-start`), `hookCommand`, `hookShellWord`, `defaultBin`, `writeHookSettings`, and `func (a *app) hookGit(dir, arg string) (string, error)` / hookMainCheckout. Preserve `hookCommand(bin, profile, event string, autoAck bool, mcp string) string` and its optional --mcp suffix; hookOwnedCommand already recognizes it.
- **Produces:**
  - **`hookHandler`:** gains `AdditionalContextLimit int \`json:"additionalContextLimit,omitempty"\``. The Claude path leaves it 0, so Claude bytes are unchanged.
  - **`const hookCodexContextLimit = 3 * hookMaxUnits`,** with a comment giving the no-spill derivation from the Global Constraints.
  - **Event lists:** `type hookEvent struct{ key, name, matcher string; contextLimit int }`.
    - `var claudeHookEvents = []hookEvent{{"UserPromptSubmit","user-prompt","",0},{"PostToolUse","post-tool","*",0}}`.
    - `var codexHookEvents = []hookEvent{{"SessionStart","session-start","",hookCodexContextLimit},{"UserPromptSubmit","user-prompt","",hookCodexContextLimit},{"PostToolUse","post-tool","*",hookCodexContextLimit}}`.
  - **Merge functions:**
    - `func mergeHooks(existing []byte, bin, profile string, autoAck bool, mcp string, events []hookEvent) (out []byte, changed bool, err error)` is today's body with the hard-coded event table replaced by `events`, and `Timeout: 5` for every handler.
    - `func mergeHookSettings(existing []byte, bin, profile string, autoAck bool, mcp string) (out []byte, changed bool, err error)` keeps its current signature and delegates to `mergeHooks(existing, bin, profile, autoAck, mcp, claudeHookEvents)`, so existing tests and callers are untouched.
  - **`defaultSettingsRedirect(path, what string) error`:** `what` replaces the literal "the personal settings.local.json" in the message. The Claude call passes that same text, so its message is byte-identical. The Codex call passes "the project .codex/hooks.json".
  - **Flags:** `cmdHookInstall` gains `client` (strFlag): `claude` (default) or `codex`. Anything else gives `usageErr("hook install: --client must be claude or codex")`. Keep existing boolean install --mcp for Claude, including dry-run argv, local-scope registration, VECNET_CONFIG forwarding and failures. Reject --client codex --mcp with `usageErr("hook install: --mcp registration is supported only for --client claude")` before settings reads/writes or any client process. Codex registration stays out of scope; never invoke claude mcp for Codex.
  - **Codex target and checks:**
    - **Target:** `--settings PATH`. Otherwise `<git top-level>/.codex/hooks.json`, falling back to `<cwd>/.codex/hooks.json`. Codex starts from cwd and ignores CLAUDE_PROJECT_DIR and Claude main/nested-checkout advice; retain those behaviors unchanged in the Claude lane. Codex multiple-profile advice names VECNET_PROFILE=P codex and existing rebind limitations. The stderr reason is `git top-level; Codex project hooks` or `cwd fallback; Codex project hooks`.
    - **Tracked-file check:** only with `--write`, no `--settings`, and a successful top-level probe. Run `git -C <root> ls-files --error-unmatch -- .codex/hooks.json` with the probe's 2 s timeout and `WaitDelay`. Exit status 1 (untracked) proceeds. Any other outcome refuses with `hook install: <path> is tracked by git (or its status is unknown); writing it would share your profile with everyone; pass --settings <path> to write it anyway`.
    - **Write:** then the existing redirect check, mode preservation and atomic write.
    - **Messages:** success prints `wrote <path> (SessionStart, UserPromptSubmit, PostToolUse)`. After both dry-run and write, stderr gets `codex: trust these hooks in /hooks before they run; new or changed hooks are skipped until trusted` and `codex: project hooks load only in a trusted project; to keep this file personal, list .codex/hooks.json in .git/info/exclude`.
  - **`usageText`:** `hook install [--client claude|codex] [--profile P] [--write] [--settings PATH] [--auto-ack] [--mcp]`, with --mcp explicitly Claude-only.

- [ ] **Step 1: Write the failing tests**

```go
// hookinstall_test.go (package cli_test)
func TestHookInstallCodexPrintsStanza(t *testing.T)            // `hook install --client codex --profile p --bin /opt/vecnet` → stdout JSON hooks has exactly SessionStart, UserPromptSubmit, PostToolUse; each one group with one handler {type "command", command "/opt/vecnet hook --profile p --event <session-start|user-prompt|post-tool>", timeout 5, additionalContextLimit 18000}; PostToolUse matcher "*", the others no matcher key; no Stop, SubagentStop, Interrupt, SessionEnd, SubagentStart, PreToolUse key
func TestHookInstallCodexDefaultTarget(t *testing.T)           // temp git repo (GIT_DIR/GIT_WORK_TREE unset as in TestHookInstallDefaultsToLocalAtGitRoot), run from a subdirectory with --write → <root>/.codex/hooks.json written; <root>/.claude absent; stderr has "/hooks" and ".git/info/exclude"; a rerun prints "unchanged"
func TestHookInstallCodexRefusesTrackedDefault(t *testing.T)   // commit .codex/hooks.json containing {} → --write exits non-zero, file byte-identical; the same command with --settings <that path> writes it
func TestHookInstallCodexRefusesSymlinkDefault(t *testing.T)   // .codex is a symlink to another dir → --write refused, link and target unchanged, message names "the project .codex/hooks.json"
func TestHookInstallCodexPreservesForeign(t *testing.T)        // hooks.json with "description", a c2c SessionStart handler (matcher "startup|resume") and a dcg PreToolUse group → after --write, those keys and groups are unchanged and in order; diff shows only added lines plus one comma; a second profile adds its own handlers
func TestHookInstallRejectsUnknownClient(t *testing.T)         // --client cursor → usage error, no file written
```

```go
// hookinstall_internal_test.go (package cli)
func TestCodexContextLimitCoversCaps(t *testing.T)             // hookCodexContextLimit >= 3*hookMaxUnits && hookCodexContextLimit >= hookPipeAtomicBytes
func TestMergeHookSettingsMatchesClaudeEvents(t *testing.T)    // for three fixture inputs, mergeHookSettings(x,…,mcp) == mergeHooks(x,…,mcp,claudeHookEvents) byte for byte and contains no "additionalContextLimit"
```

Add proposed `TestHookInstallCodexRejectsMCP` (usage error, no file/client process), `TestHookInstallCodexIgnoresClaudeProjectDir` and git-status probe-failure coverage (unknown tracking status refuses). Merge equivalence tests cover empty and valid mcp values; Claude MCP retains its suffix and omits additionalContextLimit.

Guards include hookinstall_session_test.go and existing `TestHookInstallMCPPrintsRegistration`, `TestHookInstallMCPWriteRunsClaude`, `TestHookInstallMCPWithoutClaudeStillWrites`, `TestHookInstallMCPRefusesUnnameable`. Guards: every existing `hookinstall_test.go` and `hookinstall_internal_test.go` test, in particular `TestHookInstallPrintsStanza`, `TestHookInstallDefaultsToLocalAtGitRoot`, `TestHookInstallDefaultRefusesSymlinkRedirect` and `TestOursHookExactForms`.

- [ ] **Step 2:** `BB_HOST=x-tuf BB_JOBS=2 just bb CGO_ENABLED=0 go test -p 2 ./internal/cli/ -run 'HookInstallCodex|UnknownClient|CodexContextLimit|MatchesClaudeEvents'` fails.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `BB_HOST=x-tuf BB_JOBS=2 just bb CGO_ENABLED=0 go test -p 2 ./internal/cli/` passes.
- [ ] **Step 5: Commit** `feat(hook install): --client codex writes .codex/hooks.json (SessionStart, UserPromptSubmit, PostToolUse)` (trailer `Plan-Task: 02`)

---

### Task 3: Codex connector guide, PROTOCOL §13.9 bullets, changelog, gate

**Depends:** 1, 2. Written from the merged code.

**Files:**
- Create `docs/connectors/codex.md`.
- Modify `docs/PROTOCOL.md`: the §13.7 command list, and a new §13.9 bullet placed after **Install**.
- Modify `docs/connectors/claude-code.md`: one sentence linking `codex.md`.
- Modify `CHANGELOG.md`: one line at the top.

**Interfaces (content that must appear):**
- **PROTOCOL §13.7:** `hook [--event …]` lists `session-start`, and `hook install` lists `--client claude|codex`.
- **PROTOCOL §13.9 "Codex (2026-11-04)" bullet:**
  - **Events:** `SessionStart` (any source), `UserPromptSubmit` and `PostToolUse` use the same admitted poll, wrapper, caps, state, identity/session binding and hints; SessionStart cannot automatically bind a fresh session. Other Codex events are silent.
  - **Lifecycle:** no lifecycle handlers.
  - **Handler fields:** `"timeout": 5` and `"additionalContextLimit": 18000`, with why (no spill to `<temp_dir>/hook_outputs/`).
  - **Trust:** Codex adds this context as developer context, and the wrapper stays untrusted data.
  - **Target:** the default target is `<git top-level>/.codex/hooks.json`, refused when git tracks it unless `--settings` is given.
  - **Trust review:** hooks run only after `/hooks` trust in a trusted project.
  - **Subagents:** Codex tool hooks may share the parent session id without agent_id (a present agent_id is always skipped), so one agent per profile applies, and a delivery shown to a subagent stays unacked and in `vecnet inbox`.
  - Cite the Codex hooks URL.
- **`docs/connectors/codex.md`** (same shape as `claude-code.md`):
  - **Set up:** `hook install --client codex --profile P`, then --write; launch with VECNET_PROFILE=P codex and trust in /hooks; optionally list the file in `.git/info/exclude`. A user-level `~/.codex/hooks.json` via `--settings` would make every Codex session poll as `P`.
  - **What the agent sees:** a `SessionStart` example and the first-run summary, both with `vecnet --net P` hints.
  - **Latency:** no idle wake; delivery comes at the next session start, prompt or tool completion (post-tool at most every 5 s). Use `vecnet --net P watch` or `inbox` to check.
  - **Acks:** explicit, as for Claude.
  - **Troubleshooting:** untrusted hook, untrusted project, a restrictive `PostToolUse` matcher, `features.hooks = false`, and silence being normal.
  - **Limits:** existing session admission, no Codex process auto-takeover, local hook rebind opening the existing next-hook window when Claude session env is absent, and subagents without agent_id. Codex --mcp registration is refused; Claude --mcp stays supported.
  - **Restart:** hook poll errors remain silent without committing shown ids/cursors; retry next safe hook. Keep §2.3 HTTP SSE `event: hub_restarting`, `retry: 1000`, `data: {"retry_after_ms":1000}` and §4.1 MCP clean close with no custom restart event or synthetic final JSON-RPC result. No transport change here.
  - **Live check:** `just codex-smoke` (Task 4).
- **`CHANGELOG.md`:** `- 2026-11-04: vecnet hook accepts Codex SessionStart; hook install --client codex writes .codex/hooks.json (timeout 5, additionalContextLimit 18000, no lifecycle hooks) (internal/cli/hook*.go, PROTOCOL §13.9, docs/connectors/codex.md)`.

- [ ] **Step 1:** Write the docs. Every literal (event names, flags, 5, 18000, paths, the stderr reminder text) is copied from the merged code, not from this plan.
- [ ] **Step 2: Gate:** `BB_HOST=x-tuf BB_JOBS=2 just bb CGO_ENABLED=0 go test -p 2 ./...`, `BB_HOST=x-tuf BB_JOBS=2 just bb go vet ./...`, `gofmt -l .`, `BB_HOST=x-tuf BB_JOBS=2 just bb CGO_ENABLED=0 go build -p 2 -o bin/ ./cmd/...`, `BB_HOST=x-tuf BB_JOBS=2 just bb CGO_ENABLED=0 GOOS=windows go build -p 2 -o /dev/null ./cmd/vecnet`, `BB_HOST=x-tuf BB_JOBS=2 just bb CGO_ENABLED=0 GOOS=darwin go build -p 2 -o /dev/null ./cmd/vecnet`, `BB_HOST=x-tuf BB_JOBS=2 just bb just isa-check` and `BB_HOST=x-tuf BB_JOBS=2 just bb just validate-protocol` all pass. Also check `grep -n 'session-start' docs/PROTOCOL.md docs/connectors/codex.md` and `grep -c 'additionalContextLimit' docs/connectors/codex.md` (at least 1).
- [ ] **Step 3: Commit** `docs: Codex connector guide and PROTOCOL §13.9 Codex bullets` (trailer `Plan-Task: 03`)

---

### Task 4: Opt-in live smoke, `just codex-smoke` (coordinator-run)

**Depends:** 1, 2

**Files:**
- Create `scripts/codex/hook-smoke.mjs`.
- Modify `justfile`: one build-box-only recipe, `codex-smoke: build` followed by `build2 node scripts/codex/hook-smoke.mjs`, with a comment saying it is opt-in and coordinator-run.

**Interfaces:**
- **Consumes:** `bootDemo` from `scripts/ui/lib.mjs` (returns `{demo, asPeer, close}`, where `demo.peers.codex.{id,secret}` and `demo.url`), `bin/vecnet`, and the original Codex CLI 0.166.0 flag contract (recheck the installed build at execution time) (`exec`, `--dangerously-bypass-hook-trust`, `--skip-git-repo-check`, `-C`, `--ephemeral`, `-o`).
- **Skips (exit 0, one `SKIP: <reason>` line, touches nothing):**
  - `codex` is not on `PATH`;
  - `codex exec --help` lacks `--dangerously-bypass-hook-trust`;
  - `CODEX_SMOKE_HOME` is unset. It must point at a disposable Codex home the coordinator prepared with working model access.
- **Refuses (exit 2):**
  - `realpath(CODEX_SMOKE_HOME)` is `$HOME/.codex`;
  - it already holds a `hooks.json` without the marker file `.vecnet-smoke`.
- **Flow:**
  1. All vecnet state goes under `S = mkdtemp`: `VECNET_CONFIG=$S/config.toml`, `XDG_CONFIG_HOME=$S/xdg` and `XDG_CACHE_HOME=$S/cache`. Unset inherited CLAUDE_PID, CLAUDE_CODE_SESSION_ID and CLAUDE_PROJECT_DIR for every child (including the no-session seed); do not alter the parent environment.
  2. Start `bootDemo()`. Write `$S/config.toml` with profile `smoke` set to `{hub = demo.url, secret = demo.peers.codex.secret, peer_id = demo.peers.codex.id}`.
  3. Write the `.vecnet-smoke` marker. Run `bin/vecnet hook install --client codex --profile smoke --bin <abs bin/vecnet> --settings $CODEX_SMOKE_HOME/hooks.json --write` (the user layer, so no project trust is needed).
  4. Seed: pipe `{"hook_event_name":"UserPromptSubmit"}` into `bin/vecnet hook --profile smoke`.
  5. Send canary `T = "VXC-" + 12 random hex` from `claw` to the `codex` peer via `asPeer('claw','POST','/v1/messages', …)` with priority `wake`. asPeer returns a fetch Response: check response.ok (and decode JSON when needed), never treat it as an already-decoded body.
  6. Run `codex exec --dangerously-bypass-hook-trust --skip-git-repo-check --ephemeral -C $S/ws -o $S/last.txt "Reply with only the VXC- token from the vecnet block in your context, or NONE."` with `CODEX_HOME=$CODEX_SMOKE_HOME` and the vecnet env above plus VECNET_PROFILE=smoke. Unset inherited CLAUDE_PID, CLAUDE_CODE_SESSION_ID and CLAUDE_PROJECT_DIR for the Codex child. Kill it after 180 s.
- **Assertions:**
  - **PASS:** `last.txt` contains `T`, and `bin/vecnet --net smoke inbox` still lists the canary's delivery (no auto-ack).
  - **Otherwise FAIL (exit 1):** print the codex exit code, `last.txt`, and the hint "codex exec may not run hooks: use the manual interactive check in docs/connectors/codex.md".
  - **Info only:** print whether any handler ran during the exec, judged by whether `$S/state/smoke.json`'s `last_post_tool_poll` changed from the seed; also inspect the separate state/smoke+session.json binding and start/prompt order, never assuming SessionStart binds. Subagent behaviour is not asserted (B1).
- **Cleanup, always, in a `finally`:** restore any pre-existing marked hooks.json/marker bytes and mode, or remove only those newly created by this run; `close()` the demo, and remove `S`. It never reads or writes `~/.codex` or `~/.config/vecnet`.

- [ ] **Step 1:** Write the script. Verify the skip/refusal paths on the build box with Node and disposable fixtures: PATH containing Node but no codex prints SKIP and exits 0; a stub codex advertising the required flag with CODEX_SMOKE_HOME unset also skips; the same stub with CODEX_SMOKE_HOME=$HOME/.codex exits 2 before touching that path. Run via BB_HOST=x-tuf BB_JOBS=2 just bb, respecting early-check order. No live model access is used in this task.
- [ ] **Step 2:** `node --check scripts/codex/hook-smoke.mjs` passes, and `just --list` shows `codex-smoke`.
- [ ] **Step 3: Commit** `test(codex): opt-in live hook smoke (just codex-smoke)` (trailer `Plan-Task: 04`)

After the merge, the coordinator prepares a disposable Codex home, runs `CODEX_SMOKE_HOME=… just codex-smoke` once inside the prepared disposable build-box environment (the wrapper refuses forwarding secret/model-session env names; provision this home on the box separately), and records the result in the build status. If `codex exec` turns out not to run hooks, the coordinator follows the guide's manual interactive check instead and records that.

---

--- SUMMARY ---

- **What:** Codex support for `vecnet hook`, refreshed against master fcc71a1, including MCP-first and session admission, as the four-task replacement for codex-llmp's bundle (`.scratch/vecnet/codex-review/bundle-review.md`).
- **Why it is small:** Codex's hook protocol is the same as the one `hookRun` already handles: stdin `hook_event_name`, `hookSpecificOutput.additionalContext`, and `hooks.json` nesting identical to Claude settings. The shared core (state, identity binding, hints, caps, pipe write, purge recovery) is reused unchanged.
- **Task 1:** `--event session-start` / stdin `SessionStart` polls only after existing admission, with output named SessionStart; an unbound session waits for UserPromptSubmit/rebind. Other stdin events are silent without an explicit --event; keep the current override.
- **Task 2:** `hook install --client codex` writes the `SessionStart`, `UserPromptSubmit` and `PostToolUse` handlers (`timeout: 5`, `additionalContextLimit: 18000`, PostToolUse matcher `*`) through the existing order-preserving merge.
  - The default target is `<git top-level>/.codex/hooks.json`, refused when git tracks it unless `--settings` is given; a symlinked `.codex` is refused.
  - It prints the `/hooks` trust and `.git/info/exclude` advice, and never edits trust or VCS state.
  - Claude output and --mcp registration/hint suffix remain byte-identical; Codex --mcp registration is refused.
- **Task 3:** `docs/connectors/codex.md`, §13.7 and §13.9 Codex bullets (no new section), the changelog line and the full gate.
- **Task 4:** `just codex-smoke`, an opt-in, coordinator-run canary through `codex exec` with a demo hub, private vecnet state and a disposable Codex home. It skips without `codex` or `CODEX_SMOKE_HOME`, refuses `~/.codex`, and asserts the canary reached context and stayed unacked.
- **Constraints baked in from the Codex docs:**
  - No `Stop`/`Interrupt`/`SessionEnd`/`SubagentStart`/`PreToolUse` handlers.
  - An explicit 5 s timeout (the default is 600 s).
  - The output cap (6000 UTF-16 units ≤ 18000 bytes ≥ tokens) stays under `additionalContextLimit`, so no peer text spills to `<temp_dir>/hook_outputs/`.
  - Context is developer-role, so the trust line and inert wrapper stay byte-identical.
  - Never exit 2.
- **Deliberately not built:** new binding/takeover machinery (reuse master), additional root-versus-subagent detection (Codex exposes no signal; one agent per profile is documented), Codex-specific cursor-gap resync, a bound-call guard, MCP setup, doctor, a version allowlist, and the managed app-server tier (launcher P4 `codex-app-server`).
- **Dependencies:** `{"1":[],"2":[1],"3":[1,2],"4":[1,2]}`. 1 → 2 is serialised on `commands.go` and the hook tests; 3 and 4 run in parallel.
- **Rebase points:** `usageText`, `CHANGELOG.md`, PROTOCOL §13.7/§13.9, and `justfile` (launcher `ln-*` recipes). No PROTOCOL section and no migration are allocated.
- **Open:** whether `codex exec` runs hooks is unverified. Task 4 reports it, and the guide's manual interactive check is the fallback.
