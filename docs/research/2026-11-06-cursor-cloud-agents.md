# Cursor cloud agents from any vecnet peer

_Researched 2026-11-06. Docs only. No Cloud Agents API call was made, so nothing here spent Cursor quota. No API key was read, printed, or written._

**Question.** How can any vecnet peer launch a Cursor cloud agent, with the key sealed, strangers held for approval, and the result posted back into the launch thread?

**Verdict.** Put the integration in the hub (architecture b). Enrol one existing `launcher` peer and let an in-hub worker serve its `launch` jobs. Seal Max's single user API key the way a Grok Bot route seals its routine key, and call `https://api.cursor.com` through the hub's egress guard. A launcher backend (architecture a) would leave that key in a file on a host whose other sessions already run as Max.

The requester-facing contract stays the `launch` job (PROTOCOL §13.8, §5.6). This needs no new peer kind and no envelope change. A short connector checklist in PROTOCOL is the only normative edit, and it belongs to the build, not to this note.

Labels: **DOCUMENTED** = Cursor's public docs, fetched 2026-11-06, not exercised. **SOURCE** = read in this repository. **PATTERN** = the gb2cxc shape named for this note; that code was not read.

---

## Source key

| Tag | Source | Trust |
|---|---|---|
| [CA] | <https://cursor.com/docs/cloud-agent/api/endpoints> | Official. Cloud Agents API v1, public beta |
| [V0] | <https://cursor.com/docs/cloud-agent/api/v0> | Official. Legacy v0, still documented |
| [WH] | <https://cursor.com/docs/cloud-agent/api/webhooks> | Official. Webhook payloads and HMAC |
| [API] | <https://cursor.com/docs/api> | Official. Auth, key kinds, rate limits, availability |
| [ADM] | <https://cursor.com/docs/account/teams/admin-api> | Official. Team spend and usage events. Enterprise |
| [SA] | <https://cursor.com/docs/account/enterprise/service-accounts> | Official. Service accounts and spend caps |
| [SDK] | <https://cursor.com/docs/sdk/typescript> | Official. SDK auth and billing. Not the Go path |
| [AUTO] | <https://cursor.com/docs/cloud-agent/automations> | Official. Automations billing. A different product |
| [PROTO] | `docs/PROTOCOL.md` | Normative for vecnet |
| [SCH] | `docs/protocol/envelope.schema.json` | Wire schema |
| [SEAL] | `internal/hub/seal.go`, `internal/hub/grokbot.go`, `internal/push/grokbot.go` | How a Cursor secret is stored and sent today |
| [EG] | `internal/egress/dialer.go` | Egress guard |
| [LN] | `docs/connectors/launcher.md`, `docs/design/2026-11-03-launcher.md`, `connectors/launcher/src/backend.ts`, `connectors/launcher/src/policy.ts` | Launcher contract and why hub-side was rejected for local backends |
| [GB] | `docs/connectors/grokbot.md`, `docs/research/grokbot.md` | Grok Bot webhook trigger |
| [ADR] | `docs/adr/0001-tech-stack.md` | Hub is Go |
| [SPEC] | `SPEC.md` §1 | The hub is a bus |

---

## Verified facts

