# Antigravity CLI (`agy`) Delivery Integration Design & Plan

**Date:** 2026-11-20 · **Status:** implemented (2026-11-21): Slices 1.1–1.2 (agy hook install + hook runtime), 2 (`vecnet_wait` background waiter + `watch --once`) and 4 (`agy acp` launcher qualification) are built and covered by a disposable end-to-end probe ([connector guide](../../connectors/agy.md), [evidence](../../research/evidence/agy-1.5.13.json)). Slice 1.3 (enrollment/local authority/passive status) and Slice 3 (native sidecar push) are deferred; Slice 0 probes ran 2026-11-20 ([results](../../research/2026-11-20-agy-autodelivery.md#5-slice-0-results-2026-11-20-agy-1513-linux-x86-64)), interactive TUI checks included.

[Research report](../../research/2026-11-20-agy-autodelivery.md) · [Claude delivery spec](./2026-11-19-claude-delivery-design.md) · [Codex app-server connector](../../connectors/codex-appserver.md) · [Launcher ACP setup](../../connectors/launcher.md#set-up-claude-through-acp)

---

## 1. Goal & Architectural Decisions

A Vecnet-enabled Antigravity CLI (`agy`) root session should enroll cleanly, work out of the box inside `agy`'s default sandboxed `auto` mode, receive active-turn and idle deliveries with attributable framing, and integrate into the **exact same per-user shared delivery host (`startDeliveryHost`), `WatchPool`, SQLite claim ledger (`delivery.sqlite`), and enrollment/repair pipeline** already used by Codex and Claude Code.

Core decisions:

1. **Reuse the shared delivery host—never add a second daemon.** `agy` mounts as a `HostedAdapter` alongside Codex and Claude inside `startDeliveryHost` (`connectors/launcher/src/delivery/host.ts`), sharing the existing singleton lock, `WatchPool`, and `delivery.sqlite` (using a new `ledger.kv('agy')` namespace) and publishing its own strict versioned status sidecar (`bindings.json.agy-status.json`).
2. **MCP-first is mandatory, not optional.** Because `agy` v1.5.7+ defaults to `auto` mode whose terminal sandbox blocks `~/.config` (`vecnet/config.toml`) and network access, `vecnet hook install --client agy` must support writing workspace-scoped `.agents/mcp_config.json` (`vecnet-<profile>` running `vecnet --net <profile> mcp`) with core tools marked `"eager": true` so everyday inbox/reply/ack work without weakening the user's sandbox.
3. **Ship in 4 decoupled, gated slices so no upstream limitation blocks earlier value:**
   - **Slice 0:** Disposable canary probes (hooks framing/worktrees, background MCP `vecnet_wait` wakeup, plugin sidecar + `agentapi`, and `agy acp` launcher smoke).
   - **Slice 1 (Foundation + Active-Turn Hooks + Enrollment/Status):** Exact root registration (`agy-bindings.json`), `vecnet hook --client agy` (`SessionStart` + `PreInvocation`), `vecnet hook install --client agy [--mcp] --write`, `vecnet enrol`/`repair` integration, and passive `agy status` sidecar.
   - **Slice 2 (Cross-Platform Sandbox-Safe Idle Wakeup):** Background MCP waiter tool `vecnet_wait` (`"background": "always"`) + `vecnet watch --once` CLI fallback, providing reactive idle wakeup across Linux, macOS, and Windows inside `auto` mode.
   - **Slice 3 (Zero-Setup Shared-Daemon Native Push — Gated Canary):** Contingent on Slice 0 confirming `agy` CLI runs plugin sidecars and `agentapi send-message` safely wakes an idle TUI. A thin Vecnet Antigravity plugin sidecar bridges authenticated local deliveries from `startDeliveryHost` → `vecnet agy deliver` → `agentapi send-message`, with zero CSRF token scraping/persistence, pointer-only framing, 128 pending cap, and no auto-ack.
   - **Slice 4 (Launcher-Owned `agy` Sessions via Existing ACP Backend):** Qualify and document `agy acp` (`--acp-mode default`) on `connectors/launcher/src/backends/acp/`, with an optional follow-up for steering and `session/resume`.
4. **Never compromise Vecnet security invariants:**
   - Never read, log, persist, or borrow `ANTIGRAVITY_CSRF_TOKEN` or call `ANTIGRAVITY_LS_ADDRESS` directly.
   - Never modify `agy`'s `settings.json` (`agentMode`, `toolPermission`, `permissions.allow`, `trustedWorkspaces`) automatically.
   - Never turn `hold` into a pushed wake, never silently degrade `wake_idle` to `wake`, never auto-ack on local transport completion, and never use tmux/PTY injection or external `agy -p --conversation <id>` injection against a live TUI.

---

## 2. Architecture & Component Authority

| Component | Path(s) | Responsibility | Not an Authority For |
| --- | --- | --- | --- |
| **CLI enrollment, repair & `agy` hooks** | `internal/cli/agy_setup.go`, `agy_binding.go`, `agy_binding_linux.go`, `hook.go`, `hookinstall.go`, `commands.go` | Detect `ANTIGRAVITY_AGENT`, save durable pre-redemption intent (`agy-enrollments.json`), install `.agents/hooks.json` & `.agents/mcp_config.json`, run `PreInvocation`/`SessionStart` hooks with exact `conversationId` binding, manage `agy-bindings.json` & durable opt-outs. | `agy` permission rules, sandbox bypasses, hub ack advancement across unresolved claims. |
| **Shared delivery host** | `connectors/launcher/src/delivery/host.ts`, `codex/delivery-main.ts`, `daemon.ts` | Hold existing singleton lease, compose Codex + Claude + Agy `HostedAdapter`s, own `WatchPool`, open shared `delivery.sqlite`, drain all adapters before releasing lock. | Transferring ownership across profiles, sessions, or recipients. |
| **Agy passive status adapter** | `connectors/launcher/src/delivery/agy-status.ts`, `internal/cli/agy_status.go` | Poll `vecnet agy runtime-snapshot`, publish strict private `bindings.json.agy-status.json` with host PID/start/boot and opaque binding keys/generations/states. | Readiness of native transport; never carries paths, tokens, or message bodies. |
| **MCP relay + `vecnet_wait` (Slice 2)** | `internal/cli/mcp.go` / `internal/mcpserver/` | Expose profile-scoped `vecnet_*` tools outside the command sandbox; provide one-shot `vecnet_wait` (configured `background: "always"`) returning a pointer-only wake trigger on first `wake`/`wake_idle`. | Auto-acking hub cursors or delivering `hold` as a wake. |
| **Agy native delivery adapter + Go helper (Slice 3 canary)** | `connectors/launcher/src/delivery/agy-adapter.ts`, `internal/cli/agy_deliver.go`, `internal/cli/agy_deliveries.go` | Subscribe to `WatchPool`, stage bounded inbox candidates (`--no-display-proof`), commit durable `reserved` → `attempting` in `ledger.kv('agy')`, invoke single-shot `vecnet agy deliver` over owner-verified Unix bridge socket (`SO_PEERCRED` + `/proc` start), expose `agy deliveries` & `agy resolve`. | Presentation proof from write completion; never holds hub credentials inside the sidecar; never auto-retries uncertain attempts. |
| **Vecnet Antigravity Plugin (Slice 3)** | `connectors/agy-plugin/` (embedded/staged asset) | Bundles `plugin.json`, `hooks.json`, `mcp_config.json`, `skills/vecnet/SKILL.md`, and thin `sidecars/delivery-bridge/sidecar.json` that listens on `$XDG_RUNTIME_DIR/vecnet/agy-bridge-<pid>.sock` (`0600`) and executes sanctioned `agentapi send-message` / `get-conversation-metadata`. | Hub access, claim state, retries, or message-body parsing. |
| **Launcher ACP backend (Slice 4)** | `connectors/launcher/src/backends/acp/` | Spawn and drive launcher-owned `agy acp` jobs under `mode_id: "default"`. | Attaching to pre-existing user TUI sessions. |

---

## 3. Detailed Slice Designs

### Slice 0: Disposable Canary Qualification (Gate Before Coding Transports)

Build a disposable test harness (temp `HOME`, temp git repo + linked worktree, disposable loopback hub/model stub, `vecnet-test-<nonce>` tmux where TUI is needed, strict teardown verification) and record sanitized evidence under `docs/research/evidence/agy-1.5.13.json`:

1. **Hook contract verification:**
   - Capture raw stdin JSON & exported env var *names* for `SessionStart`, `PreInvocation` (turn 1 inv 0, turn 1 inv 1 after tool call, turn 2 inv 0), `Stop`, and a spawned subagent.
   - Verify whether `invocationNum` resets per user turn or is cumulative across the conversation.
   - Verify `injectSteps[].userMessage` vs `injectSteps[].systemMessage` (plus `metadata.user_markdown`) in the actual outbound model request and terminal rendering.
   - Verify `.agents/hooks.json` discovery in a linked git worktree.
2. **Background MCP `vecnet_wait` verification:**
   - Run a disposable stdio MCP server configured with `"tools": {"probe_wait": {"background": "always"}}` in `.agents/mcp_config.json`.
   - Confirm that completing `probe_wait` while the `agy` TUI is idle triggers an immediate autonomous turn, and `/exit` cleans up the pending call.
3. **Plugin sidecar + `agentapi` verification:**
   - Test an enabled plugin sidecar in interactive `agy` 1.5.13 TUI.
   - Verify whether the sidecar starts automatically in CLI mode, what `agentapi get-conversation-metadata <id>` returns in idle/busy/approval-wait states, and what `agentapi send-message <id> <pointer>` does in each state (including duplicate-send control and outbound model role capture).
4. **ACP launcher verification:**
   - Run `AcpBackend` integration test against `agy acp` (`mode_id: "default"`): `initialize`, `session/new`, `session/set_mode`, `session/prompt`, `session/request_permission`, `session/cancel`, `session/close`.

---

### Slice 1: Active-Turn Hooks, MCP-First Setup, Exact Registration & Passive Status

#### 1.1 Hook Installation (`vecnet hook install --client agy`)
Extend `internal/cli/hookinstall.go`:
- Accept `--client agy` alongside `claude` and `codex`.
- Default target: `<git-top-level>/.agents/hooks.json` (or CWD if not in git). Apply the same safety guards as Codex:
  - Refuse symlinks on `.agents` or `hooks.json` unless explicit `--settings PATH` is passed.
  - Refuse writing to a git-tracked `.agents/hooks.json` unless `--settings` is explicit; advise adding `.agents/hooks.json` (and `.agents/mcp_config.json`) to `.git/info/exclude`.
  - Support `--dry-run`, `--write`, and backup-verified `--rollback` (reusing Codex's migration/backup pattern).
- Named hook stanza merged cleanly into `.agents/hooks.json` (preserving unrelated hook groups and key order):
  ```json
  {
    "vecnet-P": {
      "enabled": true,
      "SessionStart": [
        {
          "type": "command",
          "command": "/abs/path/to/vecnet hook --client agy --profile P --event session-start --mcp vecnet-P",
          "timeout": 5
        }
      ],
      "PreInvocation": [
        {
          "type": "command",
          "command": "/abs/path/to/vecnet hook --client agy --profile P --event pre-invocation --mcp vecnet-P",
          "timeout": 5
        }
      ]
    }
  }
  ```
- When `--mcp` is passed (recommended default in `guide.md` for `agy`):
  - Merge `mcpServers["vecnet-P"]` into `<git-top-level>/.agents/mcp_config.json` (same symlink & git-untracked guards; never clobber other servers):
    ```json
    {
      "mcpServers": {
        "vecnet-P": {
          "command": "/abs/path/to/vecnet",
          "args": ["--net", "P", "mcp"],
          "env": {},
          "tools": {
            "vecnet_inbox": { "eager": true },
            "vecnet_reply": { "eager": true },
            "vecnet_ack": { "eager": true },
            "vecnet_send": { "eager": true }
          }
        }
      }
    }
    ```
  - Include `"VECNET_CONFIG"` in `env` if `VECNET_CONFIG` is explicitly set (same rule as Claude `--mcp`).
  - Keep `VECNET_PROFILE` multi-session guard in `vecnet mcp` (a relay whose profile differs from `VECNET_PROFILE` refuses to start).

#### 1.2 Hook Execution (`vecnet hook --client agy`)
Extend `internal/cli/hook.go` & `hooksession.go`:
- Parse `agy`'s camelCase stdin payload:
  ```go
  type agyHookInput struct {
      HookEventName string   `json:"hookEventName"`
      ConversationID string  `json:"conversationId"`
      Cwd           string   `json:"cwd"`
      InvocationNum int      `json:"invocationNum"`
      InitialSteps  int      `json:"initialNumSteps"`
      ModelName     string   `json:"modelName"`
  }
  ```
- **Session binding & subagent isolation:**
  - Use `conversationId` (falling back to `ANTIGRAVITY_CONVERSATION_ID`) as the exact session identity.
  - Because every Antigravity subagent gets its own distinct `conversationId`, a bound profile automatically rejects subagent hook calls (subagent sees nothing, claims nothing).
  - Bind ONLY on `SessionStart` or turn-start `PreInvocation` (`invocationNum == 0`; Slice 0 verified it resets to 0 on every user turn) and NEVER on mid-turn invocations, preventing newly spawned subagents from racing an unbound profile!
  - Track parent `agy` process (`PID` + `/proc` start + host) so `/clear` follows the same process while dead processes cleanly unbind.
  - Respect `VECNET_PROFILE` for multi-session isolation in the same repo.
- **Rate limiting:**
  - Turn-start `PreInvocation`: always poll (mirrors Claude `UserPromptSubmit`).
  - Mid-turn `PreInvocation`: 5-second rate limit (`hookPostToolEvery`, mirrors Claude `PostToolUse`).
  - `SessionStart`: passive root registration only—zero hub calls, emits `{}`.
- **Output encoding (`agyHookOutput`):**
  - When empty / silent: emit `{}` (valid JSON, exit 0; remember `agy` hooks require JSON stdout on exit 0).
  - When deliveries are claimed: emit `{"injectSteps": [{"systemMessage": {"systemMessage": "..."}}]}`, wrapping the existing `hookContext` / `hookSummary` inert `<vecnet-inbox untrusted="true">` block (and respecting the 4096-byte atomic pipe cap).
  - Preserve display-proof-before-commit and fail-open timeout budget (`1.5s`).
  - Slice 0 constraints (evidence `agy-1.5.13.json`):
    - **Never emit `userMessage`.** `agy` wraps it as `<USER_REQUEST>`, which looks exactly like a real user prompt, and overwrites `lastUserInput` for later hooks until the next real user input.
    - **Never set `metadata.user_markdown` on a model-visible step.** In 1.5.13 it suppresses the `systemMessage` text from the model entirely (the docs say otherwise).
    - Never use deprecated `ephemeralMessage`: it is delivered raw with no framing.
    - `systemMessage` reaches the model as role `user` inside agy's `<SYSTEM_MESSAGE>` wrapper ("important information to pay attention to"). At `invocationNum > 0` it is appended into the preceding tool's `functionResponse`. The inert `<vecnet-inbox untrusted="true">` frame therefore has to carry all of the de-privileging.
    - Hooks receive only `ANTIGRAVITY_CONVERSATION_ID` from agy (not `ANTIGRAVITY_AGENT`/`APP_DATA_DIR`/`LS_VERSION`). Take identity from stdin `conversationId` and discover version/app-data dir separately. Hook process cwd is the `.agents/` directory.
    - `.agents/hooks.json` must be installed in each worktree; a main-checkout file is not seen from linked worktrees.

#### 1.3 Enrollment, Local Authority & Passive Status
Mirror `internal/cli/claude_setup.go`, `claude_binding.go`, `claude_status.go`, and `connectors/launcher/src/delivery/claude-status.ts`:
- `isAgyEnvironment(getenv)`: true when `ANTIGRAVITY_AGENT != ""` (and neither Codex nor Claude markers are set).
- `prepareAgyEnrollment` / `reconcileAgyEnrollment` wired into `cmdEnrol` (`internal/cli/commands.go`) and `vecnet repair`:
  - Records durable pre-redemption intent in `agy-enrollments.json` (`Config`, `Profile`, `As`, `AppDataDir`, `ProjectDir`, `ConversationID`, `State`).
  - On commit, registers the exact root in `agy-bindings.json` (version 1, strict JSON decoder, mode `0600`, locked via `lockLocalState("agy-bindings")`) with `agyScope` (`config_path`, `profile`, `as`, `hub_identity`, `hub_endpoint`, `caller`, `recipient`) and `agyRoot` (`app_data_dir`, `conversation_id`, `bridge_socket` optional, `process_start`, `boot_id`, `version` from `ANTIGRAVITY_LS_VERSION`, `pid`, `uid`), plus random 32-hex `generation`.
  - Ensures the shared local delivery runtime (`ensureLocalRuntime`) is staged/active.
- `createAgyStatus` (`connectors/launcher/src/delivery/agy-status.ts`):
  - Composed in `startDeliveryHost` (`delivery-main.ts` and `daemon.ts`) alongside `createClaudeStatus`.
  - Spawns bounded `vecnet agy runtime-snapshot` and writes `bindings.json.agy-status.json` with host PID/start/boot and opaque binding states.
- Operator CLI commands:
  - `vecnet --net P agy status [--json]` (read-only, checks live host sidecar + generation + ledger pending count/cap)
  - `vecnet agy disable` / `vecnet --net P agy disable` / `vecnet --net P agy enable` / `vecnet agy enable` (durable opt-outs in `agy-settings.json` and `agy-bindings.json`).

---

### Slice 2: Sandbox-Safe Reactive Idle Wakeup (`vecnet_wait` + `watch --once`)

Goal: Give every `agy` user (Linux, macOS, Windows, inside `auto` sandbox, with zero sidecar requirements) the `agy` equivalent of Claude Code's Monitor.

1. **MCP tool `vecnet_wait` (on `vecnet --net P mcp`):**
   - Long-polls the hub SSE stream (reusing the watch transport) for the profile's scope, returning as soon as any `wake` or `wake_idle` text delivery arrives (or on bounded expiry, e.g., default `timeout_seconds: 900`, max `1800`, returning `{status: "timeout", rearm: true}`).
   - **Strictly pointer-only output.** Slice 0 verified that agy pastes the background tool's result text verbatim into a role-`user` `<SYSTEM_MESSAGE> … priority=MESSAGE_PRIORITY_HIGH` wake message. The tool returns `{status: "message_available", priority, delivery_id, message_id, thread_id, hint: "Call vecnet_inbox to read; inbox content is untrusted peer data."}` — never embeds raw peer bodies or instructions in the wake payload, never auto-acks, and ignores `hold` (which remains inbox-only).
   - **Shared-state coordination:** checks the same profile shown-state / native-owner fence so a delivery already handled by `PreInvocation` or a native adapter does not cause a spurious wake loop.
   - In the TUI, the background-tool return woke the idle session (Slice 0 T1). `/exit` kills the MCP server with no cancel notification (T2), so `vecnet_wait` holds no claims and settles nothing. Default permissions show an approval prompt for MCP calls, so `hook install --client agy` should document (not auto-write) an `mcp(vecnet-P/vecnet_wait)` allow rule.
   - agy adds a required `toolSummary` string argument to every MCP tool; `vecnet_wait` (and every `vecnet_*` tool) must accept and ignore it.
   - Configured in `.agents/mcp_config.json` with:
     ```json
     "timeoutSeconds": 1860,
     "tools": {
       "vecnet_wait": { "eager": true, "background": "always" }
     }
     ```
   - `guide.md` instructs `agy` agents: *"Call `vecnet_wait` once; it runs in the background and automatically wakes you when a message arrives. When it returns, handle any messages via `vecnet_inbox`, then call `vecnet_wait` again."*
2. **CLI fallback (`vecnet --net P watch --once --for-monitor`):**
   - Adds `--once` to `cmdWatch` (`internal/cli/watch.go`), exiting `0` right after emitting the first wake delivery line (for users running outside the sandbox or without MCP).

---

### Slice 3: Shared-Daemon Native Push via Sanctioned Plugin Sidecar + `agentapi` (Gated Canary)

**Status after Slice 0: DEFERRED.** Sidecars did not run under `agy -p` or in the interactive TUI (even with the documented opt-in), and `agentapi` authenticates only through the per-session `ANTIGRAVITY_CSRF_TOKEN`/`LS_ADDRESS` env given to `run_command` children. A hook calling `agy agentapi` fails with `ANTIGRAVITY_LS_ADDRESS is not set`. A Vecnet-arranged bridge would therefore have to borrow that env, which the CSRF rule in §1 decision 4 forbids. Reopen only if a later agy release runs plugin sidecars in the CLI with their own `agentapi` access.

**Entry Gate:** Slice 0 Probe 3 MUST pass (confirming `agy` CLI launches plugin sidecars, `agentapi send-message` wakes an idle interactive TUI cleanly, and busy/role framing is documented and safe). If Probe 3 fails, Slice 3 remains unshipped and Slices 1+2+4 remain the complete supported `agy` path.

1. **Vecnet Antigravity Plugin (`connectors/agy-plugin/`):**
   - Validated in CI with `agy plugin validate --strict`.
   - Contains:
     - `plugin.json` (`name: "vecnet"`, versioned with Vecnet CLI)
     - `hooks.json` (generic `SessionStart` / `PreInvocation` invoking `vecnet hook --client agy`)
     - `mcp_config.json` (optional/profile-scoped)
     - `skills/vecnet/SKILL.md` (progressive-disclosure workflow + untrusted-data rules)
     - `sidecars/delivery-bridge/sidecar.json` (`restart_policy: "on-failure"`, runs a tiny bounded local bridge executable/script)
   - **Sidecar trust & security contract:**
     - Listens on a private user-only Unix socket `$XDG_RUNTIME_DIR/vecnet/agy-bridge-<pid>.sock` (`0600`, `0700` parent dir, no symlinks).
     - Registers its socket path + owning `agy` PID/start/boot/`conversationId` into `agy-bindings.json` via `vecnet agy register-bridge` (bounded local call, zero hub access).
     - On each incoming connection: verifies `SO_PEERCRED` UID == own UID, reads one ≤4 KiB LF-terminated JSON frame `{version: 1, generation, conversation_id, msg_id, thread_id, message_id}`, verifies `conversation_id` matches the live bound root, checks `agentapi get-conversation-metadata <id>` (rejecting if archived/closed/mismatched), assembles the **fixed pointer-only copy** (never accepting arbitrary text over the socket!), and executes `agentapi send-message <conversation_id> <pointer>`.
     - **Zero CSRF token exposure:** `ANTIGRAVITY_CSRF_TOKEN` stays strictly inside `agy`'s spawned sidecar child environment and is never written to disk, logged, or sent over the socket.
     - Unlinks socket on clean exit; stale sockets fail `ECONNREFUSED` / `SO_PEERCRED` mismatch.
2. **Go Single-Shot Helper (`internal/cli/agy_deliver.go`):**
   - Exact mirror of `internal/cli/claude_deliver.go`:
   - `vecnet agy deliver --binding-key K --expect-generation G --msg-id M --thread-id T --message-id I`
   - Resolves binding under `agy-bindings` lock, validates `live && policy && version_qualified`, connects to `bridge_socket`, verifies `SO_PEERCRED` PID/UID + `/proc` start identity on that exact connection, sends the ≤4 KiB validated frame with a bounded deadline, and maps exit codes identically (`0` sent—not presentation proof, `2` refused before write, `3` connect failed/definitely unwritten, `4` write/response uncertain).
3. **Hosted TypeScript Adapter (`connectors/launcher/src/delivery/agy-adapter.ts`):**
   - Exact mirror of `connectors/launcher/src/delivery/claude-adapter.ts`:
   - Gated behind `VECNET_AGY_DELIVERY_CANARY=1` in `startDeliveryHost` (`host.ts`).
   - Uses `WatchPool` (`credentialSlot: 'agy'`, `scope: 'exclusive:agy:' + binding_key`), `ledger.kv('agy')`, `inbox --no-display-proof`, 128 pending cap (`AGY_PENDING_LIMIT`), `reserved` → `attempting` durable transitions before invoking `vecnet agy deliver`, `hold` quarantined inbox-only, `wake_idle` deferred unless Slice 0 proves an atomic receiver idle gate, and zero hub auto-ack.
4. **Operator Surface (`internal/cli/agy_deliveries.go`):**
   - `vecnet --net P agy deliveries [--json]`
   - `vecnet --net P agy resolve <delivery-id> --expect-generation G --keep-inbox | --retry --confirm-duplicate-risk`
5. **Writer Handoff Fence:**
   - When native delivery is active for a qualified scope (only after canary graduation), `PreInvocation` hooks consult the shared claim fence in `agy-bindings` / state lock so an already-natively-promoted wake is not double-displayed, while `hold` and legacy items remain readable.

---

### Slice 4: Launcher-Owned `agy` Sessions via ACP

1. **Immediate qualification (`connectors/launcher/src/backends/acp/`):**
   - Add an automated integration test fixture and live smoke check (`scripts/agy-acp-smoke.mjs`) exercising `AcpBackend` with `agy acp` (`mode_id: "default"`).
   - Document `vecnet --net PROFILE launcher setup --backend acp --acp-command /abs/path/to/agy --acp-arg=acp --acp-provider agy --acp-mode default ...` in `docs/connectors/launcher.md` and `docs/connectors/agy.md`.
2. **Optional follow-up (separate PR):**
   - Add opt-in capability negotiation in `AcpBackend` so presets advertising ACP steering / `session/resume` (such as `agy acp`) can support `steer` for `wake` during busy turns and post-restart session resume where safe.

---

## 4. Acceptance & Rollout Gates

| Gate | Required Evidence Before Promotion |
| --- | --- |
| **Slice 0 (Canary probes)** | Sanitized captures in `docs/research/evidence/agy-1.5.13.json` covering hook payload/role/worktree/subagent isolation, `vecnet_wait` background wakeup, sidecar/`agentapi` idle+busy+approval+duplicate controls, and `agy acp` handshake/turn/cancel. |
| **Slice 1 (Hooks + MCP + passive status)** | Unit + integration tests (`go test ./internal/cli -run TestAgy\|TestHookAgy`, `just ln-test connectors/launcher/test/delivery-agy-status.test.ts`): exact `conversationId` binding, subagent non-binding, `auto`-sandbox MCP round-trip, symlink/tracked-file refusal, `enrol` → `repair` recovery, stale sidecar rejection, and zero regression to Codex/Claude tests. |
| **Slice 2 (`vecnet_wait` + `watch --once`)** | Loopback hub + disposable `agy` session test proving idle wakeup on `wake`/`wake_idle`, non-wake on `hold`, clean timeout/re-arm, zero auto-ack, and zero leaked child on `/exit`. |
| **Slice 3 (Native sidecar canary)** | `agy plugin validate --strict` passes; fake-bridge + crash-injection (`SIGKILL` after `attempting` before write and after write) prove no resend; 129th pending triggers `blocked_pending_limit`; E2E disposable tmux canary proves hub send → daemon claim → `agy deliver` → sidecar `agentapi send-message` → live TUI turn → MCP `vecnet_inbox` fetch with zero hub ack. Default-off behind `VECNET_AGY_DELIVERY_CANARY=1` until all gates pass. |
| **Slice 4 (`agy acp` launcher)** | Passing `AcpBackend` smoke test against real `agy acp` covering launch, permission request/decision, follow-up prompt, cancel, and clean process teardown. |

---

## 5. Planned File Touchpoints

### Go CLI (`internal/cli/`)
- `hookinstall.go`: add `--client agy`, `.agents/hooks.json` named-hook merger, `.agents/mcp_config.json` writer (`--mcp`), git-untracked & symlink checks.
- `hook.go`, `hooksession.go`: camelCase `agy` stdin parser, `injectSteps` output encoder, `conversationId` binding & subagent isolation, `PreInvocation` turn-start vs mid-turn rate limiting.
- `agy_binding.go`, `agy_binding_linux.go`, `agy_binding_other.go`: strict v1 `agy-bindings.json` registry, `/proc` + `boot_id` + ancestry validation.
- `agy_setup.go`, `commands.go`: `isAgyEnvironment`, `prepareAgyEnrollment`, `reconcileAgyEnrollment`, `retryAgyEnrollments`, wired into `cmdEnrol` and `vecnet repair`.
- `agy_status.go`: `agy status|disable|enable|runtime-snapshot`, strict `bindings.json.agy-status.json` reader.
- `watch.go`, `internal/mcpserver/`: `--once` flag on `watch`; `vecnet_wait` MCP tool (Slice 2).
- `agy_deliver.go`, `agy_deliver_linux.go`, `agy_deliveries.go`: single-shot `SO_PEERCRED` bridge helper & `agy deliveries|resolve` (Slice 3).
- `guide.md`: add **Antigravity CLI (`agy`)** section.

### TypeScript Delivery Host & Launcher (`connectors/launcher/src/`)
- `delivery/agy-status.ts` + `test/delivery-agy-status.test.ts` (Slice 1).
- `delivery/agy-adapter.ts` + `test/agy-adapter.test.ts` (Slice 3).
- `delivery/host.ts`, `codex/delivery-main.ts`, `daemon.ts`: compose `createAgyStatus` (default) and `createAgyDeliveryAdapter` (behind `VECNET_AGY_DELIVERY_CANARY=1`).
- `connectors/agy-plugin/`: official plugin manifest, hooks, MCP config, skill, and thin `delivery-bridge` sidecar (Slice 3).

### Documentation (`docs/`)
- `docs/research/2026-11-20-agy-autodelivery.md` (this worktree).
- `docs/superpowers/specs/2026-11-20-agy-delivery-design.md` (this worktree).
- `docs/connectors/agy.md` & `docs/connectors/launcher.md` (updated as slices land).
