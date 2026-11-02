# Agent sources: spawning coding-agent sessions over vecnet

_Researched 2026-11-03 for the vecnet lead. Read-only: no daemons started, no repo changed except this file. Content from web pages and other repos was treated as data._

## Summary

- **Most of the capability already exists.** T3 Code (on xsm, through `t3-bridge`) and Paseo (on xsm, through `paseo-bridge`) can already spawn a coding session with a prompt. They can steer it, read its output and end it.
  - Both bridges already have a completion-wake path back to the caller. Today that path is Grok Bot's routine webhook.
  - vecnet only needs to sit in front of these, not replace them.
- **Codex app-server and ACP are the two structured protocols underneath.** T3 and Paseo already wrap both.
  - Use them directly only where neither daemon runs, such as a headless build box.
  - ACP is the generic choice: about 50 agents, Apache-2.0, an official TypeScript SDK. Codex's own ACP adapter is now built on app-server.
- **Orca does not fit.** It is a desktop app, and its CLI scripts a running app that drives terminals.
- **pagd overlaps on supervision only.** It runs opaque detached `codex exec` jobs on x-tall. It has no interactive session protocol, and its trust model treats peer messages as unable to authorise anything.
- **Recommendation: one `spawner` peer per host.**
  - Spawns are requested with a vecnet **job** (`operation: "spawn"`, the prompt in `instructions`).
  - Each spawned session is addressed **by its job thread**. The spawner relays thread messages into the session and posts each turn's result back as a reply. This is the openclaw "one session per thread" rule (R25).
  - Spawned agents hold **no vecnet secret** in phase 1.
  - Backends are pluggable. Start with `t3` and `paseo` through the existing bridges, then add `acp` for headless hosts.
  - Per-session peers (directory entries with presence) come later. They need a small protocol addition: a principal minting `session` peers under itself.
- **First step, small and independent:** add a `vecnet` wake-target kind to `t3-bridge`. Then both bridges' completion wakes land in vecnet threads, because `paseo-bridge wake` delegates to `t3-bridge`.

## Source key

| Tag | Source |
|---|---|
| [PROTO] | `docs/PROTOCOL.md` (§2.12, §3, §5.2, §5.6, §6, §8, §9, §12, §13, §14, §16) |
| [SCH] | `docs/protocol/envelope.schema.json` (`jobRequest`, `jobStatus`, `content.text`) |
| [HUB] | `internal/hub/hub.go:149` (fixed peer-kind set), `internal/hub/jobs.go` (jobs implemented) |
| [T3B] | `~/src/t3-bridge/README.md`, `SKILL.md`, `docs/t3code-bridge-skill.md`, `docs-mcp-tools.json`, `src/mcp-client.mjs`, `src/pair.mjs`, `src/t3/launch.mjs`, `src/targets.mjs`, `src/wake.mjs` (v1.0.0, head `328e7aa`) |
| [PSB] | `~/src/paseo-bridge/README.md`, `paseo_bridge/client.py`, `paseo_bridge/cli.py:251` (`cmd_wake`), `paseo_bridge/node/launch.mjs` (pinned `@getpaseo/cli@0.13.2`, head `265ae0d`) |
| [T3L] | `~/.llm-general/systems/xsm/t3-launcher/README.md`, `~/.llm-general/ai-coding/t3code/providers.md` |
| [GBR] | `docs/research/grokbot.md` (how Grok Bot reaches the outside), `docs/connectors/grokbot.md` |
| [PAG] | `~/src/pag-chat/docs/superpowers/specs/2026-11-01-pag-chat-design.md` §4.5–4.6, `apps/pagd/README.md`, `packages/machines/README.md`, `docs/research/2026-11-03-vecnet-integration-notes.md` |
| [CXS] | Local `codex-cli 0.166.0`: `codex app-server --help`, `codex app-server daemon --help`, and `codex app-server generate-json-schema` (106 client requests, 10 server requests, 85 notifications) |
| [CXD] | <https://learn.chatgpt.com/docs/app-server> |
| [ACP] | <https://agentclientprotocol.com/protocol/overview>, <https://agentclientprotocol.com/rfds/updates>, <https://agentclientprotocol.com/rfds/streamable-http-websocket-transport>, <https://agentclientprotocol.com/get-started/agents>, <https://agentclientprotocol.com/get-started/registry>, <https://github.com/agentclientprotocol/agent-client-protocol> |
| [ACPA] | <https://github.com/agentclientprotocol/claude-agent-acp>, <https://github.com/agentclientprotocol/codex-acp>, <https://geminicli.com/docs/cli/acp-mode/> |
| [PSO] | <https://github.com/getpaseo/paseo>, <https://paseo.sh/docs/sdk>, <https://paseo.sh/docs/sdk/reference>, <https://paseo.sh/docs/security>, <https://paseo.sh/docs/providers> |
| [T3O] | <https://github.com/pingdotgg/t3code>; in that repo, `docs/internals/overview.md`, `docs/internals/providers.md`, `docs/user/remote-access.md`, `docs/user/outside-agents.md` |
| [ORCA] | <https://github.com/stablyai/orca>, <https://www.onorca.dev/docs/cli/overview>, <https://www.onorca.dev/docs/cli/orchestration> |