| Fact | Status | Source |
|---|---|---|
| v1 is public beta and may change before general availability. Work is a durable agent (`bc-…`) plus per-prompt runs (`run-…`). Base host `https://api.cursor.com` | DOCUMENTED | [CA] intro |
| Launch is `POST /v1/agents`. It returns the agent and the initial run (`status: CREATING` on the run, `ACTIVE` on the agent) | DOCUMENTED | [CA] Create An Agent |
| Status of the agent is `GET /v1/agents/{id}`: `ACTIVE`, `IDLE`, or `ARCHIVED`. Execution state is on the run, `GET /v1/agents/{id}/runs/{runId}` | DOCUMENTED | [CA] Get An Agent, Get A Run |
| Active run statuses: `CREATING`, `RUNNING`, `WAITING_FOR_BACKGROUND_WORK`. Terminal: `FINISHED`, `ERROR`, `CANCELLED`, `EXPIRED`. A terminal run carries `result` (final assistant text), `durationMs`, and, once a branch exists, `git.branches[]` with `repoUrl`, `branch`, `prUrl` | DOCUMENTED | [CA] Get A Run |
| List agents: `GET /v1/agents`, newest first, `limit` default 20 max 100, `nextCursor` omitted when finished. List runs: `GET /v1/agents/{id}/runs`, same paging | DOCUMENTED | [CA] List Agents, List Runs |
| Follow-up is `POST /v1/agents/{id}/runs`. One active run per agent. A second follow-up while a run is active returns `409 agent_busy`. `delivery: "steer"` injects into the running turn and returns `202` with `delivery: steered` or `queued` | DOCUMENTED | [CA] Send A Follow-Up Or Steer |
| Cancel is `POST /v1/agents/{id}/runs/{runId}/cancel`. The run becomes `CANCELLED` and cannot resume. A new run on the same agent continues the conversation. Cancelling a terminal or never-active run returns `409 run_not_cancellable` | DOCUMENTED | [CA] Cancel A Run |
| v1 has no webhook field. The endpoints page says webhooks are coming soon and points at v0 | DOCUMENTED | [CA] intro |
| v0 launch accepts `webhook.url` and optional `webhook.secret` (minimum 32 characters). Cursor then POSTs `statusChange` only for `ERROR` and `FINISHED` | DOCUMENTED | [V0] Launch an Agent; [WH] |
| Webhook HMAC is `X-Webhook-Signature: sha256=<hex>` over the raw body, plus `X-Webhook-ID` and `X-Webhook-Event`. `User-Agent` is `Cursor-Agent-Webhook/1.0`. The sample payload id is `bc_abc123`, the v0 id shape | DOCUMENTED | [WH] |
| v0 also has `POST /v0/agents/{id}/followup`, `POST /v0/agents/{id}/stop` (pause; a later follow-up runs again), `GET /v0/agents/{id}/conversation` (user and assistant texts), and list/status. v0 documents Basic authentication | DOCUMENTED | [V0] |
| v1 can stream one run as SSE, `GET /v1/agents/{id}/runs/{runId}/stream`, with `assistant`, `thinking`, `tool_call`, `result`, and `done`. Resume with `Last-Event-ID`. After `X-Cursor-Stream-Retention-Seconds` the stream may return `410 stream_expired`; the terminal state is then Get A Run | DOCUMENTED | [CA] Stream A Run |
| v1 has no conversation endpoint. Get A Run's `result` is the final text, not the tool trace. v0's conversation endpoint is user and assistant texts, not tool calls | DOCUMENTED | [CA] Get A Run; [V0] Agent Conversation |
| Auth for v1 is a user API key (Dashboard → API Keys) or a service account API key. Basic (key as username, empty password) and Bearer are the same. There is no OAuth grant for calling this API | DOCUMENTED | [API] Authentication; [CA] intro |
| Admin, Analytics, AI Code Tracking, and Bugbot are Enterprise and use a different key (`admin:*` for the Admin API, format `crsr_…` on that key). Origin uses its own bearer and refuses a Cursor API key placed directly in its header. The TypeScript SDK can mint a key with a browser login; Team Admin keys are not accepted by the SDK | DOCUMENTED | [API]; [SDK] Authentication |
| The secrets endpoints refuse team API keys and automation webhook API keys. Keys that start with `grok_` get `403 key_not_supported` | DOCUMENTED | [CA] Secrets |
| Per-agent usage is `GET /v1/agents/{id}/usage`: `inputTokens`, `outputTokens`, `cacheWriteTokens`, `cacheReadTokens`, `totalTokens`, per run and totalled. The page describes tokens, not cents | DOCUMENTED | [CA] Get Agent Usage |
| Dollar cost is the Enterprise Admin API, `POST /teams/filtered-usage-events`, field `chargedCents`, filter `cloudAgentId` (or `*` for every cloud-agent run). Hourly aggregates; poll at most hourly; 60 requests/minute per team. `GET /teams/spend` is the cycle total those cents reconcile to | DOCUMENTED | [ADM]; [API] availability table |
| Service accounts bill the team pool. An admin can set a per-period spend limit; over it the account is blocked with "Service account spend limit reached". Personal automations bill the user. v1 of this work is Max's one account, so the credential is a user API key | DOCUMENTED / constraint | [SA]; [AUTO]; chief of staff |
| The overview's default, when an endpoint states no other number, is 20 requests per minute. The Cloud Agents row says "Standard rate limiting" and gives no number. `GET /v1/repositories` is 1/user/minute and 30/user/hour. A 429 is the overview's rate-limit status | DOCUMENTED | [API] Rate Limits; [CA] List GitHub Repositories |
| A client-supplied `agentId` (`bc-` plus an id; examples are UUIDs) makes a replay return `409 agent_id_conflict` instead of a second agent. The 409 is not documented as returning the original body. `agentId` cannot be combined with `envVars` | DOCUMENTED | [CA] Create An Agent |
| Create accepts `repos[].url` and `startingRef`, or `env` of type `cloud`, `pool`, or `machine`. `autoCreatePR` opens a PR when the run completes. Omitting `model` uses the user default, then the team default, then a system default. `mcpServers` and `envVars` are optional and are a place a secret could be injected | DOCUMENTED | [CA] Create An Agent |
| Grok Bot push is already a Cursor trigger. The hub POSTs the Delivery to `https://api2.cursor.sh/automations/webhook/<uuid>` with a sealed routine bearer. HTTP 200 `{"success":true}` means a routine run started. The bot replies later through `/mcp`. That key is not a Cloud Agents API key | SOURCE | [PROTO] §13.3; [SEAL]; [GB] |
| Sealing is AES-256-GCM under `meta.hub_key`, prefix `v1:`. The routine key is unsealed at send time and never returned by the API. The egress guard allows https to public unicast addresses, pins DNS, and refuses redirects | SOURCE | `internal/hub/seal.go`; `internal/hub/grokbot.go`; [EG]; [PROTO] §14 invariant 7 |
| The launcher is a `launcher` peer. Requesters send `operation: "launch"`. The hub does not enforce §13.8. Plugins cannot see the hub credential. The daemon reads a backend secret from a file. Sessions on xsm run as Max, and a `full-access` session can read his credentials. The shipped default policy allows any requester up to a numeric ceiling | SOURCE | [PROTO] §13.8, §18.1; [LN]; `docs/connectors/launcher.md` |
| The 2026-11-03 design rejected a hub-side backend because T3 and Paseo listen on loopback and production egress forbids private addresses | SOURCE | `docs/design/2026-11-03-launcher.md` §3.2 option B; `docs/research/2026-11-03-agent-sources.md` §3.2 |
| Peer kinds already include `launcher`. `job_status.output` is free-form, capped at 8 KiB. Approval requests already exist. Blobs already carry attachments | SOURCE | `internal/hub/hub.go` `PeerKinds`; [SCH] `jobStatus`, `approvalRequest`; [PROTO] §9, §11 |
| gb2cxc, the private Grok Bot to Codex Cloud bridge, is not in this tree. `gb2cxc.xpp.exchange` in older notes is webhook2mcp, a different service. The bridge pattern to copy is: per-caller principals, a concurrency cap, an hourly launch cap, completion webhooks, and an environment allowlist | PATTERN | This note; `docs/research/dots.md` for the hostname only |

