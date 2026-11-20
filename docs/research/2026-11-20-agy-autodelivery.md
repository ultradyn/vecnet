# Antigravity CLI (`agy`) Autodelivery Research (2026-11-20)

**Question.** How do Vecnet's current autodelivery methods work for Codex and Claude Code, and which documented Antigravity CLI (`agy`) features enable equivalent autodelivery integrated into Vecnet's existing shared-daemon, hook, MCP, and launcher architecture?

**Short answer.** Vecnet has already factored its autodelivery control plane into a shared per-user delivery host (`startDeliveryHost`), a notification-only pooled watch layer (`WatchPool`), a canonical per-user SQLite claim ledger (`delivery.sqlite`), and per-client Go CLI binding/transport helpers. Antigravity CLI (`agy` **1.5.13**) exposes **four** documented, sanctioned capabilities that map cleanly onto this architecture:

1. **Lifecycle hooks (`hooks.json`)** with `SessionStart`, `PreInvocation` (runs before *every* model call and injects `injectSteps`), `PostInvocation`, and `Stop`, using per-conversation `conversationId` values (subagents get their own IDs, avoiding Codex's shared-ID hazard) and running outside the terminal sandbox.
2. **Native stdio MCP (`vecnet --net P mcp`) with per-tool `"background": "always"` and eager/lazy loading**, which is *mandatory* for ergonomic `agy` use because `agy` v1.5.7+ defaults to `auto` mode whose OS terminal sandbox blocks `~/.config` (where `vecnet/config.toml` lives) and outbound networking. A background MCP wait tool (plus a `vecnet watch --once` CLI fallback) leverages `agy`'s reactive background-task wakeup to wake idle sessions cross-platform.
3. **Antigravity Plugins + Sidecars + `agentapi`** (`agy plugin validate` confirms first-class support for `hooks`, `mcpServers`, and `sidecars`; `agentapi send-message <conversation_id> <content>` and `agentapi get-conversation-metadata <conversation_id>` are documented sidecar commands). If `agy` CLI runs plugin sidecars during interactive TUI sessions, a thin Vecnet plugin sidecar can act as an authenticated local `agentapi` bridge for `startDeliveryHost` **without ever reading, persisting, or borrowing `agy`'s internal `ANTIGRAVITY_CSRF_TOKEN`**.
4. **ACP server (`agy acp`)** speaking ACP v1 over stdio with `default` (ask) mode, `session/load`/`resume`/`list`/`close`, background wakeup streaming, and mid-turn steering—immediately compatible with Vecnet's existing `connectors/launcher/src/backends/acp/` backend for launcher-owned sessions.

Labels used below: **VERIFIED** = directly observed in this repo or against installed `agy` 1.5.13 on Linux; **DOCUMENTED** = official `antigravity.google/docs/*.md` or built-in skill docs; **UNVERIFIED (NEEDS CANARY)** = requires a disposable lab test before enabling in production.

---

## 1. Current Vecnet Autodelivery Architecture (Codex & Claude Code)

### 1.1 Shared Infrastructure (Single Per-User Delivery Host)

On branch `claude-inbox-delivery` (commit `51c7fba2`), Vecnet's autodelivery stack has converged onto one shared per-user runtime shared by both the standalone systemd/Task Scheduler service (`connectors/launcher/src/codex/delivery-main.ts`) and the embedded launcher (`connectors/launcher/src/daemon.ts`):

| Layer | Implementation | Responsibility & Invariants |
| --- | --- | --- |
| **Shared delivery host** | `connectors/launcher/src/delivery/host.ts` (`startDeliveryHost`, `HostedAdapter`, `composeHosted`) | Elects the single per-user lease (`~/.local/state/vecnet/codex/bindings.json.status.json.lock`), runs the Codex connector plus composed `HostedAdapter` auxiliaries, holds the lease through shutdown/drain, and stops all adapters on lock loss. |
| **Shared watch pool** | `connectors/launcher/src/delivery/watch-pool.ts` (`createWatchPool`, `spawnWatchChild`) | Reference-counted `vecnet watch --json` children keyed by `(hubIdentity, endpoint, streamOwner, credentialSlot, scope)` and credential revision. Strictly notification-only: emits only `WakeEvent` (`wake` \| `wake_idle`), never bodies, delivery IDs, or acks. |
| **Canonical claim ledger** | `~/.local/state/vecnet/codex/delivery.sqlite` via `openLedger` (`connectors/launcher/src/ledger.ts`) | Shared per-user SQLite KV partitioned by namespace (`ledger.kv('codex')`, `ledger.kv('claude')`). Used by both standalone and launcher modes so host-mode switches recover the exact same durable claims. |
| **Enrollment & repair** | `internal/cli/commands.go` (`cmdEnrol`), `localsetup.go`, `claude_setup.go`, `repair` | Pre-redemption durable setup intent (`enrollments.json`, `claude-enrollments.json`) committed *before* redeeming one-use hub invitations, paired with config witnesses (`EnrollmentCaller`, `EnrollmentHub`, `EnrollmentIntent`, `EnrollmentCredential`). `vecnet repair` retries pending setup without re-inviting or transferring ownership. |
| **Shared hook core** | `internal/cli/hook.go`, `hookinstall.go`, `hooksession.go`, `hookstate.go` | Inert untrusted-data framing (`<vecnet-inbox untrusted="true">`, `hookTrust`, HTML/bidi flattening, 300-rune excerpts, 6000 UTF-16 unit cap, 4096-byte atomic pipe cap), per-profile state lock (`state/<profile>.json`), display proof before any optional ack, and session binding (`+sessions.json`). |
| **Operator-guard env list** | `internal/cli/operator.go` | Already recognizes `ANTIGRAVITY_AGENT` (line 62) alongside `CLAUDECODE` and `CODEX_THREAD_ID` to prevent bare operator commands inside agent sessions. |

### 1.2 Codex Autodelivery (Daemon App-Server Push)

*Reference: `docs/connectors/codex.md`, `docs/connectors/codex-appserver.md`, `docs/research/2026-11-04-codex-app-server-delivery.md`, `connectors/launcher/src/codex/`, `internal/cli/codex*.go`.*

- **Why hooks are signal-only:** Codex subagents can inherit the parent's `session_id` and fire `SessionStart`/tool hooks; hook `additionalContext` cannot prove root consumption and cannot wake an idle session. `.codex/hooks.json` is therefore strictly signal-only (no polling, claiming, or peer text).
- **Exact root ownership:** `vecnet enrol` inside a Codex root reads `CODEX_THREAD_ID` and `CODEX_HOME`, writing to `$HOME/.local/state/vecnet/codex/bindings.json` (mode `0600`) with a random generation and TTL (default 24h, renewable). Moving a recipient requires addressed CAS (`--expect-generation`).
- **Transport:** One shared Codex app-server daemon per `CODEX_HOME` at `$CODEX_HOME/app-server-control/app-server-control.sock` (WebSocket JSON-RPC, same-UID `0600` socket).
- **Delivery mechanics:**
  - Idle `wake` / `wake_idle`: `turn/start` with a fixed visible stub in `input` and untrusted peer blocks in `additionalContext` (`kind: "untrusted"`, never `application`, never policy/model/cwd overrides).
  - Busy `wake`: `turn/steer` with `expectedTurnId`; `wake_idle` waits for `thread/status/changed` → `idle`; approval/input wait never manufactures answers.
  - `hold`: stays in inbox; never pushed or bundled with `wake`.
  - Receipt recovery: passes `clientUserMessageId` and scans `thread/items/list` / `thread/turns/list {itemsView:"full"}` to settle uncertain sends before retry.
  - Keeps bound threads loaded by subscribing via `thread/resume {threadId, excludeTurns:true}` (preventing Codex's 60s no-subscriber unload).
- **Coverage limits:** Does not reach `--no-daemon`, `codex exec`, desktop app, or Paseo/T3 stdio servers.

### 1.3 Claude Code Autodelivery (Dual Path: Legacy Hooks+Monitor & Shared-Daemon Native Canary)

*Reference: `docs/connectors/claude-code.md`, `docs/connectors/claude.md`, `docs/superpowers/specs/2026-11-19-claude-delivery-design.md`, `connectors/launcher/src/delivery/claude-{adapter,status}.ts`, `internal/cli/claude_*.go`.*

1. **Shipped default path (Hooks + Monitor + MCP):**
   - `vecnet hook install --profile P --mcp --write` merges `UserPromptSubmit` and `PostToolUse` (`matcher: "*"`, timeout 5s) into `<git-root>/.claude/settings.local.json` and registers local-scope MCP server `vecnet-P` (`vecnet --net P mcp`).
   - Active-turn delivery: `vecnet hook` emits `hookSpecificOutput.additionalContext` (`<vecnet-inbox untrusted="true">`); `post-tool` is rate-limited to 5s; `agent_id` in stdin suppresses subagents; `session_id` + `CLAUDE_PID` binds one profile to one session (surviving `--resume` and `/clear`).
   - Idle wakeup: agent runs a persistent Claude Monitor executing `vecnet --net P watch --for-monitor` (line-oriented wakeup, 30-minute Monitor cap, lossless replay on re-arm).
   - Multi-session isolation in one repo: `VECNET_PROFILE=P claude` suppresses foreign profiles' hooks and MCP relays.
2. **Shared-daemon native delivery (Slices 1–3 implemented; canary behind `VECNET_CLAUDE_DELIVERY_CANARY=1`):**
   - **Registration (`claude_binding.go`, `claude_binding_linux.go`, `claude_setup.go`):** `SessionStart` and first `UserPromptSubmit` passively register the exact root into `claude-bindings.json` using `CLAUDE_PID`, `CLAUDE_CODE_SESSION_ID`, `CLAUDE_CODE_VERSION` (exact-match build allowlist, Linux x86-64 `2.1.322`), `CLAUDE_CONFIG_DIR`, `CLAUDE_CODE_MESSAGING_SOCKET`, verified via `/proc` start time, `boot_id`, kernel parent ancestry, and Claude's own `sessions/<pid>.json`.
   - **Passive status sidecar (`claude-status.ts`, `claude_status.go`):** `startDeliveryHost` publishes `bindings.json.claude-status.json` with host PID/start/boot and opaque binding keys/generations/states.
   - **Native transport (`claude-adapter.ts`, `claude_deliver.go`):**
     - `WatchPool` wakes trigger a coalesced catch-up pass reading `vecnet inbox --json --limit 32 --no-display-proof --since <cursor>`.
     - Stages bounded ordered events (`hold` quarantined as `held`; `wake_idle` skipped onto legacy path because Claude has no receiver-enforced atomic idle gate; unknown event/message kinds fail closed and stall the window).
     - Enforces a 128-claim unresolved pending cap per scope (`blocked_pending_limit`).
     - Commits durable `reserved` → `attempting` in `ledger.kv('claude')` *before* spawning single-shot Go helper `vecnet claude deliver --binding-key K --expect-generation G ...`.
     - Helper re-verifies registry under lock, connects to Unix socket, verifies `SO_PEERCRED` + `/proc` start identity on that exact socket before writing one ≤4 KiB LF-terminated pointer-only JSON frame (`<cross-session-message from-name="Vecnet">…`) with a 1s deadline and zero retries. Exit codes: `0` write completed (never presentation proof), `2` validation/refusal before write, `3` connect refused/timeout (definitely unwritten), `4` write failure/partial (uncertain).
     - **Never auto-acks the hub.** Restart recovery releases foreign `reserved` claims and quarantines foreign `attempting` claims as `uncertain` (operator resolves via `vecnet --net P claude deliveries` and `claude resolve --keep-inbox | --retry --confirm-duplicate-risk`).

### 1.4 Cross-Client Invariants Any `agy` Integration Must Preserve

1. **One per-user delivery host** (`startDeliveryHost`, same singleton lock, same `WatchPool`, same `delivery.sqlite`, versioned sidecar `bindings.json.agy-status.json`).
2. **Exact root identity & generation fencing**—never guess by CWD, recency, or alias; one active owner per `(hub_identity, recipient)`.
3. **Never touch host permissions, sandbox policy, or steal internal host secrets/tokens.**
4. **Untrusted peer text discipline**—pointer-only first for any push path; never inject peer-controlled content into elevated `system` roles without verified API-request evidence.
5. **Claim-before-write & no silent loss**—commit `attempting` before bytes leave; never auto-retry uncertain writes; `hold` stays inbox-only; never auto-ack the hub.
6. **Exact-build qualification & durable opt-outs**—unknown/unqualified platforms stay on the safe active-turn/inbox path; global and profile `disable`/`enable` survive restart and repair.

---

## 2. Antigravity CLI (`agy` 1.5.13) Feature Audit

Installed binary verified on this Linux host: `/home/xertrov/.local/bin/agy`, version `1.5.13`.

### 2.1 Environment Variables & Session Identity (VERIFIED)

Observed exported environment variable *names* inside an `agy` 1.5.13 `run_command` shell:

- `ANTIGRAVITY_AGENT` — host-tree detection marker (already in `internal/cli/operator.go:62`).
- `ANTIGRAVITY_CONVERSATION_ID` — exact UUID of the active conversation (analog of `CODEX_THREAD_ID` / `CLAUDE_CODE_SESSION_ID`).
- `ANTIGRAVITY_TRAJECTORY_ID` — trajectory identifier.
- `ANTIGRAVITY_APP_DATA_DIR` — canonical app data directory (e.g., `~/.gemini/antigravity-cli`, analog of `CODEX_HOME` / `CLAUDE_CONFIG_DIR`).
- `ANTIGRAVITY_PROJECT_ID` — project identifier (default `default-cli-project`).
- `ANTIGRAVITY_LS_VERSION` — observed Antigravity language-server/CLI version string (analog of `CLAUDE_CODE_VERSION`, eliminating version guessing).
- `ANTIGRAVITY_AGENTAPI_EXE` — path to the bundled `agentapi` helper (`~/.gemini/antigravity-cli/bin/agentapi`).
- `ANTIGRAVITY_LS_ADDRESS` & `ANTIGRAVITY_CSRF_TOKEN` — internal per-session language server address and CSRF secret. **Security boundary:** Vecnet MUST NEVER read, log, persist, or forward `ANTIGRAVITY_CSRF_TOKEN` or talk directly to `ANTIGRAVITY_LS_ADDRESS` (mirrors Claude Code's strict prohibition against borrowing own-child tokens).

**UNVERIFIED (NEEDS CANARY):**
- Which subset of these environment variables is exported to `hooks.json` handlers, `mcp_config.json` stdio servers, and plugin `sidecar.json` processes?
- Whether `agy` exports an explicit `AGY_PID` or if parent process ancestry (`/proc/<ppid>`) identifies the owning `agy` process.
- Whether `ANTIGRAVITY_CONVERSATION_ID` changes on `/clear` and `/fork`, and stays identical on `--conversation <id>` / `/resume` (DOCUMENTED: `/clear` starts a new conversation session; `/fork` allocates a new unique session ID; `--conversation` / `/resume` keeps the ID).

### 2.2 Terminal Sandbox & Why MCP-First Is Mandatory (DOCUMENTED / VERIFIED IN CHANGELOG)

- Since `agy` v1.5.7, interactive and headless sessions default to **`auto` mode** (where the OS terminal sandbox is available: Linux kernel namespaces, macOS `sandbox-exec`, Windows 11 24H2+ AppContainer).
- **Critical constraint (verified in `agy changelog`):** The terminal sandbox **blocks sandboxed commands from reading `~/.config` (`XDG_CONFIG_HOME`) and Windows `AppData\Roaming`**, AND runs **without outbound network access** by default!
- **Architectural consequence for Vecnet:**
  - `vecnet` stores credentials in `~/.config/vecnet/config.toml` and state in `~/.config/vecnet/state/` / `~/.local/state/vecnet/`. Inside `agy`'s default `auto` mode, ordinary `run_command` invocations of `vecnet` will fail inside the sandbox and require an unsandboxed command evaluation/approval.
  - By contrast, **MCP servers (`mcp_config.json`)** and **lifecycle hooks (`hooks.json`)** are spawned directly by `agy` **outside** the terminal command sandbox!
  - Therefore, for `agy`, **MCP (`vecnet --net P mcp`) must be the primary tool surface**, and hooks handle active-turn notification without asking the user to poke `~/.config` or network holes in their terminal sandbox.

### 2.3 Lifecycle Hooks (`hooks.json`) (DOCUMENTED + VALIDATED VIA `agy plugin validate`)

*Reference: `https://antigravity.google/docs/hooks.md`, `agy-customizations/docs/hooks.md`, `build-plugins.md`.*

- **Discovery locations:**
  1. Workspace: `<workspace>/.agents/hooks.json` (walks from CWD up to repo root containing `.git`).
  2. Global: `~/.gemini/config/hooks.json` (note: hooks in `~/.gemini/antigravity-cli/settings.json` are **not** loaded).
  3. Plugin: `~/.gemini/config/plugins/<name>/hooks.json` (or `.agents/plugins/<name>/hooks.json`).
- **Schema differences from Claude Code and Codex:**
  - Top-level is a map of **named hook groups** (`{"vecnet-P": { ... }}`), NOT `{"hooks": {...}}`.
  - `SessionStart`, `PreInvocation`, `PostInvocation`, and `Stop` take **flat arrays** of handlers (`[{"type": "command", "command": "...", "timeout": 5}]`). Only `PreToolUse` and `PostToolUse` wrap handlers in `{"matcher": "...", "hooks": [...]}`.
  - Hook commands run via `sh -c` (`cmd /c` on Windows) with **process CWD = directory containing `hooks.json`** (while JSON `stdin.cwd` is the agent's workspace CWD).
  - Synchronous execution; default timeout 30s; non-zero exit fails open.
- **Input JSON contract (camelCase):**
  - Common fields on every event: `conversationId`, `workspacePaths`, `transcriptPath` (`~/.gemini/antigravity-cli/brain/<id>/.system_generated/logs/transcript.jsonl`), `artifactDirectoryPath`, `modelName`, `lastUserInput`, `hookEventName`, `cwd`.
  - `PreInvocation` / `PostInvocation` add `invocationNum` (0-indexed per turn!) and `initialNumSteps`.
  - `Stop` adds `executionNum`, `terminationReason` (`NO_TOOL_CALL`, `MAX_INVOCATIONS`, `ERROR`, `USER_CANCELED`, etc.), `error`, `fullyIdle`, `continuationActive`.
- **Output JSON contract:**
  - `SessionStart`, `PreInvocation`, `PostInvocation`:
    ```json
    {
      "injectSteps": [
        { "systemMessage": { "systemMessage": "...", "metadata": { "user_markdown": "..." } } }
      ]
    }
    ```
    Also supports `{"userMessage": "..."}` and `{"errorMessage": {"userMessage": "..."}}` (`ephemeralMessage` is deprecated). `PreInvocation` supports `"terminationBehavior": "terminate"`; `PostInvocation` supports `"terminationBehavior": "force_continue"`.
  - `PostToolUse`: expects `{}` (cannot inject context).
  - `Stop`: `{"decision": "continue", "reason": "..."}` (re-enters loop with `reason` as a system message; check `continuationActive` to prevent loops).
- **Why this fits Vecnet exceptionally well:**
  1. **`PreInvocation` replaces both `UserPromptSubmit` and `PostToolUse`:** `invocationNum == 0` fires right after the user prompt (immediate poll), and `invocationNum > 0` fires before every subsequent model call in the turn (rate-limited at 5s, identical to Claude's `post-tool`!).
  2. **Subagent isolation is structural:** Antigravity gives every subagent its own `conversationId` (`docs/subagents.md`). Binding a Vecnet profile to the exact root `conversationId` automatically prevents subagents from ever claiming or seeing root deliveries—even without an `agent_id` field!
  3. **`SessionStart` matches Claude's silent passive registration hook:** Runs once at session start, performs bounded local registration with zero hub calls and returns `{}`.
  4. **`metadata.user_markdown` separates UI chrome from model text:** Allows showing a concise user-facing banner while keeping model text tightly controlled.
- **UNVERIFIED (NEEDS CANARY):**
  - Exact API role and prompt placement of `injectSteps[].systemMessage` vs `injectSteps[].userMessage` in the outbound model request (must confirm untrusted peer text never lands in a privileged system-instruction block; pointer-only `systemMessage` is safe by construction since no peer bytes enter it).
  - Whether `invocationNum` resets to `0` on each new user turn or increments monotonically across the whole conversation (determines how `PreInvocation` distinguishes prompt-start vs mid-turn!).
  - Exact git worktree discovery behavior for `.agents/hooks.json` (worktree root vs main checkout root).
  - Whether `PreInvocation` fires for subagents (and confirming their `conversationId` differs from the parent's).

### 2.4 MCP Configuration, Eager/Lazy Tools, and Background Task Wakeup (DOCUMENTED + VALIDATED)

*Reference: `https://antigravity.google/docs/mcp.md`, `agy mcp add --help`, `agy plugin validate`.*

- **Config locations:**
  - Workspace: `<workspace>/.agents/mcp_config.json` (project-scoped, analog of Claude Code `--scope local`).
  - Global: `~/.gemini/config/mcp_config.json` (managed by `agy mcp add|remove|list|enable|disable`).
  - Plugin: `<plugin-root>/mcp_config.json` (namespaced `<plugin>_<server>`).
- **Rich per-tool options (`mcp_config.json`):**
  - `"timeoutSeconds"`: default `180`s per call, `300`s to connect; negative (e.g. `-1`) disables timeout, or set a bounded window like `1800` (30 min).
  - `"tools": { "<tool>": { "eager": true, "background": "always" } }`:
    - `eager: true` registers core tools (`vecnet_inbox`, `vecnet_reply`, `vecnet_ack`, `vecnet_send`) directly in native tool declarations; other tools stay lazy-loaded to save context tokens.
    - **`background: "always"`** executes calls to that MCP tool as a **background task**!
- **Reactive Wakeup on Background Task Completion (DOCUMENTED):**
  - In `agy`, background tasks (both `run_command` with `WaitMsBeforeAsync` and background MCP tools) automatically wake the idle agent when they complete—without polling!
  - **Key difference from Claude Code's Monitor:** Claude Code's Monitor wakes on *every stdout line* of a long-lived `vecnet watch --for-monitor` process. `agy` background tasks wake when the **task finishes** (exits / returns).
  - **Design implication:** A one-shot waiter—specifically:
    - **Primary (sandbox-safe, cross-platform):** an MCP tool `vecnet_wait` on `vecnet --net P mcp` configured with `"background": "always"` and bounded timeout (e.g., 15–30 min), returning a **pointer-only** wake notice as soon as a `wake` or `wake_idle` event arrives (never returning raw untrusted bodies as instructions, never auto-acking). When it completes, `agy` wakes the idle session; the agent fetches `vecnet_inbox`, handles it, and re-arms `vecnet_wait`.
    - **CLI fallback:** `vecnet --net P watch --once --for-monitor` (exits cleanly after the first wake delivery line), runnable via `run_command` outside the sandbox.
- **UNVERIFIED (NEEDS CANARY):**
  - Confirm that an MCP tool with `"background": "always"` wakes an **idle interactive `agy` TUI session** upon returning, and remains alive while the agent is idle between turns (not canceled on turn end).
  - Confirm exact timeout / cancellation behavior when the user sends a prompt or exits `/exit` while `vecnet_wait` is pending.
  - Check whether `agy mcp add` writes only globally (`~/.gemini/config/mcp_config.json`) or supports a workspace flag (in `agy mcp add --help`, there is NO `--scope` flag—workspace MCP configs must be written cleanly to `<workspace>/.agents/mcp_config.json`!).

### 2.5 Plugins, Sidecars, and `agentapi` (DOCUMENTED + VALIDATED VIA `agy plugin validate` & `agentapi --help`)

*Reference: `https://antigravity.google/docs/sidecars.md`, `https://antigravity.google/docs/build-plugins.md`, `agy changelog`.*

- **`agentapi` CLI (VERIFIED via `/home/xertrov/.gemini/antigravity-cli/bin/agentapi --help`):**
  ```text
  Usage: agentapi <command> [args]

  Available Commands:
    get-conversation-metadata <conversation_id>
    new-conversation [--model=<flash_lite|flash|pro|model_id>] [--title=<title>] [--profile=<profile>] <prompt>
    send-message [--title=<title>] <recipient_id> <content>
  ```
- **Sidecar support (VERIFIED via `agy plugin validate --json` + `agy changelog`):**
  - `sidecars.md` documents global (`~/.gemini/config/sidecars/<id>/sidecar.json`) and plugin (`~/.gemini/config/plugins/<plugin>/sidecars/<name>/sidecar.json`) sidecars, explicitly enabled by the user in `~/.gemini/config/config.json` under `"sidecars": { "<plugin>/<name>": { "enabled": true } }`.
  - `agy plugin validate --json` on `agy` 1.5.13 explicitly recognized and validated `"sidecars": 1`, `"hooks": 1`, and `"mcpServers": 1` with **zero errors and zero warnings**.
  - `agy changelog` explicitly notes a CLI bugfix for *"long sessions with plugins that run sidecars"*, suggesting sidecars run in CLI sessions as well as Antigravity App (despite `sidecars.md`'s header currently saying `Available on: Antigravity App`).
- **Why this is the cleanest candidate for true zero-model-setup daemon push:**
  - Unlike Codex (shared daemon socket) and Claude Code (per-session UDS inbox socket), `agy` does **not** document a standalone external Unix socket for arbitrary processes to inject turns into an interactive TUI.
  - Instead, **a Vecnet plugin sidecar supervised by `agy` itself** is the *sanctioned* process that receives `agentapi` access from `agy`!
  - Architectural separation of duties:
    - The **Vecnet plugin sidecar** holds NO hub credentials, runs NO hub watches, and keeps NO claim ledger. It exposes a single private per-instance Unix socket (`0600`, `$XDG_RUNTIME_DIR/vecnet/agy-bridge-<pid>.sock`) verified by `SO_PEERCRED` + `/proc` start time + boot ID, validates the incoming pointer-only request against its live `conversationId`, and executes `agentapi send-message <conversation_id> <pointer>` (and `get-conversation-metadata` for liveness).
    - The **shared Vecnet delivery host** (`startDeliveryHost`) retains 100% of the control plane: `WatchPool`, `delivery.sqlite` (`ledger.kv('agy')`), 128 pending cap, `reserved` → `attempting` fencing, single-shot Go helper `vecnet agy deliver`, status sidecar `bindings.json.agy-status.json`, and `vecnet --net P agy deliveries|resolve`.
- **CRITICAL UNVERIFIED QUESTIONS (MUST BE PROBED IN SLICE 0 BEFORE BUILDING SLICE 3):**
  1. Does `agy` CLI 1.5.13 actually launch enabled plugin/global sidecars during an interactive TUI session, and what environment variables (`ANTIGRAVITY_CONVERSATION_ID`? `PATH` with `agentapi`?) does the sidecar inherit?
  2. When `agentapi send-message <conversation_id> <content>` targets a live interactive `agy` TUI session:
     - Does it wake an **idle** TUI and stream the turn live on screen?
     - What happens if the TUI is **busy** (running a tool, streaming, or waiting for tool approval)? Does it queue, steer, interrupt, or reject?
     - What exact prompt role (`user` vs `system` vs peer wrapper) reaches the model, and how does it render in the TUI?
     - What does `agentapi get-conversation-metadata <conversation_id>` return (status/idle/busy/approval fields)?
     - Does `agentapi send-message` deduplicate or can repeated calls deliver twice? What exit codes distinguish rejected/not-found vs delivered?
     - Does sidecar execution require explicit opt-in in `~/.gemini/config/config.json` (and how should `agy status` surface that cleanly without mutating user consent)?

### 2.6 ACP (`agy acp`) for Launcher-Owned Sessions (VERIFIED)

*Reference: `https://antigravity.google/docs/ide/extensions/acp.md`, `connectors/launcher/src/backends/acp/index.ts`.*

Live handshake probe against `agy acp` (1.5.13) verified:
- `protocolVersion: 1`, `agentInfo: { name: "Antigravity ACP Server", version: "1.5.13" }`.
- `session/new` returns `modes.availableModes` containing `id: "default"` (`Ask before edits and commands, per your permission settings.`), plus `accept-edits` and `plan`.
- `agentCapabilities` advertises `loadSession: true`, `sessionCapabilities: { resume, close, load, list, additionalDirectories }`, and `_meta.antigravity.systemPrompt.append: true`.
- `ide/extensions/acp.md` documents **mid-turn steering** (*"Sending a prompt while a turn runs steers that turn instead of starting a new one"*) and `agy` v1.5.10+ fixed immediate streaming of background-task wakeups after a turn ends.
- **Compatibility with Vecnet's existing ACP backend (`connectors/launcher/src/backends/acp/index.ts`):**
  - `AcpBackend` already accepts any non-Codex absolute binary speaking ACP v1 with an `ask`-equivalent `mode_id` (`"default"` passes `/bypass|full.access|accept.?edits|auto/i` rejection).
  - Launcher setup works with the existing backend contract:
    ```sh
    vecnet --net PROFILE launcher setup --backend acp \
      --acp-command /absolute/path/to/agy --acp-arg=acp \
      --acp-provider agy --acp-mode default \
      --project PROJECT --repo /abs/repo --base-ref master --write
    ```
  - Optional future enhancement: enable `steer: true` and `resume` support in `AcpBackend` once `agy acp` steering and `session/resume` are canary-verified.

### 2.7 Features Evaluated & Explicitly Rejected for Interactive TUI Injection

| Feature | Why Rejected for Interactive TUI Autodelivery |
| --- | --- |
| **`agy -p ... --conversation <id>`** | Spawns a separate headless process against saved trajectory state rather than driving a live TUI's in-memory session loop; conflicts with active TUI presence locks (`~/.gemini/antigravity-cli/presence/<id>.lock`) and would diverge from the terminal UI. Useful ONLY for disposable canary harnesses (`--input-format stream-json --output-format stream-json`). |
| **Direct calls to `ANTIGRAVITY_LS_ADDRESS` / `ANTIGRAVITY_CSRF_TOKEN`** | Internal per-session secret and undocumented RPC surface. Violates Vecnet's core security boundary (identical to refusing Claude Code's own-child token). |
| **`agy remote-control`** | Cloud reverse tunnel to `antigravity.google.com` requiring Google Account sign-in; not a local machine-private IPC surface. |
| **Tmux / PTY keystroke injection** | Explicitly prohibited across Vecnet (breaks framing, races user typing, can submit unintended commands/approvals). |
| **Transcript scraping (`transcript.jsonl`) as ack** | Lab oracle only, never a production settlement or acknowledgement API. |

---

## 3. Comparative Matrix: Codex vs. Claude Code vs. Antigravity CLI (`agy`)

| Capability | Codex (Shipped) | Claude Code (Hooks+Monitor / Native Canary) | Antigravity CLI (`agy` Proposed) |
| --- | --- | --- | --- |
| **Host detection env** | `CODEX_THREAD_ID`, `CODEX_HOME` | `CLAUDECODE`, `CLAUDE_PID`, `CLAUDE_CODE_SESSION_ID`, `CLAUDE_CODE_VERSION` | `ANTIGRAVITY_AGENT`, `ANTIGRAVITY_CONVERSATION_ID`, `ANTIGRAVITY_APP_DATA_DIR`, `ANTIGRAVITY_LS_VERSION` |
| **Subagent isolation** | Unsafe in hooks (shared `session_id`); safe via exact-thread RPC | `agent_id` in hook stdin; root-only socket binding | **Native:** every subagent has its own `conversationId` |
| **Active-turn hook delivery** | Signal-only (no peer content) | `UserPromptSubmit` + `PostToolUse` (`additionalContext`) | `PreInvocation` (`injectSteps`; `invocationNum==0` immediate, `>0` 5s rate limit) + `SessionStart` (passive reg) |
| **Hook config location** | `.codex/hooks.json` | `.claude/settings.local.json` | `.agents/hooks.json` (workspace) or plugin `hooks.json` |
| **MCP transport & sandbox** | Optional MCP | Local-scope `claude mcp add`; CLI also works | **MCP-first required** (`.agents/mcp_config.json` / plugin) because `auto` sandbox blocks `~/.config` & network |
| **Agent-armed idle wakeup** | N/A (daemon handles) | Claude Monitor (`vecnet watch --for-monitor`, 30m cap) | **Background MCP `vecnet_wait`** (`background: "always"`, sandbox-safe, cross-platform) + `watch --once` CLI fallback |
| **Daemon-driven idle push** | Shared app-server WS over Unix socket (`turn/start`) | Per-session UDS inbox (`claude deliver` + `SO_PEERCRED`, canary) | **Plugin Sidecar + `agentapi send-message`** bridged to `startDeliveryHost` via private `SO_PEERCRED` socket (gated on Slice 0 canary) |
| **Busy / mid-turn push** | `turn/steer` (verified) | Enters mid-turn as API `role: "system"` (canary pointer-only) | Active turns covered by `PreInvocation` hook; sidecar busy behavior **NEEDS CANARY** |
| **`wake_idle` support** | Native (`thread/status/changed` → idle) | Legacy/pending (no receiver atomic idle gate) | `vecnet_wait` wakes only when idle; sidecar `wake_idle` stays off unless atomic idle gate is proven |
| **Shared daemon integration** | Primary owner in `startDeliveryHost` | `createClaudeStatus` + `createClaudeDeliveryAdapter` (`ledger.kv('claude')`) | `createAgyStatus` + `createAgyDeliveryAdapter` (`ledger.kv('agy')`, `bindings.json.agy-status.json`) |
| **Launcher-owned jobs** | Native Codex backend | ACP backend (`claude-agent-acp`) | **ACP backend (`agy acp`, `--acp-mode default`)** — already protocol-compatible |

---

## 4. Required Pre-Implementation Canary Probes (Slice 0)

Before implementing Slice 1's hook payload format, Slice 2's background MCP waiter, or Slice 3's sidecar bridge, run these disposable lab checks (using isolated temp `HOME` / workspace and disposable loopback hub/model—never a user's working session):

1. **Hook environment & framing probe:**
   - Install a minimal `.agents/hooks.json` with `SessionStart`, `PreInvocation`, and `Stop` in a disposable workspace (and a linked git worktree).
   - Capture raw stdin JSON and env variable names across: initial turn, multi-tool turn (`invocationNum` behavior), `/clear`, `/fork`, `--conversation <id>`, and a spawned subagent (`conversationId` distinctness).
   - Inspect outbound model request (or transcript) for `injectSteps[].systemMessage` vs `injectSteps[].userMessage` to confirm untrusted framing and `metadata.user_markdown` rendering.
2. **Background MCP `vecnet_wait` probe:**
   - Configure a tiny disposable stdio MCP server in `.agents/mcp_config.json` with `"tools": {"test_wait": {"background": "always"}}` that sleeps 5s then returns a marker.
   - Verify in an interactive disposable `agy` tmux session that the agent ends its turn cleanly while `test_wait` runs, and that `agy` **reactively wakes the idle session** when `test_wait` returns (and cleans up cleanly on `/exit`).
3. **Plugin sidecar + `agentapi` probe:**
   - Install a disposable plugin with `sidecars/probe/sidecar.json` enabled in a disposable `~/.gemini/config/config.json`.
   - Verify whether interactive `agy` CLI 1.5.13 spawns the sidecar, what env vars it receives, and what `agentapi get-conversation-metadata <id>` and `agentapi send-message <id> <marker>` do when the target TUI is (a) **idle**, (b) **running a tool**, (c) **waiting on tool approval**, and (d) **posted twice with the same content** (dedup check).
   - Capture both TUI rendering and model request role/framing.
4. **ACP launcher smoke probe:**
   - Run Vecnet's existing ACP backend test/smoke against `agy acp` (`mode_id: "default"`), verifying `launch`, `send`, `session/request_permission` round-trip, `interrupt` (`session/cancel`), and `settle`.

**Decision rule:** Slices 1, 2, and 4 have zero dependency on sidecar availability in `agy` CLI. If Probe 3 shows sidecars are App-only or `agentapi send-message` cannot safely wake an interactive CLI TUI, Slice 3 stays deferred while Slices 1 + 2 + 4 still deliver full active-turn hooks, sandbox-safe MCP, reactive idle wakeup (`vecnet_wait`), shared-daemon status/enrollment, and ACP launcher support!

---

## 5. Slice 0 Results (2026-11-20, `agy` 1.5.13, Linux x86-64)

Harness: [`scripts/agy-canary-probe.py`](../../scripts/agy-canary-probe.py). Run all probes, or one with `--probe <name>`. Sanitized evidence: [agy-1.5.13.json](evidence/agy-1.5.13.json). Every probe runs `agy` under a throwaway `HOME` with a dummy `GEMINI_API_KEY`, `modelProvider: "gemini"`, and a loopback Gemini API stub. The real `~/.gemini` is never touched and no model tokens are spent. The TUI probe seeds `cache/onboarding.json` in the throwaway HOME, recording that the ToS was accepted (Max approved this on 2026-11-20) and that interactions-data collection is opted out, so a fresh HOME skips onboarding. Model labels such as "Gemini 3.1 Pro (Low)" are just agy's API-key default; the stub answers every request.

**Harness note.** A custom endpoint is selected with the `GOOGLE_GEMINI_BASE_URL` environment variable (changelog 1.5.x). `customModelsConfig.customModels.<id>.modelName` (`gemini-api://…`) does **not** redirect the host: with only that set, `agy` connected to Google's public API on :443 and failed with a 404. The first attempts in this conversation failed for exactly that reason, not because of hook or agy behavior.

These results override the earlier sections of this document wherever the two disagree:

| # | Question | Result |
| --- | --- | --- |
| H1 | `invocationNum` per turn or cumulative? | **Resets to 0 on every user turn.** Turn 1: `PreInvocation` 0, 1 (after tool), turn 2: 0 again. `initialNumSteps` grows across the conversation. |
| H2 | Events and payloads | `SessionStart` → (`PreInvocation` → `PostInvocation`)×N → `Stop`, per turn; `SessionStart` fires once per conversation. Fields as documented; `Stop` also has `executionNum`, `fullyIdle`, `continuationActive`, `terminationReason`. |
| H3 | Hook env | Only `ANTIGRAVITY_CONVERSATION_ID` (+ inherited env) reaches hook processes; it matches payload `conversationId`. **No `ANTIGRAVITY_CSRF_TOKEN`/`LS_ADDRESS`/`AGENT`/`APP_DATA_DIR` in hooks.** Hook process cwd is the `.agents/` dir that holds `hooks.json`. |
| H4 | `injectSteps[].userMessage` framing | Becomes a new `USER_INPUT` step (`source: SYSTEM_SDK`) wrapped in the same **`<USER_REQUEST>\n…`** envelope as a real user prompt, and **overwrites `lastUserInput`** for later hooks until the next real user input. **Unsafe for peer content; Vecnet must never use `userMessage`.** |
| H5 | `injectSteps[].systemMessage` framing | Role `user`, wrapped as `The following is a <SYSTEM_MESSAGE> not actually sent by the user … The following system message was injected by a <Event> hook:\n<text>\n</SYSTEM_MESSAGE>`. At `invocationNum==0` it is a separate user-role content. At `invocationNum>0` (and from `PostInvocation`) it is **appended inside the preceding `functionResponse` output** (`where: contents.functionResponse`, role `model`). It stays in the trajectory for later requests. Never in `systemInstruction`. |
| H6 | `metadata.user_markdown` | **Contradicts docs:** when `user_markdown` is set, the `systemMessage` text was **not sent to the model at all** (nor written to the transcript). Use it only for UI-only banners, never paired with text the model must see. |
| H7 | `ephemeralMessage` (deprecated) | Delivered raw as user-role text with no framing. Do not use. |
| H8 | Hooks discovery in worktrees | `.agents/hooks.json` is found from the workspace root and from subdirectories, in a main checkout and in a linked worktree. A main-checkout `hooks.json` is **not** seen from a linked worktree, so each worktree needs its own install (same as `.claude/settings.local.json`). |
| M1 | MCP `background: "always"` | Works headless. The call returns at once with `Tool is running as a background task with task id: <conv>/task-N`, the turn ends, and when the tool returns, `agy` **runs a new model call by itself**. The result comes in as role `user`: `<SYSTEM_MESSAGE>[Message] … sender=<conv>/task-N priority=MESSAGE_PRIORITY_HIGH content=Task id "…" finished with result:\n<tool text>`. Without `background`, the call blocks the turn as normal. |
| M2 | MCP tool schema | `agy` added a **required `toolSummary`** parameter to the probed MCP tool (`mcp_probe_probe_wait`); its built-in tools carry the same field. Treat it as present on every MCP tool. MCP stdio servers get no `ANTIGRAVITY_*` env (only inherited). |
| S1 | Plugin sidecars in CLI | `agy plugin validate/install/enable` all succeed and `plugin list` shows the plugin enabled. `enable` writes only `{"plugins": …}`, so the harness also adds the documented `"sidecars": {"probeplug/bridge": {"enabled": true}}` opt-in. **Even with the opt-in, no sidecar process ran in `agy -p` print mode.** (Not in the evidence file: on the real profile, `cli.log` shows no sidecar activity apart from the `BUILTIN_SIDECARS_TO_PLUGINS` migration.) **The interactive TUI (T3) didn't start the sidecar either.** |
| S2 | `agentapi` | `~/.gemini/antigravity-cli/bin/agentapi` is a shell shim for `agy agentapi` (created by an interactive install, so absent in a fresh HOME). `ANTIGRAVITY_AGENTAPI_EXE` points at the plain `agy` binary: `$ANTIGRAVITY_AGENTAPI_EXE send-message …` exits 2, while the shim exits 0. `ANTIGRAVITY_CSRF_TOKEN`/`LS_ADDRESS` are present in `run_command` children and absent from hooks and MCP servers. `agy agentapi get-conversation-metadata` run from a hook fails with `ANTIGRAVITY_LS_ADDRESS is not set`. No other auth path was found. |
| S3 | `send-message` busy | From a synchronous `run_command`: exit 0, delivered in the same turn, appended to that tool's `functionResponse` as `<SYSTEM_MESSAGE>\n[Message] timestamp=<ts> sender=<conv> priority=MESSAGE_PRIORITY_HIGH content=<text>`. |
| S4 | `send-message` idle | From a still-running daemon task after the turn ended: exit 0, and **the idle agent woke** about 4.7 s after the prompt (the task was still running), with a role `user` message in the same `[Message] … priority=MESSAGE_PRIORITY_HIGH content=<text>` format. |
| T1 | TUI idle wake by background MCP | In the interactive TUI, the turn rendered and ended; about 5 s later the background `probe_wait` returned, and agy ran a new turn on its own and rendered the reply. In the TUI, MCP connects lazily, so a tool call on the very first turn can fail with `unknown tool` (the harness warms up first). With default permissions the MCP call raises an "Allow calling this tool?" prompt, so `vecnet_wait` needs an `mcp(vecnet-P/vecnet_wait)` allow rule or user approval. |
| T2 | `/exit` with a pending background call | The status bar shows `1 task(s)`. `/exit` exits at once and kills the MCP server process. No `notifications/cancelled` arrives and stdin EOF isn't logged. |
| T3 | Plugin sidecar in TUI | With `plugin install`+`enable` plus the documented `config.json` sidecar opt-in, **the sidecar still didn't start** in the TUI. |
| T4 | `agentapi send-message` to idle TUI | Sent from a daemon task that was still running: exit 0, and the idle TUI woke and rendered the model's reply. The pointer text itself was not shown in the pane (it appears only in the model request). |
| A1 | `agy acp` | `initialize` v1; modes `default`, `accept-edits`, `plan`, with `default` current; `session/set_mode default` ok; prompt streams `agent_message_chunk` and returns `end_turn`; a `run_command` raises `session/request_permission` (`allow_once`/`reject_once`, kind `execute`), and the turn finishes after `allow_once`; `session/cancel` mid-turn gives `stopReason: "cancelled"`; `session/close` ok. `agy acp` also sends tool-less side requests (purpose not captured; probably titles), so per-request stubs must key on content. |

**Design consequences:**

1. **Active-turn hook delivery (Slice 1) works, using `systemMessage` only.** Content must be pointer-only or strictly inert-framed. `agy`'s own wrapper says "important information to pay attention to", so the Vecnet `<vecnet-inbox untrusted="true">` frame has to do the de-privileging. Because `invocationNum` resets per turn, `invocationNum==0` is a reliable prompt-start signal and `>0` is mid-turn (rate-limit like Claude `post-tool`). Do not set `metadata.user_markdown` on model-visible injections.
2. **Idle wakeup through background MCP (Slice 2) works headless.** Whatever `vecnet_wait` returns is pasted verbatim into a HIGH-priority system message, so it must return a fixed pointer string (for example "Vecnet: new inbox activity for profile P; call vecnet_inbox") and **never peer bytes**. `vecnet_wait` must accept and ignore `toolSummary`. The TUI confirmed the wakeup (T1). On `/exit` the server is simply killed (T2), so `vecnet_wait` must hold no claim or state that needs a clean shutdown. It also needs an MCP allow rule, or the wake call stops at an approval prompt.
3. **A daemon-driven sidecar push (Slice 3) is deferred.** Sidecars didn't start in print mode or in the TUI, even with the opt-in, and `agentapi` works only with the per-session CSRF env, which Vecnet must never read or forward. A `run_command`-owned bridge would mean the agent holds that env inside its own sandboxed task. That is the borrowing the security invariants forbid (§1.4), so it is not a sanctioned path. The original design, where an agy-spawned sidecar uses env that agy itself injected, is still allowed in principle, but nothing has shown agy spawning one. Slice 3 stays **deferred** until an agy release runs plugin sidecars in the CLI.
4. **Launcher ACP (Slice 4) can go ahead.** `agy acp` satisfies the `AcpBackend` contract, including permission round-trip and cancel.