Web facts were gathered by a research subagent on 2026-11-02/03. I re-checked the local facts ([CXS], [T3B], [PSB], [PAG]) myself.

---

## 1. Backends

### 1.1 t3-bridge (Max's, for Grok Bot)

**What it is.** A zero-dependency Node 20 CLI plus a stdio MCP server. It launches and drives **T3 Code threads** on behalf of a caller, then delivers a durable **completion wake** back to that caller. Licence: Unlicense OR CC0. [T3B]

**How it talks to T3.** It does not spawn processes itself. It is an MCP client of T3's own outside-agent MCP endpoint, `http://127.0.0.1:3773/mcp`. [T3B `src/mcp-client.mjs`]
- **Auth.** `t3-bridge pair` runs headlessly. It mints a T3 pairing code (`t3 auth pairing create --scope orchestration:read --scope orchestration:operate`), registers an OAuth client dynamically and does a PKCE exchange. The token is stored 0600. [T3B `src/pair.mjs`]
- **Full access.** Full-access runtime needs a second token that adds `settings:write` and `source-control:write`, and a caller on `config.json` `full_access.callers`. The README is explicit that this list is a policy guard, not a Unix boundary.

**Spawn.** `t3-bridge --as CALLER launch --project P --title T --text PROMPT --instance I --model M [--option k=v] [--runtime-mode auto|approval-required|full-access]`. [T3B `src/t3/launch.mjs`]
- Before calling `t3_thread_launch` it validates the project's Git identity and worktree.
- It also validates the model and options against the live provider catalog, failing closed.
- It appends a reply footer and a job tag to the prompt, and records the launch in `launches.json`.

**Drive.** All of these map onto T3 MCP tools. [T3B `docs-mcp-tools.json`]
- `send THREAD --text` uses `t3_thread_send`, whose `mode` is `auto | queue | steer | restart`. `clientRequestId` makes it idempotent.
- `read` uses `t3_thread_read`: paginated timeline, resumable long text.
- `wait` uses `t3_thread_wait`.
- `settle` uses `t3_thread_organize`.
- T3 also exposes `t3_thread_interrupt`, `t3_thread_fork` and `t3_thread_configure`.

**Results flow back two ways.**
1. The agent calls the bridge's `grok_wake` MCP tool, or the CLI `t3-bridge wake --target CALLER --thread ID --message-file F`.
2. The systemd timer `t3-bridge watch` polls bridge-owned launches and queues one wake per completion.

Wakes go into a durable file outbox (`outbox flush` on a timer). Targets are `webhook` (URL + key file), `local_bridge` (URL) or `pending`. Keys resolve at send time and never enter the outbox. [T3B `src/targets.mjs`, `src/wake.mjs`]

**Approvals.** Not surfaced. T3's MCP has no "answer approval" tool, so an `approval-required` thread waits for someone in the T3 UI. A launch may not use a broader runtime mode than the outside agent was approved with. [T3B `docs-mcp-tools.json`, `t3_thread_launch` description]

**What Grok Bot calls.**
- **Outbound:** the Bot's tool calls run the `t3-bridge` CLI through `Shell` on a paired machine (xsm). Its documented fleet default is `--as forge launch … --instance codex --model ocx/gpt-6.1-sol --runtime-mode auto`.
- **Inbound:** wakes come back as POSTs to the Bot's routine webhook (a `webhook` target).

[T3B `docs/t3code-bridge-skill.md`; GBR "Every outbound message from a Bot is a tool call … `Shell`"]

vecnet's in-hub `grokbot` push route ([PROTO] §13.3) is the same inbound half, made general.

### 1.2 paseo-bridge (Max's, for Grok Bot)

**What it is.** Two parts:
- A headless Paseo daemon: `paseo-bot.service` on `127.0.0.1:6768`, password-protected, `PASEO_HOME` under `/mnt/build-cache/paseo`. It never uses the desktop's `~/.paseo` on port 6767.
- A Python CLI, `paseo-bridge`, that dispatches agents to it.

It is pinned to `@getpaseo/cli@0.13.2`. Upstream is at 0.14.0, and the `~/.local/bin/paseo` on xsm is the 0.14.0 desktop AppImage wrapper. [PSB]

**Spawn.**
- `paseo-bridge launch --preset sol|muse|claude-ui|… --title T --text PROMPT [--wake-target forge] [--no-worktree]`.
- `paseo run` cannot pass feature values or a project id, so launch goes through `node/launch.mjs`. That script uses the pinned CLI's internal client (`dist/utils/client.js`) and:
  - creates an **unprompted** agent;
  - validates provider, model, mode, effort and service tier;
  - archives the agent on any mismatch before the prompt is sent.
- Projects are keyed by main repo root, and each agent gets a Paseo worktree by default.

[PSB `paseo_bridge/node/launch.mjs`]