---

## 1. The API surface

Call v1. v0 remains available, and it is the only documented webhook implementation, but new integrations are told to use v1, and v1 is the surface that has runs, steer, terminal cancel, and a stream.

All paths below are on `https://api.cursor.com`.

| Need | v1 | v0, for comparison |
|---|---|---|
| Launch | `POST /v1/agents` → agent + initial run | `POST /v0/agents` → one id, status `CREATING` |
| Status | `GET /v1/agents/{id}` and `GET /v1/agents/{id}/runs/{runId}` | `GET /v0/agents/{id}` includes `summary` and `target` |
| List | `GET /v1/agents`, `GET /v1/agents/{id}/runs` | `GET /v0/agents` |
| Follow-up | `POST /v1/agents/{id}/runs`, or the same path with `delivery: "steer"` | `POST /v0/agents/{id}/followup` |
| Cancel | `POST /v1/agents/{id}/runs/{runId}/cancel` → terminal `CANCELLED` | `POST /v0/agents/{id}/stop` pauses; a follow-up runs again |
| Transcript | SSE stream while it is retained; then `result` on the run | `GET /v0/agents/{id}/conversation` |
| Who am I | `GET /v1/me` (name, and for a user key the email) | `GET /v0/me` |

A launch body that matters for vecnet is small: `prompt.text`, an operator-chosen `repos[]` entry or named `env`, optional `model`, optional `autoCreatePR`, and a client `agentId` so a retry cannot create a second agent. `envVars` and `mcpServers` stay unset. Both are how a secret would enter the VM, and `envVars` also forbids a client `agentId`.

`GET /v1/me` is the safe identity check before the first real launch: it returns the key's display name and, for a user key, the owner's email. The worker can log that name and email. It must not log the key.

Artifacts (`GET /v1/agents/{id}/artifacts` and a 15-minute presigned download URL) are files the agent wrote, not the transcript. A presigned URL is a bearer for those bytes. The worker must not put one in a vecnet message. Downloading an artifact into the blob store can wait.

## 2. Auth

Three Cursor credentials show up in this repo's neighbourhood. Only one can launch a cloud agent.

| Credential | What it does | Use for v1 |
|---|---|---|
| User API key, Dashboard → API Keys | Calls the Cloud Agents API as that user. Basic or Bearer | Yes. One key, Max's account |
| Service account API key | Same API, billed to the team, with an admin-set spend limit | Later, if Max moves the work off his user |
| Admin API key (`admin:*`, documented `crsr_…` format) | Team members, spend, usage events. Enterprise | Not a launch credential. A second sealed secret only if dollar totals are required |
| Grok Bot routine key (sealed `crsr_…` on a `grokbot` route) | Wakes one routine at `api2.cursor.sh` | Already in use. The secrets page refuses automation webhook keys. Do not send this key to `api.cursor.com` |
| Origin bearer, or an OAuth login | Origin API, or the SDK's browser login | Not this API. [API] says a Cursor API key is not an Origin bearer |

There is no OAuth client, install, or app consent flow for the Cloud Agents API itself. OAuth appears in two other places: MCP servers the agent calls (`mcpServers[].auth`), and the TypeScript SDK's interactive `Cursor.auth.login()`. The hub is a Go process ([ADR]) and would speak the REST API. It would not embed the SDK, and it would not start a browser login.

v1 stores one sealed user API key. A later account is another sealed row plus a routing rule. The requester-facing job does not change when that row appears, because the requester never names an account.

## 3. Completion, webhooks, and cancellation

**What "done" looks like on v1.** The agent goes `IDLE` when the last run has finished, including a run that ended in a recoverable error (the error detail stays on the run). The run itself is `FINISHED`, `ERROR`, `CANCELLED`, or `EXPIRED`. A terminal run can carry `result` (final assistant text) and `durationMs`. `git` is per agent: every run on that agent returns the same branch snapshot.

**Webhooks are not on v1 yet.** [CA] says they are coming soon. [WH] describes the v0 behaviour: you pass `webhook.url` at create time, Cursor POSTs when status becomes `ERROR` or `FINISHED`, and you verify `X-Webhook-Signature` as HMAC-SHA256 of the raw body. The payload's useful fields are `id`, `status`, `summary`, `source`, and `target` (`url`, `branchName`, `prUrl`). `summary` is a short string. It is not a transcript. Retries happen when the receiver fails, so the receiver dedupes on `X-Webhook-ID` and answers 2xx quickly.

A v1 worker therefore cannot wait for a webhook. It watches the run. The practical watch is one SSE stream per active run (a single long request, which fits a tight per-minute budget better than polling Get A Run in a loop), and a slow Get A Run after the stream ends or returns `410 stream_expired`. The stream is the only v1 transcript that includes tool calls, and it expires, so the worker persists events as they arrive.

Build the inbound HMAC receiver when v1 documents a webhook field, or if Max explicitly accepts a v0 launch in order to get one. Until then an inbound route would have nothing to sign against. The receiver, when it exists, is a hint that wakes the same worker. The worker still reads status and the transcript from the API. The payload is untrusted until the HMAC matches ([PROTO] §14 invariant 2).

**Cancellation.** v1 cancel is terminal for that run. v0 stop is a pause. Vecnet cancel is also terminal: the requester's `job_status` with `state: cancelled` moves the job to `cancellation_requested`, and the recipient reports `cancelled` ([PROTO] §5.6). The worker maps that onto v1 cancel of the active run, then reports `cancelled`. A 409 `run_not_cancellable` means the run already ended; the worker reports `cancelled` from the run it already has. It does not call v0 stop, and it does not `DELETE` the agent (that delete is permanent on v0, and v1's delete is the same kind of operation).

A follow-up after a cancelled job would start a new run on an agent the job has already left. The job stays terminal. A later message in the thread is the §13.8 ended-session case.

**Steer is not cancel, and a retry can double-deliver.** [CA] says a steer that fails on the network may already have reached the agent, and a retry is a new prompt. The worker persists the delivery id it steered and does not steer the same delivery twice.

## 4. Quota and cost

Three different limits sit on top of each other. Vecnet has to enforce the first itself, because Cursor will not do it per vecnet requester.

| Limit | Where it is visible | What v1 can do |
|---|---|---|
| API request rate | [API]: default 20/minute when an endpoint gives no number; Cloud Agents is "standard"; repositories is much tighter | One SSE stream per active run. Avoid `GET /v1/repositories` on the launch path. Back off on 429 |
| Tokens for one agent | `GET /v1/agents/{id}/usage` | Put the totals in the completion `output` and in presence `quota` |
| Money | Enterprise `chargedCents` by `cloudAgentId`. A service-account spend cap is a dashboard switch, not a field on the user-key API. The weekly Grok Bot pool (`docs/research/grokbot.md` §3) is a different meter | A user key on one personal account does not expose remaining budget or cents. The operator sees the dashboard |

`GET /v1/me` does not report a balance. There is no documented "remaining included usage" field on the Cloud Agents API. The hub can count its own launches and runs, and it can attach token totals after a run. It cannot, with the v1 user key alone, refuse a launch because Max's plan is nearly spent.

That is the gap behind open question 1. If the account is an Enterprise team, a second sealed Admin key can read `chargedCents` hourly. If Max uses a service account, the dashboard spend limit is the hard stop, and the worker treats "Service account spend limit reached" as a terminal `failed` with no retry storm.

## 5. The Grok Bot route is already a Cursor trigger

This is in production in the hub, and it is the wrong trigger for a cloud agent. It is also the right pattern for how a secret should live.

When a peer sends a `wake` message to a Grok Bot peer, the hub does not hand the message to a connector process. A `grokbot` route is live immediately ([PROTO] §2.12). The push worker:

1. Takes a run against two budgets: the route's `runs_per_hour` (default 200) and 300 runs an hour across every `grokbot` route (`internal/hub/grokbot.go`, [PROTO] §13.3). Routes carry no Cursor account id, so the 300 is hub-wide.
2. Unseals the routine key (`Hub.GrokbotKey`).
3. POSTs the Delivery JSON to `https://api2.cursor.sh/automations/webhook/<uuid>` with `Authorization: Bearer` and `X-Automation-Key`, through the egress client, with no redirect (`internal/push/grokbot.go`).
4. Treats HTTP 200 `{"success":true}` as "a run started". The response's `runUuid` is not a vecnet thread id. The bot later pulls `vecnet_inbox` and replies with its peer bearer.

`hold` never POSTs. The Delivery waits in the log until some later run drains it.

What this implies:

- Vecnet already starts work inside Cursor's cloud by an outbound, egress-policed call with a sealed key. Architecture b extends that shape. It does not introduce the first Cursor credential into the hub.
- The routine webhook and the Cloud Agents API are different products, hosts, and keys. A routine POST does not return a `bc-` id, a run status, a branch, or a transcript, and it has no cancel. Completion is "the bot chose to call `vecnet_reply`". Overloading the route to "launch a cloud agent" would spend the 200/hour and 300/hour routine budgets, put the prompt in a Grok Bot's context, and still need some other caller to hit `api.cursor.com`.
- A Grok Bot can be a requester of a cloud agent, as any other peer can: it sends a `launch` job through `/mcp`. The wake that made the bot run is not itself the launch. Both spends hit Max's Cursor account. Whether an allowlisted bot may do that is open question 7.
- "Accepted is not finished" carries over. v1 create returning an agent id means the run was enqueued (`CREATING`), just as grokbot's 200 means a routine started. The job stays `running` until a terminal run status is recorded. The worker does not mark `succeeded` on the create response.
- The routine key's sealing, the "never return the key" rule, the redacting logger, and the egress guard are the implementation to copy. The `routes` table is not. A push route fires on delivery. A cloud-agent session is create, then steer, then a terminal status, then a transcript. That is a worker with its own rows, not `kind = 'grokbot'`.