**Policy it already enforces** (worth keeping behind any vecnet front end): [PSB README "Model policy"]
- a model allowlist (Sol, a time-boxed Astra, MiniMax always refused);
- tier fallback;
- capacity recovery (retry, then a Cursor Auto handoff in the same workspace);
- a Claude subscription-only guard that strips API-key environment names and checks `claude auth status`;
- cache pruning tied to settle.

**Permissions.** Every preset launches unattended: Codex `full-access`, Claude `bypassPermissions`, Muse `allowAll`, Cursor `auto_accept`. **No approvals flow at all today.** [PSB]

**Drive.** `wait <id>`, `read <id> --after N`, `send`, `settle <id> [--force]`, and `settle --all-idle --older-than 2d`.

**Results.** `--wake-target` appends instructions telling the agent to send `QUESTION`, `BLOCKED` or `DONE` wakes. Those wakes run `paseo-bridge wake`, which **shells out to `t3-bridge wake`** and so reuses t3-bridge's targets and outbox. Launch itself sends no completion wake. [PSB `cli.py:251`]

### 1.3 T3 Code (upstream)

**What it is.** `pingdotgg/t3code`, MIT, ~41k stars, v0.1.6 upstream; xsm runs 0.1.3. It is a server plus web, desktop and mobile clients. The server owns provider processes, terminals, git and files, with event-sourced orchestration. [T3O; T3L]
- **Providers:** Codex through app-server over stdio (`packages/effect-codex-app-server`), Claude through `@anthropic-ai/claude-agent-sdk`, Cursor, OpenCode, Pi, and generic ACP (`packages/effect-acp`, `provider-acp-registry`).
- **Client API:** Effect RPC over WebSocket. It is internal and versioned for T3's own clients, not a public contract.
- **Outside-agent surface:** the MCP endpoint that t3-bridge uses, with OAuth scopes.
- **Remote:** loopback by default; `--lan`; `t3 pair` one-time links that become bearer sessions with scopes (`orchestration:operate`, `terminal:operate`, …); Tailscale Serve; the hosted relay "T3 Connect".

[T3O `docs/user/remote-access.md`]

**Local caveats** [T3L]:
- The xsm database is about 22 GB.
- A cold start takes about 134 s.
- The service has a launcher-handoff mitigation in place.

So a spawner must treat T3 as slow to come up, and use `t3-health`-style readiness checks rather than "port open".

### 1.4 Paseo (upstream)

**What it is.** `getpaseo/paseo`, Apache-2.0 per its LICENSE (GitHub shows NOASSERTION because of a third-party carve-out), ~22.8k stars, v0.14.0 (2026-10-29). It is a daemon plus desktop, mobile, web and CLI clients for Claude Code, Codex, Copilot, OpenCode, Pi, Antigravity and Muse, plus a generic ACP adapter with a catalog of 25+ agents. [PSO]
- **API:** WebSocket at `ws://127.0.0.1:6767/ws`, with an **official TypeScript SDK `@getpaseo/client`**:
  - `agents.create({config:{provider}, cwd, prompt})`, `agent.send()`, `run()`, `waitForFinish()`;
  - `respondToPermission({requestId, …})`;
  - `agent.timeline.subscribe()` (live only; refetch after reconnect);
  - `archive()`.
  - **No cancel method is documented** (unconfirmed).
- **Auth:** localhost bind; an optional password sent as a Bearer token (stored as bcrypt); a Host-header allowlist; an optional E2E relay (Curve25519 + NaCl box, self-hostable Elixir relay). [PSO `docs/security`]
- **Implication:** paseo-bridge's coupling to CLI internals can move to the official SDK on the next upgrade. The SDK also exposes **permission requests**, which T3's MCP does not.

### 1.5 Codex app-server

**What it is.** The JSON-RPC protocol that the Codex VS Code extension, T3 and the new codex-acp all use. Apache-2.0. The CLI labels it `[experimental]`. [CXS; CXD]

**Transports.**
- `--listen stdio://` (default, newline-delimited JSON, `jsonrpc` field omitted).
- `unix://PATH`.
- `ws://IP:PORT`: experimental, and **unauthenticated by default** off loopback. Use `--ws-auth capability-token` (token file or SHA-256) or `--ws-auth signed-bearer-token` (shared-secret JWT). Requests carrying `Origin` get 403.

The CLI also has `app-server daemon {bootstrap|start|stop|enable-remote-control|…}` ("durable local app-server management for SSH-driven use") and `app-server proxy`, which bridges stdio to the running daemon's control socket. The daemon subcommands are undocumented on the web page; I saw them in local `--help` only. [CXS]

**Lifecycle.** [CXS schema; CXD]
- `initialize` → `initialized`.
- `thread/start {model, cwd, approvalPolicy, sandbox, developerInstructions, …}` emits `thread/started`. Also `thread/resume`, `thread/fork`, `thread/list`, `thread/read`.
- `turn/start` takes input items. `turn/steer` (with `expectedTurnId`) updates an in-flight turn. `turn/interrupt`.
- `thread/inject_items` adds items without starting a turn.
- Streaming: `item/started`, `item/agentMessage/delta`, `item/completed`, `turn/completed`, `thread/status/changed`.