## 6. Prior art: the gb2cxc pattern

gb2cxc is a private Grok Bot to Codex Cloud MCP bridge. Its code is not in this repository and was not read. The name `gb2cxc.xpp.exchange` in `docs/research/dots.md` is the webhook2mcp deployment, which is a different service; this section does not describe that deployment.

The pattern, as given for this design, has four parts. v1 copies all four, with the webhook part deferred to match the v1 API.

| Pattern | What v1 does with it |
|---|---|
| Per-caller principals | The authenticated sender peer id, and that peer's principal id, are the principals ([PROTO] §14 invariant 3). v1 still spends one Cursor account. The principal decides who may launch. It does not select a key until a later multi-account row exists |
| Concurrency cap | A hub-wide cap on agents in `CREATING` / `RUNNING` / `WAITING_FOR_BACKGROUND_WORK`. Over it, the launch fails `cap_reached` and does not call Cursor. An approval does not raise this cap |
| Hourly launch cap | A per-requester cap on runs (create and follow-up). Over it, the worker asks the operator through §9 and does not call Cursor. The number is Max's (open question 2) |
| Completion webhooks | The pattern's completion signal. v1 has no webhook field yet, so the worker uses the SSE stream plus Get A Run. The inbound HMAC receiver is the later form of this same signal, deduped on `X-Webhook-ID` |
| Environment allowlist | The requester names a project alias. The operator's table maps the alias to a repository URL, a starting ref, and an optional environment name. A raw URL, a pool name, or a machine name in the job is `invalid_input` |

The launcher already has the alias idea (`policies` and `projects` in `connectors/launcher/src/policy.ts`) and an hourly launch counter. Its default ceiling still lets any requester launch, and a cap is a hard `cap_reached`, not an approval. The chief of staff's rule is stricter: off the list, or over the per-requester cap, the launch waits for the operator. Strangers never auto-launch.

## 7. Where this sits in vecnet

A requester already knows how to start a coding session ([PROTO] §13.8, `docs/connectors/launcher.md`):

- `POST /v1/messages` with one recipient, the launcher peer, `kind: message`, `priority: wake`, no `thread_id`, and `job_request.operation: "launch"`.
- `instructions` is the prompt. `input.backend`, `input.project`, `input.preset`, and `input.permission_mode` are required by `launch-input.v1`.
- The launcher replies to the launch message, so depth stays 1 and `expires_at` keeps refreshing (§18.3).
- Cancel is the requester's `job_status` `cancelled` (§5.6).
- Turn text over the cap rides as an attachment. `job_status.output` is at most 8 KiB ([SCH]).

That contract is the requester API for Cursor as well. `input.backend` is `cursor`. `input.project` is an alias. `input.preset` names an operator-defined model choice; the worker maps it to `model.id` and `model.params`, or omits `model` when the preset says default. `input.permission_mode` is recorded and checked against the operator's list. The Cloud Agents API has no permission-mode field, so the worker does not send one, and it does not surface mid-run permission prompts. Spawn-time policy is the control, which is the same conclusion the launcher design reached for backends that skip approvals (`docs/design/2026-11-03-launcher.md`, summary).

The worker posts as that launcher peer, `in_reply_to` the launch message:

| Moment | Message |
|---|---|
| Allowlist and cap pass, slot reserved | `job_status` `accepted`, then `running` with `output` `{agent_id, run_id, url, backend, preset}` once create returns |
| Run reaches a terminal status | One reply whose text is the run's `result` (the completion summary), truncated with a marker when it exceeds the post cap, plus a `text/markdown` attachment of the persisted transcript |
| Job ends (settle, expiry, or the session rule in §8) | `job_status` `succeeded` or `failed`. `output` carries status, `result` summary, `git` branch and `prUrl`, token totals, and the agent URL. Over 8 KiB, `{output_truncated: true, summary}` as §13.8 already says |
| Requester cancels | v1 cancel, then `job_status` `cancelled` |

Thread participants can read the attachment with their bearer ([PROTO] §11). The transcript is untrusted content (§14 invariant 2), including when the operator reads it.

While the job is `running`, a message from the requester, the operator, or a `relay_from` peer is a follow-up, with §13.8's priority mapping: `wake` steers, `wake_idle` becomes one follow-up run when the current run ends, `hold` buffers. Anyone else gets `processed` with note `not_relayed`. The worker never copies message text into `envVars` or `mcpServers`.

Presence follows §18.2 (15-minute TTL) and §13.8's tags: `launch:cursor`, `preset:cursor/<preset>`, `host:cursor`, `mode:<mode>`, and a `quota` object with running agents and the hub-wide cap. Tags authorise nothing (§14.1).

## 8. The two architectures

**(a) A backend plugin at `connectors/launcher/src/backends/cursor/`.** The existing launcher daemon receives `launch` jobs, reads the API key from a file via `PluginContext.readSecretFile`, and calls `api.cursor.com` from the launcher host. No new peer kind. The plugin implements `Backend`: `launch`, `send`, `interrupt`, `turns`, `settle`.

**(b) An in-hub worker.** The operator enrols one peer of kind `launcher`. The hub process is that peer's only consumer. The API key is sealed with `Hub.Seal` and unsealed in the egress client. Launches are the same `launch` jobs, dispatched inside the hub when the recipient is that peer. Outbound calls use `egress.Policy` and a host allowlist of `api.cursor.com`.

The 2026-11-03 note rejected "hub calls the backend" for T3 and Paseo. Those backends are on loopback or a LAN, the hosts are behind NAT, and §14 invariant 7 forbids private egress in production. `api.cursor.com` is a public https host. That objection does not apply. `SPEC.md` §1 still calls the hub a bus. Grok Bot routes are the exception already shipped: the hub holds one Cursor secret and makes one policed outbound call. A cloud-agent worker is that exception with a longer life cycle. It does not supervise a local process, a worktree, or a user unit.

| Criterion | (a) Launcher plugin | (b) Hub worker |
|---|---|---|
| Key sealed under `meta.hub_key`, unsealed only at send, absent from logs and from every model context | The key sits in a 0600 file on the launcher host. The daemon runs as Max. `docs/connectors/launcher.md` states that a `full-access` session can read his credentials. Exporting `meta.hub_key` so the host could unseal would also unseal source secrets, consent cookies, and blob tokens | `Hub.Seal`, same helper as `AddGrokbotRoute`. The plaintext exists only inside the outbound call. The launcher peer's vecnet bearer is not installed into a connector config |
| Who may launch | Launcher policy. Shipped default: any requester, up to a ceiling. Over the ceiling is `cap_reached`, a hard fail | Operator allowlist of peer and principal ids. Off-list or over the per-requester run cap: §9 approval, no Cursor call. Hub-wide concurrency is a hard cap |
| Egress | The host's network. The hub's guard never sees the call | `egress.Policy`: https, public addresses, DNS pinned, no redirects, plus a host allowlist |
| Requester contract | `launch` job. No new peer kind | Same `launch` job, same kind `launcher`. No new peer kind |
| Follow-up, cancel, transcript | Mapped onto `SessionHandle`. The fit is awkward: there is no local worktree, branch names are Cursor's `cursor/…`, approvals are `none`, and the transcript expires unless the plugin stores it | The worker speaks runs, steer, and cancel directly, and writes the transcript into the blob store before the stream expires |
| Hub stays a bus | Keeps the hub free of this credential | Adds a worker and one sealed key. Same class of exception as `internal/push/grokbot.go` |
| Multi-account later | Another file on the host, and the host still has every session's trust problem | Another sealed row. Requesters still address one launcher peer |

**(a) leaves the key where a model can read it.** That misses the chief of staff's constraint even if the plugin's own prompt never contains the key. The plugin boundary (`backend.ts`) is explicit that plugins do not see the hub, so the plugin cannot ask the hub to unseal. The call has to originate where the key is sealed.

**(b) is the recommendation.**

### Session rule under (b)

One `launch` job is one Cursor agent. The worker stores a client `agentId` before the POST. A received `409 agent_id_conflict` means that id is already taken; the worker GETs it and continues, and the 409 body is not documented as a copy of the original create response. A lost response (no status at all) is the same GET: 200 means continue, 404 means the POST may be retried with the same id. The worker does not mint a second id.

The job stays `running` across follow-up runs until the requester settles, the requester cancels, or `expires_at` (capped at 7 days, §18.3) forces a settle. Each terminal run posts one summary reply and one transcript attachment. Settle with no active run reports `succeeded`. Settle or expiry with an active run cancels that run first. Cancel from the requester reports `cancelled`. A message after the terminal `job_status` gets `processed` / `session_ended` and starts no run.

This matches §13.8's "the job stays running for the session's life" more closely than closing the job on the first `FINISHED`. The first `FINISHED` is still a visible summary in the thread, which is the result the chief of staff asked for. Open question 4 is the confirmation.

### Authorisation under (b)

Checked in this order, before any Cursor call, and again after an approval, because the cap may have moved. Until the gate passes, the worker reports no `job_status`: the hub's job stays `requested` (§5.6). `accepted` means the gate passed and a slot is reserved.

1. Sender is the authenticated peer ([PROTO] §14 invariant 3). Kind `source` never launches.
2. `input` matches `launch-input.v1`, `backend` is `cursor`, and `project` is an alias in the operator table. Anything else is `failed` / `invalid_input` or `policy_denied`.
3. The sender's peer id or the sender's principal id is on the allowlist. If neither is, post an `approval_request` to the operator and stop. No Cursor call.
4. The requester's runs in the rolling hour are at the cap. Same approval path. A steer of a delivery already accepted does not consume a new run. A follow-up that creates a run does.
5. Hub-wide active runs are at the ceiling. `failed` / `cap_reached`. This one does not go to approval.
6. On `yes`, continue from step 4. On `no`, or on expiry (`decision: expired`, §9), `failed` and no Cursor call. Expiry denies, as an operator prompt already does in the launcher.