**Approvals** are server → client requests:
- `item/commandExecution/requestApproval` → `accept | acceptForSession | decline | cancel`;
- `item/fileChange/requestApproval`;
- `item/permissions/requestApproval`;
- `item/tool/requestUserInput`;
- `mcpServer/elicitation/request`;
- `item/tool/call`: client-provided dynamic tools. Declaring them is experimental; I did not find `dynamicTools` in the non-experimental schema.

**Auth.** `account/login/start` supports API key, ChatGPT browser, device code or external tokens. The docs say app-server auth is not permitted for commercial or hosted services, which is fine for Max's own boxes.

### 1.6 ACP (Agent Client Protocol)

**What it is.** JSON-RPC 2.0, client to agent, with the agent normally a **subprocess over stdio**. Apache-2.0, ~4.6k stars, very fast cadence: v1.15.1 on 2026-11-02, and schema v2.0.0-alpha in parallel. [ACP]
- **Agent methods:** `initialize`, `authenticate`, `session/new`, `session/prompt` (returns a stop reason), and the notification `session/cancel`. Optional: `session/load`, `session/set_mode`, plus the completed RFDs `session/list`, `session/resume`, `session/delete` and session config options.
- **Client methods (the spawner's side):** `session/request_permission` (baseline), `session/update` notifications (message chunks, tool calls, plans, mode changes), and optional `fs/*`, `terminal/*` and `elicitation/*`.
- **Remote:** a Streamable-HTTP + WebSocket transport RFD is "Active" (revised 2026-10-30), not final. A Rust crate already implements it.
- **SDKs:** TypeScript `@agentclientprotocol/sdk` 2.4.0, Rust, Python, Kotlin, Java. **I found no official Go SDK**, which matters if the spawner lives in vecnet's Go binary.
- **Registry:** <https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json>, with install metadata.

**Adapters.** [ACPA]
- **Claude Code:** `@agentclientprotocol/claude-agent-acp` (0.100.0, on the Claude Agent SDK; successor to `@zed-industries/claude-code-acp`).
- **Codex:** `@agentclientprotocol/codex-acp` 2.5.0, "built on the new Codex App Server". `zed-industries/codex-acp` was archived on 2026-07-22.
- **Gemini CLI:** `--acp` (`--experimental-acp` is a deprecated alias).
- **Others:** about 50 agents in total, including OpenCode, Goose, Kimi, Qwen, Cursor, Copilot (preview), Cline, OpenHands, Hermes and Pi (`pi-acp`).

**Caveats.** v1 and v2 differ, and I could not confirm whether v2 drops `session/load` and fs/terminal. Claude through ACP must keep paseo-bridge's subscription-only rule: the adapter runs the Agent SDK under whatever auth the environment gives it.

### 1.7 Orca

**Candidates.**
- **`stablyai/orca`** (MIT, Electron, created 2026-03, near-daily releases) is the "AI orchestrator / agent development environment" Max most likely means. The GitHub API reports ~113k stars, which is implausible for its age; treat it as unverified.
- `araa47/orca` is a 7-star tmux spawner.

[ORCA]

**How it spawns.** Each agent runs in a terminal inside its own git worktree. It supports about 30 CLIs or "any CLI agent", SSH worktrees, and a mobile companion through a relay.

**Automation.** The CLI "scripts a running Orca editor", so the desktop app must be up. Control is at terminal level: `orca terminal create --command`, `send --text --enter`, `read`, `wait --for tui-idle`. An experimental orchestration layer adds runs, tasks, `worker-start`, typed `send/check/ask` and `worker_done`. There is no headless mode, network API or MCP server.

**Fit.** Poor as a vecnet backend:
- It needs a GUI session.
- It drives TUIs by keystrokes, so there are no structured approvals or turn boundaries.
- It duplicates what T3 and Paseo already do headlessly.

At most it is a human viewer, if Max likes its UI.

### 1.8 pagd (pag-chat; read only, another agent's active work)

**What it is.** pag-chat's daemon on xsm. It hosts the front-door bot `pag0` and dispatches coding workers. [PAG]

**How it spawns and supervises.**
- `@pag/machines` `MachineProvider`. `SshProvider` uses one unix user per bot on the box (x-tall today).
- Jobs are **detached** and run under `systemd-run --user`: `jobs.start/status/output/kill`. Each job can get its own ssh-agent holding one repo key.
- After a crash, pagd reattaches to running jobs; a job the box no longer knows settles `outcome_unknown` and is never re-run.
- Each job gets a per-job capped token from the credential proxy on cachy.
- An nftables egress rule per uid limits traffic to the proxy, forgejo and github.
- Codex binaries are pinned.

**What a worker is.** An **opaque `codex exec` job**: brief in, transcript as a blob, a typed outcome parsed from a final JSON block. **Steering a running worker is not possible in milestone 1.** Interactive child loops and a per-bot "work thread" wait for Stage 2. [PAG design §4.5–4.6]

**Trust model.** Peer messages are untrusted, and `PeerPort` cannot express consent. Workers never message Max; only the front door does. [PAG design §4.5]

**Overlap with "ACP agents as supervised processes".**
- **Shared:** host-side process lifetime that survives the daemon, reattach, per-job credentials, egress enforcement, and version pins.
- **Different:** pagd has no bidirectional session channel. A detached `systemd-run` job with an output file gives stdout only, while ACP and app-server need a live stdio peer for the whole session.

So reusing `SshProvider` for ACP would need a small per-session host process (stdio ↔ socket) launched as the detached job.

---

## 2. How each maps onto vecnet

### 2.1 Comparison

| Backend | Spawn | Send prompt | Stream / result | Approvals | Cancel / end | Remote | Built by Max |
|---|---|---|---|---|---|---|---|
| t3-bridge → T3 | `launch` (`t3_thread_launch`) | `t3_thread_send` `auto/queue/steer/restart`, idempotent | `read` timeline; `watch` → wake | None over MCP (T3 UI only) | `t3_thread_interrupt`, `settle` | T3 loopback/LAN/relay; bridge is local CLI | Yes, full |
| paseo-bridge → Paseo | `launch` (validated, unprompted-first) | `send` | `read`/`wait`; agent-sent wakes | Bypass presets only; SDK has `respondToPermission` | `settle [--force]` | Daemon loopback; relay optional | Yes, full |
| Codex app-server | `thread/start` | `turn/start`, `turn/steer`, `thread/inject_items` | item deltas, `turn/completed` | Typed server requests | `turn/interrupt`, `thread/archive` | stdio; WS experimental with token auth | No (T3 wraps it) |
| ACP | spawn process, `session/new` | `session/prompt` | `session/update`; stop reason | `session/request_permission` | `session/cancel`, kill process | stdio; HTTP/WS RFD not final | No (T3 and Paseo wrap it) |
| Orca | `terminal create` / `worker-start` | keystrokes | `read` / `tui-idle` | None structured | kill terminal | Needs running desktop app | No |
| pagd | `jobs.start` (`codex exec`) | Brief only | Output file + typed outcome | pag-chat cards, its own | `jobs.kill` | SSH to build box | pag-chat's |

### 2.2 What a spawned agent becomes

**Options.**
1. **Thread-addressed session behind a spawner peer** (recommended for phase 1).
   - The job thread *is* the conversation, and the spawner peer is the only vecnet identity.
   - Messages to the spawner in that thread go to that session. Replies come `as` the spawner, `in_reply_to` the prompting message.
   - There is precedent: openclaw runs "one openclaw session per vecnet thread" [PROTO §13.5, R25].
   - Other peers join the conversation by joining the thread, under the §16.3 history rules.
2. **Per-session peer under the spawner's principal** (phase 2).
   - Each session gets its own `peer_` (kind `session`), a directory entry and presence. The spawner holds the principal credential and speaks `as` each session [PROTO §1.3].
   - This needs a new power. Today only the operator runs `peer add` [PROTO §2.12]. So add "a principal may mint and remove `session` peers under itself, within a quota, audited". Without it, the operator would have to pre-create a pool of slots.
3. **The agent enrols itself** (a token injected into the session, plus vecnet MCP inside the agent). Rejected for the default:
   - It puts a long-lived secret inside a model context, the thing §14.8 avoids for dots.
   - Enrolment needs the operator anyway.

### 2.3 Who holds credentials

| Credential | Holder |
|---|---|
| vecnet peer credential (phase 1), or principal credential (phase 2) | The spawner, stored like other CLI profiles [PROTO §12] |
| T3 OAuth token | `t3-bridge`, in its config dir |
| Paseo password | `paseo-bridge`, in its config dir |
| Model and provider auth | The backend: OpenCodex homes, Claude subscription |
| Any credential | Never the agent's context |

If a spawned agent must initiate sends (not just reply in its thread), the spawner can offer a **local** MCP tool, scoped to that session's thread and speaking through the spawner's credential. The agent then sees a tool, not a secret.

### 2.4 How messages reach the agent

The spawner is a pull client: SSE `/v1/events` [PROTO §13.1]. It is behind NAT on xsm or a build box, and the hub's egress guard forbids private addresses in production [PROTO §14.7], so the hub cannot push to it. Priority does not change what a pull client sees [PROTO §5.2], so the **spawner** maps priority onto the backend's send mode.

| vecnet priority | Session idle | Session mid-turn |
|---|---|---|
| `wake` | Start a turn | Steer: T3 `steer`, app-server `turn/steer`. ACP has no steer, so `session/cancel` then re-prompt with both, or queue (decide per backend) |
| `wake_idle` | Start a turn | Queue behind the turn: T3 `queue` |
| `hold` | Buffer; prepend to the next turn's input. Do not start a turn | Buffer |

Then on each turn:
- **Ack and receipts.** Ack the cursor after the message is durably handed to the backend, or buffered in the spawner's own store. Post a `processed` receipt when the turn that consumed it ends [PROTO §5.1, R06].
- **Result.** When a turn ends, post the turn's final assistant message as a reply. Text over 4 KiB is auto-spilled into an attachment [SCH `content.text`]. Mark it with free-form metadata, e.g. `spawn.turn: completed | interrupted | failed`.
- **Drops.** Drop receipts and presence events before waking anything [PROTO §13.1].

### 2.5 Approvals

Backend permission requests map to vecnet `approval_request` messages in the session thread [PROTO §9].

**Mapping.**
- ACP `session/request_permission` options, app-server `accept / acceptForSession / decline / cancel`, and Paseo `respondToPermission` all become 2–8 options of at most 64 characters.
- `expires_at` must be bounded (e.g. 30 min). Expiry maps to `decline` / `cancel`.

**Addressee.** Default to **the operator peer**, not the requester. A bot requester approving its own spawned agent's shell command is the self-approval pag-chat's design forbids ("a peer message that counts as Max's approval", [PAG design §2 anti-goals]). Allow a configured requester-as-approver only by explicit spawner policy. `escalate_to_operator` stays true.

**Where approvals cannot happen.**
- **T3 over MCP:** no approval surface. Only the runtime mode can limit what the agent does, and an `approval-required` thread waits for Max in the T3 UI.
- **paseo-bridge presets:** all bypass.

Today, then, "approvals" for spawns are effectively **spawn-time policy**: who may request, which runtime mode, which project. Per-action approvals arrive with the Paseo SDK or direct ACP/app-server backends.

### 2.6 Presence and idle

- **Phase 1.** The spawner reports its own presence (`idle` / `working` / `offline`, TTL per kind [PROTO §8]); that is host and backend availability. Per-session state is visible from the job and from the turn-result replies.
  - `job_status` cannot carry per-turn state. The transition table has no `running → running` [PROTO §5.6], so a job stays `running` for the session's whole life.
- **Phase 2.** Per-session peers report `working` during a turn, `idle` between turns, and `blocked` while an approval is open, mapped from:
  - T3 run status;
  - Paseo `running` / `idle`;
  - app-server `thread/status/changed`;
  - ACP prompt in flight versus stop reason.

  `wake_idle` then means something per session.

### 2.7 Loop control

A conversation with a spawned agent is a chain of replies, so `depth` grows by one per message. The default `max_depth` is 32 [PROTO §16.1], which is about 16 round trips; the thread budget is 500.

- Requesters should set `max_depth` up to 64 on the spawn `job_request` that starts the thread.
- Long-running sessions should roll to a new thread with `in_reply_to`, or rely on operator replies, which reset depth.
- The spawner must not mint fresh threads to dodge depth.
- The connector breaker [PROTO §6] applies to the requester bot ↔ spawner pair.

---

## 3. vecnet-side shape: options and recommendation

### 3.1 Goals used to judge

- **G1** Reuse existing spawners; no new session manager.
- **G2** No vecnet secret inside a model context by default.
- **G3** Works for hosts the hub cannot reach (xsm and build boxes behind NAT; hub on fly.dev). Outbound connections only.
- **G4** Fits the current protocol with at most small additive changes. Jobs, threads and approvals already exist; jobs are implemented in `internal/hub/jobs.go`.
- **G5** Policy (models, permission modes, projects, subscription guards) is enforced next to the backend, where it already lives.
- **G6** The hub stays a message hub: no process supervision and no backend credentials.
- **G7** Authorisation comes from the authenticated sender id, never from content [PROTO §14.1, §14.3].

### 3.2 Options

| Option | G1 | G2 | G3 | G4 | G5 | G6 | G7 | Verdict |
|---|---|---|---|---|---|---|---|---|
| **A. `spawner` peer per host; `spawn` jobs; thread-addressed sessions** | ✔ wraps bridges | ✔ | ✔ SSE out | ✔ jobs exist; `spawner` kind is a one-line addition, or use `session` + capability tag | ✔ | ✔ | ✔ local allowlist on `sender` | **Chosen** |
| B. Hub-side "agent source" config (hub calls backends) | ✔ | ✔ | ✘ backends are loopback; §14.7 forbids private egress in prod | ✘ new hub subsystem | ✘ policy split from backend | ✘ hub holds T3/Paseo creds | ✔ | Refuted by G3, G6 |
| C. Bridges inside pagd | ✔ | ✔ | ✔ | ✔ | ? | ✔ | ✘ pagd treats peer requests as untrusted and unable to authorise state changes; a vecnet-requested spawn is exactly that | Refuted by G7 under pag-chat's own design; also another team's daemon |
| D. Agents self-enrol with injected tokens | ✔ | ✘ | ✔ | ✘ enrolment is operator-only | ✔ | ✔ | ✔ | Refuted by G2, G4 |
| E. A + per-session peers minted under the spawner's principal | ✔ | ✔ | ✔ | ✘ today (needs principal-minted peers) | ✔ | ✔ | ✔ | Phase 2 extension of A |

### 3.3 Recommended design (A, growing into E)

**Identity.**
- One spawner per host: `spawner-xsm` first; build boxes later.
- Peer kind `spawner`. This is an additive change to the fixed kind set in `internal/hub/hub.go:149` and the schema enum. The fallback is kind `session` with `capabilities: ["spawn:t3", "spawn:paseo"]`.
- `capabilities[]` advertises backends and presets. The `quota` object advertises concurrency, e.g. `{"max_sessions": 4, "running": 1}` [PROTO §8].

**Spawn request.** A `job_request` with exactly one recipient, the spawner [PROTO §5.6]:

```json
{"version": 1, "job_key": "review-pr-91", "operation": "spawn",
 "instructions": "<the prompt, <= 16 KiB>",
 "input": {"backend": "t3", "project": "vecnet4",
           "workspace": {"type": "worktree", "branch": "agent/review-pr-91"},
           "preset": "sol", "runtime_mode": "auto", "title": "Review PR 91"}}
```

`job_key` gives idempotent spawns: the same thread and key returns the existing job. `input` stays under 16 KiB; larger briefs go in attachments.

**Lifecycle.**
1. `accepted`.
2. `running`, with `output {backend, session_ref, link}`. The link is a `t3-thread://` link or Paseo agent id.
3. Per-turn reply messages.
4. On settle, `succeeded` with a short summary. Errors give `failed`.
5. Cancel: the requester's `job_status: cancelled` moves the job to `cancellation_requested`. The spawner interrupts and settles the session, then reports `cancelled`.

**Ending sessions.** An idle timeout and an explicit `settle` operation end a session. The default idle timeout is to be decided; paseo-bridge's bulk settle uses 2 days.

**Conversation.** Section 2.4: the session is addressed by the job thread, and replies come `as` the spawner.

**Authorisation (spawner-local, default deny).**
- An allowlist of requester `peer_` ids, each with a ceiling: backends, presets, runtime mode, projects, and max concurrent sessions.
- Never authorise on names, content, `origin` or `ingress` [PROTO §14].
- vecnet send scopes [PROTO §12] are the outer gate: only peers allowed to message the spawner can reach it at all.
- A requester that is not allowlisted gets an `approval_request` to the operator instead of a refusal. This is optional and needs a decision.

**Approvals.** As in 2.5: operator by default; mapped only for backends that surface them.

**Backends, in order.**
1. `t3`, through `t3-bridge` (CLI or library).
2. `paseo`, through `paseo-bridge`, moving to `@getpaseo/client` on upgrade.
3. `acp` (direct, for hosts without T3 or Paseo).
4. `codex-app-server` direct, only if ACP's codex adapter proves insufficient. It is richer: steer, inject, typed approvals.

**Where it lives.** A separate small daemon, not the hub. Two choices:
- a `vecnet spawner` subcommand of the Go CLI, which reuses the existing pull loop in `internal/cli/watch.go` but has no official Go ACP SDK;
- a Node daemon next to t3-bridge, with the TS SDKs for ACP and Paseo and direct reuse of t3-bridge modules.

Rec: **Node**, because three of the four backends have first-party TypeScript clients and t3-bridge is Node. Max to decide (see below).

### 3.4 Phasing

- **P0 (small, independent).** Add a `vecnet` target kind to `t3-bridge`'s `targets.json`.
  - It POSTs `/v1/messages` with a peer credential read from a file, `idempotency_key` = the wake event id, `thread_id` from launch state, and priority `wake_idle`.
  - `paseo-bridge wake` gets it for free, because it delegates to t3-bridge.
  - Result: completion and `QUESTION`/`BLOCKED`/`DONE` wakes land in vecnet threads now. Grok Bot keeps working through its `grokbot` push route.
- **P1.** The `spawner-xsm` daemon with `t3` and `paseo` backends: spawn jobs, thread relay, turn-result replies, cancel and settle, local allowlist.
  - Grok Bot can then request spawns through `vecnet_send` on `/mcp` instead of needing `Shell` on xsm. That shrinks the Bot's footprint on Max's workstation.
- **P2.**
  - Per-session peers: principal-minted `session` peers, with presence.
  - Approvals through the Paseo SDK.
  - The scoped local MCP tool for agent-initiated sends.
- **P3.** An `acp` backend for build boxes (x-alpha preferred, not xsm, given the AVX-512 stamp and cargo-guard). Coordinate the supervision layer with pag-chat (below).

---

## 4. To decide with pag-chat

1. **Is pagd a spawner client, a spawner backend, or neither?**
   - **Client:** `pag0`'s `dispatch` could become a `spawn` job to a spawner, giving interactive, steerable workers instead of opaque `codex exec`.
   - **Backend:** pagd handles `operation: "spawn"` for x-tall.
   - Rec: client at most. pagd's rule that peers cannot express consent conflicts with a peer-requested spawn.
2. **Supervision reuse.** Should a build-box `acp` backend reuse `@pag/machines` `SshProvider`?
   - Candidates for reuse: detached `systemd-run` units, reattach, per-uid nftables egress, per-job capped tokens through the cachy proxy, and Codex pins.
   - It would need a per-session stdio ↔ socket host process.
   - The alternative is a separate implementation. Who owns the x-tall `pagchat-*` users and the egress rule if both run there?
3. **Credential proxy.** Can spawner jobs on build boxes mint per-job capped tokens from pag-chat's proxy, or does the spawner bring its own model auth?
4. **Work thread versus vecnet thread.** pag-chat's Stage 2 per-bot "work thread" [PAG design §4.3] is close to a vecnet thread per session. Pick one model so a worker's chatter is not mirrored twice.
5. **Trust of spawned-agent output.** A spawner's replies are agent free text. In pag-chat's lanes a `session` peer is `user-remote`. Should spawner output be `untrusted`, or tainted with the requester's taint? [PAG integration notes, open question]

## 5. To decide with Max

1. **Who may spawn, and at what ceiling.** Examples: Grok Bot `forge`, `pag0`, Claude/Codex sessions such as `cc-vecnet-cos`. Note that paseo-bridge presets are all bypass or full-access today, so any allowed requester gets code execution on the host.
2. **Approvals.** Always the operator peer (rec), or may a requester approve its own spawned agent's actions?
3. **Hosts.** xsm only for P1 (T3 and Paseo live there), and which build box for P3? x-alpha is preferred.
4. **Language and home.** Node daemon (rec) or Go `vecnet spawner`? Its own repo, or inside vecnet4?
5. **Edits to Max's bridges.** Is it OK to add the `vecnet` target kind to `t3-bridge` (P0), and later to move `paseo-bridge` to `@getpaseo/client`?
6. **Protocol additions.** Peer kind `spawner` (additive). For P2, principal-minted `session` peers with a quota.
7. **Session end policy.** Idle timeout before auto-settle, and whether settle also archives worktrees (paseo-bridge does; t3-bridge's `settle` only organises the thread).
8. **Should spawned agents be able to message arbitrary peers?** The P2 scoped MCP tool, or thread-confined forever.

## 6. Uncertainties

- **Paseo SDK:** cancel is not documented.
- **ACP versions:** the v1 and v2 method sets differ. The remote transport is not final.
- **Orca:** the star count is implausible, and the CLI's IPC mechanism is unknown.
- **T3:** its RPC is not a public contract, so the MCP surface is the stable seam.
- **Codex app-server:** `daemon` and `enable-remote-control` semantics were seen only in local `--help`.
- **Grok Bot:** whether its `Shell` for t3-bridge runs on xsm directly or on another paired machine. paseo-bridge docs say "on xsm".

--- SUMMARY ---

**Recommendation: a `spawner` peer per host.**
- Spawns are vecnet jobs (`operation: "spawn"`, prompt in `instructions`, backend and preset in `input`, `job_key` for idempotency).
- Each spawned session is addressed by its job thread. The spawner relays messages in and posts turn results out.
- Agents hold no vecnet secret.

**Reuse first.**
- t3-bridge (T3 MCP: launch, send with `auto/queue/steer`, read, interrupt, settle) and paseo-bridge (validated launch, model and subscription policy, capacity recovery, settle) already spawn and drive sessions on xsm.
- Their wake path is what vecnet replaces.
- T3 and Paseo already wrap Codex app-server and ACP.

**Protocols underneath.**
- ACP is the generic direct backend for hosts without T3 or Paseo (about 50 agents, TS SDK, stdio; remote transport not final).
- Codex app-server is the richest single-vendor protocol (steer, inject, typed approvals; WS experimental with token auth).

**Not a fit.**
- Orca: desktop-bound and terminal-level.
- Hub-side agent sources: the hub cannot reach loopback backends and would hold their credentials.
- Bridges in pagd: pag-chat treats peer requests as unable to authorise.

**Mapping.**
- **Priority:** `wake` → steer, `wake_idle` → queue, `hold` → buffer.
- **Approvals:** `approval_request` to the operator by default. Only Paseo's SDK and direct ACP/app-server surface them; T3's MCP and paseo-bridge's bypass presets do not.
- **Presence:** the spawner's own presence in phase 1; per-session presence needs principal-minted `session` peers in phase 2.
- **Depth:** set `max_depth` 64 on spawn and roll long sessions to new threads.

**Phases.**
- **P0:** a `vecnet` wake target in t3-bridge (covers paseo-bridge too).
- **P1:** `spawner-xsm` with t3 and paseo backends.
- **P2:** per-session peers, Paseo approvals, a scoped local send tool.
- **P3:** an ACP backend on build boxes, with the supervision layer agreed with pag-chat.

**Open decisions.**
- **With pag-chat:** client or backend role, `SshProvider` reuse, the credential proxy, work thread versus vecnet thread, trust of spawned output.
- **With Max:** spawn allowlist and ceilings, approval routing, hosts, Node or Go, edits to his bridges, the `spawner` kind and principal-minted peers, settle policy, agent-initiated sends.