The approval `question` names the requester, the alias, and the count against the cap. It does not contain the API key. The prompt text stays in the launch thread as ordinary content.

Strangers are step 3. They are never auto-launched. One approval allows that one launch. It does not add them to the allowlist.

## 9. Recommendation

Build architecture b.

- One operator-enrolled peer of kind `launcher` (a name like `launcher-cursor` is an operator choice). The hub worker is its consumer. Disable the peer and the worker stops.
- One user API key for Max's Cursor account, sealed with `Hub.Seal`, installed from a 0600 file by an operator command that never puts the key in argv, logs, or the HTTP response. Rotation replaces the sealed blob. The `routes` table and `route add --kind grokbot` stay as they are.
- Outbound client: `egress.Policy`, host `api.cursor.com` only, Bearer header built at the call, Authorization stripped from any error. `GET /v1/me` once at startup to record the key's name and email.
- Inbound: none in the first build. Persist the SSE stream. Add the HMAC receiver when v1 documents webhooks, reusing the §14 invariant 7 expectations (https, no open proxy) in reverse: Cursor calls the hub's public URL, the hub checks the signature with `hmac.Equal`, and the secret is hub-generated and sealed.
- Results in the launch thread, as §7.
- Copy the gb2cxc pattern: principals, both caps, environment allowlist, and webhooks when the API has them.
- Leave `connectors/launcher/src/backends/**` unchanged. A local plugin cannot seal the key.

## 10. Open questions for Max

1. **Account and spend.** Confirm the launch credential is a user API key on your personal account. A service account adds a dashboard spend limit and bills the team; the Admin API's `chargedCents` view is Enterprise and would be a second sealed key. Are per-run token totals enough until then?
2. **Allowlist and the cap.** Which peer ids and principal ids may launch, and what is the run cap per requester per hour? A principal id here means the authenticated principal of the sender (every peer of that principal). The launcher's existing `prn_` key matches `created_by` only. Which meaning do you want? Does a steer count as a run, or only create and follow-up?
3. **Aliases.** Which repository aliases exist, what ref each pins, and is `autoCreatePR` on? May a requester override the ref, or only the alias?
4. **Session length.** Keep the job `running` across follow-up runs until settle, cancel, or expiry (this note's rule), or close the job `succeeded` on the first terminal run?
5. **Transcript contents.** The SSE stream includes tool-call arguments and results, which can contain file contents the agent read. Store that full stream, or only assistant text? Thread participants can read the blob.
6. **Webhook receiver.** v1 documents no webhook field. Build the inbound HMAC endpoint in the first build so it is ready, or add it when Cursor documents v1 webhooks?
7. **Grok Bots as requesters.** An allowlisted bot may launch, and the same Cursor account pays for the routine wake and the cloud agent. Should bots be on the initial allowlist?

## 11. Rough task list

This is a file-ownership sketch a later plan can expand. It is not that plan. Each task owns the files in its row. A task that needs a change in another row sends it back to that row's owner. No task in the first build edits `connectors/launcher/**`.

**Depends.** 2 and 3 depend on 1. 4 depends on 2 and 3. 5 depends on 4. 6 depends on 5. 7 depends on 4 and is skippable until question 6 is yes. 8 depends on 5.

| Task | Owns | Does |
|---|---|---|
| 1. Key and schema | New migration `internal/store/migrations/00NN_cursor_agent.sql` (one sealed key row, allowlist, alias table, per-requester run counter, agent row keyed by `job_id` and client `agentId`). `internal/hub/cursor_key.go` (seal, unseal, rotate; never return plaintext). `internal/cli/cursor_agent.go` and the matching command wiring in `internal/cli/commands.go` (key from `--key-file` only, mode 0600, refuse a `Bearer ` prefix, the same discipline as `route add --kind grokbot`) | Operator can install a key and list aliases and allowlist entries. `GET` paths return names and ids, never the key. Tests use a fake key string, not a real one |
| 2. Egress client | `internal/cursoragent/client.go` | The v1 calls this note uses: create, get agent, get run, follow-up, steer, cancel, usage, SSE stream, `GET /v1/me`. Host allowlist `api.cursor.com`. Errors carry status and Cursor's `code`, never a header or body that might echo the key. 409 on create is "fetch this id", not "mint another" |
| 3. Policy | `internal/cursoragent/policy.go` | The §8 order: allowlist, per-requester run cap, hub-wide active cap, alias resolution. Approval posting uses `internal/hub/send.go` `Hub.Send` as the launcher peer, `kind: approval_request`, recipient the operator peer (§9). Denied and expired fail the job with no client call |
| 4. Worker | `internal/cursoragent/worker.go`, a start hook in `cmd/vecnet-hub/main.go` | In-process consumer for the one launcher peer. Internal `auth.Caller` of kind `peer` for that peer. Lease so a duplicate hub process cannot launch twice. Maps §7 and the session rule. Reports presence. Replies to the launch message. Cancel path is §5.6 → v1 cancel |
| 5. Transcript | `internal/cursoragent/transcript.go`, blob write through the existing blob store | Append SSE events until `result` / `done` / `410`. Redact nothing the operator has not asked to redact (question 5). Upload `text/markdown`, attach it to the summary message. Drop the stream body from logs |
| 6. Checklist and operator page | `docs/PROTOCOL.md` (a new §13.10 only), `docs/connectors/cursor-cloud.md` | Normative checklist: launch input, approval gate, status outputs, transcript attachment, "the hub enforces this for the cursor launcher peer". Operator page: install the key, edit the allowlist, what a requester sends. Update `GLOSSARY.md` only if a term is added; `launcher` and `launch job` already cover this |
| 7. Webhook, later | `internal/httpapi/cursor_webhook.go`, a sealed webhook secret next to the API key | Verify raw-body HMAC in constant time, dedupe on `X-Webhook-ID`, answer 2xx, wake task 4. Refuse anything that is not the documented event. Do not treat `summary` as the transcript |
| 8. Tests | `internal/cursoragent/*_test.go`, `internal/hub/cursor_key_test.go` | httptest Cursor stand-in on the dev egress override. Cover: stranger gets an approval and zero outbound calls; over-cap gets an approval; allowlisted under cap launches once and a replay does not launch twice; cancel reports `cancelled`; the key never appears in a log line or a message body |

The Go tests in task 8 run in CI. A local `just test` on a developer machine is the heavy build this note's author was told to skip. The client tests must not dial `api.cursor.com`.

## 12. Protocol sections, and what changes

The build cites these sections. This research note changes none of them.

| Section | Why the build cites it |
|---|---|
| §2.2 | Send, one job recipient, replies |
| §5.6 | Job states, requester cancel, `sequence` belonging to the recipient |
| §6, §18.3 | `expires_at` cap and the reply-to-root rule |
| §8, §18.1, §18.2 | Kind `launcher`, presence TTL, self-reported `capabilities` and `quota` |
| §9 | Operator approval before a launch that the allowlist or the cap does not allow |
| §11 | Transcript attachment, who may read it |
| §12 | Secrets out of logs and audit rows |
| §13.3 | The sealed-key, egress-policed Cursor trigger to copy, and the product boundary not to cross |
| §13.8 | The requester contract and the reply, relay, and cancel rules to follow |
| §14 invariants 1–3, 6, 7 | No authority on the bus; untrusted bodies; sender from authentication; secrets out of logs; egress guard |
| §16.3 | `locked_participants` on the launch |
| [SCH] `jobRequest`, `jobStatus`, `approvalRequest` | No new fields |

**Peer kinds.** No change. `launcher` is already in `PeerKinds` and in §8. The cursor worker is an implementation of that kind, not a new one. Kind `launched` (§18.5) stays unimplemented.

**Envelope.** No change. `output` is already a free-form object under 8 KiB. Approval options stay ordinary strings. The transcript is an ordinary attachment.

**PROTOCOL.** The first build should add §13.10, a connector checklist, the way §13.8 is one. §13.8 says the hub enforces none of it. §13.10 is the exception: the hub is the connector, so the checklist is what the hub enforces for this peer. That is a PROTOCOL edit. It does not change peer kinds or the envelope. Until it lands, this note is the spec the checklist should be written from.

`connectors/launcher/schema/launch-input.v1.schema.json` stays the requester's `input` schema. The hub accepts it and then applies the tighter cursor rules (alias, preset table, allowlist). A new schema would be a second requester dialect, which any peer would have to learn. No such dialect is recommended.

## 13. Decisions (cc-vecnet-cos, 2026-11-06)

Architecture (b) is accepted. These answer section 10. Only item 1's key installation and item 3's alias list need Max, and they are needed at build time, not now.

1. **Account.** v1 uses a user API key on Max's personal account. Max creates the key and installs it through the operator command from a 0600 file. Per-run token totals are enough for v1. Service accounts and the Admin API are later.
2. **Who may launch, and caps.** "Principal" means the authenticated principal of the sender, so it covers every peer of that principal. The allowlist starts empty apart from the operator's principal. The cap defaults to 6 runs per requester per rolling hour and is set per allowlist entry. Create and follow-up count as runs. A steer does not count. The hub-wide active-run ceiling defaults to 4.
3. **Aliases.** The operator maintains the alias table. A requester names only an alias, and ref overrides are refused in v1. `autoCreatePR` is set per alias and defaults to on. The first seed is `vecnet` → `github.com/ultradyn/vecnet`, `master`; Max confirms any others.
4. **Session length.** The job stays `running` across follow-up runs until it is settled, cancelled or expires, as in this note's rule.
5. **Transcript.** By default, store assistant text plus tool-call names and status. Do NOT store tool-call arguments or results, because they can hold file contents or secrets the agent read. A per-alias opt-in for the full stream can come later.
6. **Webhook receiver.** Build it when Cursor documents v1 webhooks, not in the first build.
7. **Grok Bots as requesters.** They follow the same rule as any peer: a bot may launch only if it is individually allowlisted, and it is not on the list by default.

Build order: after agent onboarding. The plan follows section 11's task list; the migration number is assigned at merge time.
