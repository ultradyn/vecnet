# Hub-Owned Agent Onboarding Kinds Implementation Plan

> **For agentic workers:** Implement task by task with the house task/review gates in `docs/plans/2026-11-05-hub-notices.md`. Workers receive their task, the contracts below and Global Constraints. Steps use checkbox (`- [ ]`) syntax. A `---` ends every task. This document is a plan; its creation does not authorize implementation, migration application, deployment or live hub changes.

**Goal:** The hub describes each supported agent kind's onboarding prompt, validated inputs and submit action once. CLI, operator MCP and web expose that same operation. Grok Bot is the first complete new instance: webhook first, one peer and connector identity per gateway bot, a sealed push route and one advisory setup message.

**Architecture:** Add a stdlib-only `internal/onboarding` contract/registry package, consumed by caller-aware hub services and thin HTTP, CLI, MCP and web adapters. Keep transport credentials behind the existing human invitation/key-entry boundary. The operator HTTP/web Grok submit writes the peer, route, connector descriptor, setup thread/message/delivery and safe retry receipt in one transaction; the existing push worker performs delivery after commit. MCP `vecnet_agent_add` takes no secrets: for grokbot it creates nothing and returns a web path, and the human submits URL/key on the Add agent form. Existing session, launcher, openclaw, pagchat-bot and dot behavior moves onto the registry without changing enrolment or linking semantics.

**Tech Stack:** Existing Go 1.27.1, SQLite, stdlib templates/JSON/crypto, existing MCP JSON-RPC/schema machinery and vanilla browser ES modules; Node 22.23.3 for browser-model tests. No new runtime dependency or separate Grok connector process. All builds/tests run through `just bb` per `docs/BUILDBOX.md`.

**Spec:** CoS rulings encoded in this revision (2026-11-06): hosted OAuth identity, one credential mode, consent-cookie precedence, existing peer-name semantics, connector pattern `vecnet-<slug>`, operator-only HTTP/web with no admin-parity gate, no priority resolver, and kind-scoped `tools/list` after `mcp-schema-diet`. Those rulings override earlier drafts, the previous Needs Max list, and the current MCP-first smoke sequence. Historical inputs: `/home/xertrov/src/vecnet4/.scratch/vecnet/integrations/cos-reply-2026-11-06-platform-facts.md` and `grokbot-onboarding-reply-2026-11-06.md`; `docs/connectors/grokbot.md`; `docs/PROTOCOL.md` §§2.2, 2.12–2.13, 3, 4.1, 4.3, 4.5–4.6, 5.1–5.3, 8, 12–14, 16.2–16.3 and 20.1.

**Starts from:** `master` `0bf4beb350f4696004b4d5db1dcb103bf19a4689`, branch `plan/agent-onboarding`. This inventory is source inspection, not independently rerun platform or live-hub evidence. The scratch sources' “verified” observations are supplied evidence. Grok Bot loading behavior in Decisions §5 is from the live conductor measurement supplied with this revision.

## Global Constraints

- **Plan only for this assignment.** Edit and commit only this document. Implementation tasks below remain unchecked. No implementation, migration application, live connection test or hub change occurs during planning. No `attn`.
- **Generic first, Grok Bot first instance.** The hub owns prompt templates, field definitions/validation and submit-action dispatch. Web renders the spec; CLI and operator MCP have operation parity. Preserve the existing five kinds' behavior and copy initially. Peer kinds `operator` and `source` are not Add agent choices.
- **Identity hard stop.** Any MCP `vecnet_whoami` that does not match the expected peer is a hard stop. Connector names are not proof of identity. Stop, report in the user's own chat, and do not send `connected`, a receipt, or an ack on that connection. Until a credential is bound to the intended bot and whoami proves it, use only that bot's own verified CLI profile.
- **Few gateway bots, each with its own peer and connector `vecnet-<bot-slug>`.** Max confirmed this directly to CoS (2026-11-06): a few gateway bots per user or team, each with its own peer and connector. Not one connector for every bot, and not one shared peer for the account. Keep the recommendation prominent in the prompt, form and guide.
- **One credential mode per bot.** Hosted OAuth is the default. CLI bearer is the other mode. Never both. Linking revokes that peer's bearer. The onboarding wizard warns and requires confirmation before linking a peer that already has a live bearer. There is no keep-bearer option. The conductor stays on CLI and must not be linked yet.
- Never print, log, echo or return a webhook key, Authorization value, link code or peer credential to a model. Routine URL/key are copied by the human from the routine page into the protected form or CLI; never paste either into chat, prompts, setup messages, diagnostics or receipts. Human invitation paste blocks retain their existing intentional output boundary; they never become MCP results. No peer secret is entered into Cursor for the hosted OAuth path.
- New onboarding accepts a raw key or a leading `Bearer ` header value, normalizes it in memory and seals only the raw key. Redaction covers validation errors, failed requests, panics, browser errors and test failure output. Keep the existing low-level route add/rotate commands' raw-key compatibility contract unchanged. Keep the strict grouped-UUID webhook validator.
- Use existing scope, limits, enrolment, OAuth and egress services. Do not infer operator authority from a bot kind or from arbitrary webhook text. The setup message is advisory; the user's pasted conductor-v4 prompt authorizes only its stated actions. HTTP and web onboarding authorize the operator only. Admin-parity may register these operations later; it is not a dependency and this plan adds no pending-action system.
- Prefer **no migration**. Registry definitions are code; existing peers/routes/messages/deliveries and namespaced `meta` receipts suffice. The DB build owns 0026. If integration proves new schema unavoidable, return the concrete reason to CoS and use the **next free number at build time; 0027 is currently expected**. Never reserve a number or edit an existing migration.
- **Tests and builds run on build boxes only:** `BB_JOBS=2 just bb …`, including Node/browser checks, protocol validation and race tests. Never heavy local builds on xsm. Scratch and artifacts use the lane temp directory from `docs/BUILDBOX.md` (lane `TMPDIR`, `<lane>/tmp` on the box). Browser shots use an explicit `SHOTS_DIR`; `just bb` maps that path to `<lane>/shots` and copies it back. Do not hardcode `/mnt/build-cache`. Do not use the machine `/tmp`. Use fake credentials, injected clocks and in-process HTTP servers in automated tests. No real config, `.data/`, `.demo-data/`, development hub or production endpoint.
- UI implementation tasks are **Opus**. Name every shared file in every task that edits `internal/hub/**`, `internal/httpapi/**`, `internal/mcpserver/**`, `internal/oauth/**` or `internal/ui/static/**` (the actual UI location; there is no top-level `ui/` in this checkout). Serialize the named overlaps below. Re-inventory after companion merges; do not edit `ui-addagent-kinds` or copy its implementation. Schema diet on branch `mcp-schema-diet` is out of this plan; CoS owns that branch.

## Decisions and wire contracts

### 1. Current interfaces and the registry boundary

`internal/hub/hub.go:PeerKinds` accepts `grokbot`, `dot`, `openclaw`, `pagchat-bot`, `session`, `launcher`, `operator` and `source`. `Hub.AddPeerWith(ctx, AddPeerOpts)` creates an implicit principal when needed, loads preference-derived send/thread limits through `insertPeer`, and issues enrolment tokens except for dot. The five unchanged Add agent kinds retain those exact policies, TTL choices, open-name rules, single-use output and dot linking.

`internal/ui/static/invite-model.js` currently hardcodes the six Add agent choices and mirrors CLI `inviteSlug`/`inviteBlock` from `internal/cli/invite.go`; `internal/cli/testdata/invite.json` pins both. `invite.js` calls `POST /v1/admin/peers`, with a separate dot-link panel. Move these definitions behind the hub registry; keep compatibility exports as derived adapters while their callers move, not a second editable registry. `format.js` can retain directory labels; it does not decide onboarding behavior.

Proposed shared Go types in `internal/onboarding/types.go` are `KindSpec`, `FieldSpec`, `SubmitRequest` and `SubmitResult`. Freeze their JSON contracts in Task 1:

- `KindSpec = {kind,label,spec_version,prompt_template,fields,submit_action,send_defaults}`. `spec_version` is an opaque revision of the kind contract. `fields` describe string/secret/boolean/integer/enum inputs, requiredness, defaults, bounded length/range/pattern, help and `human_only`. `submit_action` is a declarative ID (`invite`, `oauth_link`, `grokbot_setup`), never executable client code, arbitrary URLs or a client-selected backend function. Prompt placeholders are an explicit allowlist. `send_defaults` includes `priority:"wake_idle"` for grokbot as the recommendation composers preselect and guides quote. It is not a wire default and it does not authorize omitting `priority`.
- `SubmitRequest = {kind,spec_version,request_id,inputs}`. The client generates one stable random request ID per submission and retains it across uncertain retries. Reject unknown fields, unsupported actions and a stale spec version before mutation. `inputs` are validated against the selected spec on the server; frontend validation is assistance only. Grok secret inputs are accepted only on the authorized human HTTP/CLI path. MCP receives a separate secret-free schema and, for grokbot, does not submit those inputs at all.
- `SubmitResult` has safe metadata `{request_id,kind,peer_id,principal_id,route_id?,connector_name?,setup?,auth_state,send_defaults,status?,web_path?}`. `setup = {thread_id,message_id,delivery_id,state}` distinguishes queued/held, accepted, processed, failed, expired and dead-letter states. `auth_state` distinguishes unlinked/pending from independently verified identity; route acceptance never implies connected. A first successful legacy human submission may also return the existing invitation/link output in a separate `human_output`; status/replay/MCP serialization excludes it. Existing trusted human endpoints remain compatible.

The registry is code-owned; this plan adds no editable kind-definition API or database registry. All six specs are listed in the existing order: session, launcher, openclaw, pagchat-bot, grokbot, dot. Adding a future kind needs a registered server action plus parity tests, rather than another UI branch.

### 2. Operation parity, authority and secret entry

Proposed HTTP paths are `GET /v1/admin/agent-kinds`, `POST /v1/admin/agents` and `GET /v1/admin/agents/onboarding/{request_id}`. They map to CLI `vecnet agent kinds [--json]`, `vecnet agent add --kind KIND …` and `vecnet agent status REQUEST_ID [--json]`; operator MCP tools are `vecnet_agent_kinds`, `vecnet_agent_add` and `vecnet_agent_status`. Kind listing supplies the prompt template as well as fields; `agent kinds --kind KIND --json` selects one spec. Successful new creation is HTTP 201; a same-request replay is 200; bad shape is 400, bad field value 422 with field path and fixed safe text, stale spec/request mismatch 409, unauthorized 401/403. Reads and writes are `Cache-Control: no-store`.

HTTP and web authorize the **operator only**, using the existing operator check. Do not loosen that to every peer. [Admin parity](2026-11-05-admin-parity.md) may register the same operations later. This plan does not wait on it, does not add a pending-action type, and does not take a dependency on admin-parity Tasks 1/2/4/7. Tasks 4, 7 and 9 proceed on operator authority alone.

MCP `vecnet_agent_add` accepts kind, spec version, request ID and safe name/options only. It accepts **neither webhook URL/key nor a server-local file path**, takes no secrets, and has no pending-action system. The tool result never contains an invite token, link code, bearer, routine URL or routine key.

- **Legacy kinds.** Follow the HTTP route rule: no secrets in results. The invite token is human output. MCP returns a web path where the human collects it (`/…#add-agent?kind=<kind>&name=…`, plus `request_id`). The Add agent form performs the operator HTTP submit and shows the one-time paste in the dialog. MCP does not mint a token and discard it, and status/replay never return `human_output`.
- **Grokbot.** The tool creates nothing. It returns `{request_id, status:"needs_human", web_path:"/…#add-agent?kind=grokbot&name=…"}` (the fragment also carries `request_id`). The human submits the routine URL and key in the Add agent form. `vecnet_agent_status` reports safe state by `request_id`.

There is no `#add-agent` route today (`internal/ui/static/app.js` opens the invite dialog from a button). Task 9 adds the fragment route. Do not treat a request ID or web path as an authorization capability.

CLI minimum example (the URL and path are human inputs, not model-visible samples containing real values): `vecnet agent add --kind grokbot --url URL --key-file PATH --name NAME [--skip-mcp] [--connector-name NAME]`. Read the key file in-process, bounded to the existing 4 KiB limit; recommend mode 0600 and never put its contents in argv/environment/output. `--key-file` is a local CLI concern; send normalized field values over authenticated HTTPS. The CLI obtains the kind/version contract and shows the same safe result as web, including one-time human output on the human CLI. `--skip-mcp` selects CLI-bearer mode. Existing `peer invite`, `peer link` and `route add/rotate` continue to work. `peer link` still revokes that peer's bearer; the onboarding wizard must warn first when a live bearer exists (Decisions §5). No flag keeps the bearer.

### 3. Grok fields, names and input normalization

Grok inputs are `name`, `webhook_url`, `webhook_key`, optional `connector_name`, and boolean `skip_mcp` default false. The hub URL comes from trusted hub configuration, not an arbitrary form field. The prompt is shown before submission so the human can authorize the bot and obtain routine-page links first. `skip_mcp` chooses the credential mode: false is hosted OAuth (default), true is CLI bearer. The stored choice and the setup text name exactly one mode.

- `webhook_url`: require exactly `https://api2.cursor.sh/automations/webhook/<lowercase-uuid>`, with UUID groups 8-4-4-4-12; no query, fragment, userinfo, port or other host/path. Also use the existing egress policy and redirect refusal. The current `GrokbotURLPattern` checks only 36 lowercase hex/hyphen characters; the onboarding validator must enforce the stricter grouped UUID without silently broadening legacy route behavior.
- `webhook_key`: trim surrounding paste whitespace, strip one leading `Bearer ` (case-insensitive scheme), then apply the existing printable raw-key validator with its prefix and nonempty suffix. Refuse nested schemes, embedded whitespace/control characters and oversized input; fixed error text never includes supplied bytes. Form input is masked, has no reveal/copy result, and is cleared on kind switch, submit completion and dialog close. Do not cache it in storage or request receipts.
- `name`: use the existing peer-name semantics and limits. Operator-set peer names stay free-form and required (`AddPeerWith`); open-enrol suggestions stay on `agentNameRE` (2–32, PROTOCOL §2.12); the principal-create path stays 1–128 runes. Use that same string in CLI and web. Never rename the owner. No separate display-name field. Do not adopt a 64-versus-128 display-name proposal or an owner-rename proposal.
- `connector_name`: default `vecnet-` plus the existing ASCII lowercase bot-name slug (`inviteSlug`: 40-character slug limit, `agent` fallback). Optional override must match `^vecnet-[a-z0-9-]{1,48}$`. Store the chosen name against this peer in the safe receipt. A same-hub collision with another peer is refused transactionally as `connector_name_conflict`; never silently reuse its credential or add a duplicate. No other naming policy. The hub cannot enumerate Cursor account connectors; the bot checks the named connector with whoami.
- `skip_mcp`: still create peer, route, connector descriptor and setup message on the human submit. CLI-bearer mode states webhook plus that bot's own verified CLI profile as the operating path, omits OAuth link/connector instructions, and does not mint a link code. Hosted OAuth remains the default recommendation for gateways. Never emit both modes.

“Create a connector name” means allocate a per-peer descriptor and include its exact name in the hosted-OAuth setup instructions. Creating the connector in the user's app is the bot/human's subsequent platform action; there is no hub API here that silently installs a Cursor connector.

### 4. Atomic submit, one setup message and no migration

Implement proposed `Hub.OnboardAgent(ctx, caller, onboarding.SubmitRequest) (onboarding.SubmitResult, error)`, `Hub.OnboardingKinds(ctx, caller)` and `Hub.OnboardingStatus(ctx, caller, requestID)`. Caller-aware authorization belongs inside the service, including the mutation transaction. Legacy actions delegate to current creation/linking semantics and retain the one-time human output boundary. Grok setup uses transaction-level helpers, not a sequence of independently committed public `AddPeerWith`/`AddGrokbotRoute`/send calls. The grokbot MCP tool does not call this mutation; the operator HTTP/CLI/web submit does.

For a new Grok request, validate/seal before transaction writes, then in one transaction: authorize; claim the request/connector name; create the implicit principal and grokbot peer; apply normal creation defaults; insert its live grokbot route with the sealed key; allocate the connector descriptor and the single credential mode; create a setup thread containing the bot and operator peer; insert one hub-authored ordinary `message`; fan out one Delivery to the bot; append safe audit and receipt. No external webhook request occurs inside the transaction. After commit publish directory/activity/log notifications and kick the existing worker. Rollback leaves no peer, principal, route, name claim or setup message. A newly created Grok peer is `auth_state:"unlinked"`; do not generate and discard an enrolment token. Hosted OAuth then uses the existing link path; CLI-bearer mode uses the existing enrol/profile path. Never do both.

Reuse the permissive `meta(key,value)` table for namespaced `onboarding.v1` records and the transaction's serialization for name claims. Records contain request subject/ID, kind/version, a keyed request digest, resource IDs and nonsecret options only. HMAC the canonical normalized request (including secret inputs) with a domain-separated hub-key derivation in memory to compare retries; never persist the raw request, plain key, URL, ciphertext copy or unkeyed key fingerprint in a receipt. Keep request-id replay receipts and the keyed digest. Scope request lookup to verified subject plus ID; status requires current operation authority. Arbitrary caller-supplied `as` cannot set the request subject. Store IDs/options needed to recover after restart; derive setup delivery state from deliveries and credential readiness from current state. Keep these records separate from send-idempotency's 24-hour GC so retrying an old submit cannot create a second bot. Ordinary peer disable/route removal is the existing recovery path; no new deletion policy is introduced.

An identical retry, concurrent double-submit, lost response or restart returns the same peer/route/name/setup IDs and produces no additional setup Message/Delivery. A changed request under the same ID is 409 `idempotency_conflict`; a previously completed legacy invitation's secret output is not recoverable through status/replay (the human collects it from the first web/CLI submit, or uses existing invite reissue or link UI). No plaintext human output is stored in retry records. One setup **message** does not promise one HTTP attempt/run: existing durable push retry semantics remain at least once, with `delivery_id` dedupe and quota accounting.

Default scope is the current operator-created peer policy (open allow/deny unless existing creation policy says otherwise), not a new hardcoded smoke restriction. Limits come from the same effective preference snapshot as `insertPeer` (factory values 60 sends/minute and 20 threads/hour), with existing route budgets 200 runs/routine/hour and conservative 300 runs/hub/hour. Preserve later operator changes; do not turn the supervised smoke values 6/2 into product defaults.

### 5. Conductor v4, hub sender and Grok Bot credential binding

Use conductor v4 from the platform-facts source as the base, corrected by locked choices: show the user's authorization prompt **before** URL/key entry. It asks the bot to create/reuse `vecnet incoming messages`, link the routine page/deep links for human copying, never paste its webhook URL/key, and wait for submission. `<HUB_URL>` is a nonsecret hub origin; “never paste URL” here means the **routine webhook URL**, not the hub/MCP origin or routine-page links. Every routine prompt names its own connector and expected peer id, tells the bot to call `vecnet_whoami` first, and tells it to stop and report on mismatch.

The one advisory setup message contains the hub/MCP origin, expected peer ID, connector name when hosted OAuth is selected, the single credential mode, setup thread identity and safe next steps, including `wake_idle` when sending to grokbots. It contains no routine URL, key, bearer, enrol command with a token, link code, code-bearing consent URL, stdio command, `mcp-remote` invocation or speculative fallback machine path. The setup text points the human to protected linking. A plain public authorization-page address is not a ready-made peer-bound consent link.

**Grok Bot identity (all four points):**

1. **Hosted OAuth already exists** for grokbot peers (`docs/connectors/grokbot.md`, “Hosted OAuth connector”). The operator runs `vecnet peer link PEER_ID` and gets a one-time code. The bot adds `https://<hub>/mcp` as an OAuth connector named `vecnet-<bot-slug>`, then enters the code on the consent page. No peer secret goes into Cursor. Drop stdio option (c): no stdio MCP, no local `mcp-remote`, and no version-pinned stdio fallback in the prompt, setup message or guide.
2. **Connectors are user-level.** Every bot can see every bot's connector. No routine connector allowlist was observed. Mitigations in this plan: every routine prompt names its own connector and expected peer id; the bot calls `vecnet_whoami` first; on mismatch it stops and reports in the user's own chat. Residual risk is cross-bot connector callability (Review Focus). The detection path is hub audit of `ingress=mcp_tool` with the caller peer.
3. **A link code revokes that peer's existing bearer.** One live credential per peer. Onboarding records one credential mode per bot: hosted OAuth (default) or CLI bearer, never both. If the wizard is about to link a peer that already has a live bearer, it warns that the bearer will be revoked and requires confirmation. There is no keep-bearer option. The conductor must not link its own peer yet; it stays on CLI.
4. **Stale connectors are an account-settings cleanup, not a hub task.** The plan's rule is: do not use a mismatched connector. Max has removed `vecnet-peer` (grok-smoke). A `user-vecnet` HTTP connector may still exist unlinked; do not use it.

**Grok Bot platform facts (live conductor measurement):**

- Grok Bot loads MCP tool schemas on demand via GetDynamicTools / CallDynamicTool. It does not eagerly inline every connector's schemas on every run.
- Every user-level connector is visible to every bot. No routine connector allowlist was observed.
- Unused connectors cost catalog metadata only until their schemas are fetched. N×8k is not the default Grok Bot cost.
- The residual risk is cross-bot connector callability, mitigated by the whoami stop and detected by hub audit (`ingress=mcp_tool`, caller peer). Eager-load cost needs nothing further from Max: loading is on demand.

Source evidence supports hub/system sending without a new kind: `internal/hub/approvals.go:HubSender` is `"hub"`; its expiry message uses `SenderInfo{Name:"hub",Kind:"hub"}`, `insertMessage` and `fanOut`; the Message actor schema already accepts it, and `thread-model.js:isSystem` renders it. Use that representation with `kind:"message"`, `ingress:"hub"`, and `priority:"wake_idle"`, not a new peer kind or impersonated operator. Keep hub out of the peer participant list; include the actual operator peer so the bot's in-thread connected reply has a real recipient. The hub sender stands as planned. If this path proves to require a new kind, stop that slice and report the concrete obstacle; do not substitute a kind. Missing operator peer is a pre-write configuration error.

`internal/oauth/consent.go` currently reuses a returning browser's bound principal from its consent cookie before considering a link code (`postAuthorize` mints from the cookie whenever `cookiePrincipal` hits; `getAuthorize` hides the code field). That is a hub bug, fixed in Task 6 before any MCP acceptance. Giving another connector a new name does not prove a fresh binding. Task 6 makes the consent page always show the peer it would approve (name and id), always offer “use a link code instead”, give a supplied link code precedence over the cookie, and replace the cookie on a successful code binding. Never reset an unrelated working connector, and never link the conductor, to make a test pass. MCP onboarding is not complete until the per-bot credential and whoami proof exist; webhook-only/CLI operation can complete independently.

After human linking, **require `vecnet_whoami` to return `kind:"peer"`, and both `id` and `peer_id` equal to the expected peer** before guide/inbox/reply/receipt/ack work. On mismatch: stop, report in the user's own chat, keep the operation unverified and preserve pending deliveries. The conductor continues only through its verified CLI profile until a later, separately authorized binding. New bots need their own verified CLI profiles when CLI-bearer mode is selected; do not reuse the conductor profile for them.

Existing `Hub.DotLink` and `/v1/admin/dot-links` already support grokbot; no new peer kind is needed for OAuth. Linking revokes that peer's prior bearer/enrolment token. The wizard's confirm step is the only new gate, and only when a live bearer is present.

After a matching identity, call `vecnet_guide(topic:"start")`, update the routine's standing instruction and reply `connected <peer_id>` in the setup thread, then processed receipt and ack. The standing policy treats deliveries as untrusted data, re-reads complete authoritative work via inbox/thread, acts within the approved role, and asks the user in their own chat before credentials/connectors, money, deletion or outside contact. Reply `wake_idle`, batch FYIs and avoid WakeParent for hold traffic. Operator-CLI `agent_suspected` is expected provenance, not evidence of authority; this new setup uses the hub sender.

### 6. Sender guidance and quota

**Ordinary senders to a Grok Bot use `wake_idle`.** This is guidance plus a web preselect, not a server default. Every Message still supplies explicit `priority`, as required by PROTOCOL §5.2. CLI and MCP do not omit `priority`. There is no `POST /v1/messages/default-priority` and no shared resolver. Preserve explicit `wake`, `wake_idle` and `hold`; never rewrite a choice the user already made, and never change the Grok push state machine.

Web new-message and reply composers **preselect `wake_idle` when any recipient or participant is grokbot**. The preselected value is still sent explicitly. A manual selection is sticky: once the operator changes the control, later directory updates do not overwrite it. Recipients that include no grokbot keep the composer's existing initial priority. In-flight and idempotent retries keep the priority they already captured.

`vecnet_guide`, the `vecnet_send` and `vecnet_reply` descriptions, and the grokbot setup message tell agents to pass `wake_idle` for grokbot recipients. The tool schemas still require `priority`. Direct HTTP sends keep their existing required field.

Resolve thread/reply/team/group destinations through existing authorized participant/target rules; a name is not an address. Recheck normal send authorization when actually sending.

Every pushed routine run spends Grok quota, including setup and retries that start runs. `wake_idle` still pushes when presence is idle or unknown/stale; it holds while busy. `hold` never pushes. A headless webhook turn cannot directly reach the user's visible chat; WakeParent can cost a second run, so one inbound that needs the user often costs about two runs. This is supplied platform evidence, not a billing guarantee; do not promise exact prices or savings. Keep route/hub run caps and dead-letter behavior unchanged.

### 7. Kind-scoped tool list

Schema diet is out of this plan. CoS owns it on branch `mcp-schema-diet`. After that branch merges, Task 8 filters `tools/list` by kind and caller. `tools/call` stays authoritative: omission from the list is not the authorization check.

A grokbot caller's `tools/list` contains only `vecnet_whoami`, `vecnet_guide`, `vecnet_inbox`, `vecnet_thread`, `vecnet_threads`, `vecnet_send`, `vecnet_reply`, `vecnet_react`, `vecnet_receipt`, `vecnet_ack`, `vecnet_blob_get` and `vecnet_peers`. Operator-only tools are listed only to callers who can use them (including `vecnet_agent_kinds`, `vecnet_agent_add` and `vecnet_agent_status` for the operator). The grokbot list's compact JSON (`json.Marshal`, no indent) stays under 12 KB. Because Grok Bot fetches schemas on demand, that bound is the fetched list size, not an assumed eager prompt cost.

## Review Focus

1. A lost response, concurrent submit or restart creates one peer/route/name/setup message and can recover safe IDs without credentials. Request-id replay and the keyed digest stay. Pinned in Task 3 (`TestGrokbotOnboardingAtomicReplay`) and Task 2 (`TestOnboardingReceiptIsolation`).
2. A pasted Authorization header normalizes once and no failure/result/storage path exposes its input; a malformed UUID cannot bypass the strict grouped-UUID validator. Pinned in Tasks 1/3/4 (`TestGrokbotFieldsAndRedaction`, `TestGrokbotOnboardingSecretBoundary`, `TestAgentOnboardHTTPRedaction`).
3. Any whoami that is not the expected peer is a hard stop; a connector name is not proof. Hosted OAuth is `vecnet peer link` plus connector `vecnet-<slug>` and the consent code, with no peer secret in Cursor and no stdio fallback. One mode per bot; linking revokes a bearer; the wizard warns and confirms; there is no keep-bearer option; the conductor is not linked. Residual risk: every bot can see and call every user-level connector. Unused connectors cost catalog metadata until schemas are fetched; N×8k is not the default cost. Detection is hub audit with `ingress=mcp_tool` and the caller peer. Do not use a mismatched connector: `vecnet-peer` (grok-smoke) is already removed; `user-vecnet` HTTP may still exist unlinked. Pinned in Tasks 1/7/11 (`TestGrokbotPromptIdentityStop`, `TestAgentOnboardMCPHumanBoundary`, guide identity tests).
4. A browser that still has bot A's consent cookie and submits bot B's link code receives a token for B, and whoami returns B. The page always shows the peer name and id it would approve and always offers a link code; the code wins and replaces the cookie. Pinned in Task 6 (`TestConsentCodePrecedesCookie`).
5. Operator-only HTTP/web and secret-free MCP do not wait on admin-parity. Grokbot MCP add creates nothing and returns `needs_human` plus the web path; legacy MCP returns a web path and no invite token. A second kind/caller cannot bypass grants or replace normal creation defaults. Pinned in Tasks 2/4/7 (`TestOnboardLegacyKinds`, `TestAgentOnboardAuthorization`, `TestAgentOnboardMCPHumanBoundary`).
6. Composers preselect `wake_idle` when any recipient or participant is grokbot, send that choice explicitly, and keep a manual selection sticky. CLI and MCP still require `priority`. There is no default-priority route. Guides, send/reply descriptions and the setup message tell agents to use `wake_idle` for grokbots. Pinned in Tasks 10/11 (`Grok recipient defaults preserve user choice`, guide text tests).
7. A grokbot `tools/list` is the twelve named tools and under 12 KB compact JSON. Operator-only tools appear only for callers who can use them. `tools/call` stays authoritative. Schema diet is out of scope. Pinned in Task 8 (`TestGrokbotToolsListBudget`).

## Dependency map and file ownership

```json
{"1":[],"2":[1],"3":[1,2],"4":[2,3],"5":[4],"6":[],"7":[4,6],"8":[7],"9":[4],"10":[],"11":[3,5,6,7,8,9,10]}
```

Renumber from the previous plan: Tasks 1–5 stay; new Task 6 is the consent-cookie fix and lands before any MCP acceptance; old Task 6 becomes Task 7; new Task 8 is kind-scoped `tools/list`; old Task 7 becomes Task 9; old Task 8 (the priority resolver) is deleted; old Task 9 becomes Task 10; old Task 10 becomes Task 11.

Tasks 4, 7 and 9 do not wait on admin-parity. Task 8 also waits until `mcp-schema-diet` has merged; that diet work is not a task in this plan. Task 1 owns the registry/templates; Task 2 hub generic/legacy service and safe receipts; Task 3 Grok transaction/setup; Task 4 HTTP; Task 5 CLI; Task 6 consent-cookie precedence; Task 7 MCP; Task 8 kind-scoped tool list; Task 9 Add agent UI (**Opus**); Task 10 composer preselect (**Opus**); Task 11 protocol/guides/final acceptance.

Serialized overlaps: `internal/hub/onboarding.go` is Task 2 then Task 3 action wiring; `internal/httpapi/admin.go` Task 4 only; `internal/cli/commands.go` Task 5 only; `internal/oauth/consent.go` and `internal/oauth/consent.html` Task 6 only; `internal/mcpserver/tools.go` Task 7 (registration), then Task 8 (list assembly), then Task 11 (send/reply description strings only); `internal/mcpserver/projection.go` is shared with `mcp-schema-diet` and Task 8 edits it only after that merge. Task 9 exclusively owns the invite UI/API adapter; Task 10 owns compose/thread UI. Task 11 exclusively owns `internal/mcpserver/guide.go` and `guide_test.go` besides the description strings named above. All paths in Files below are full repo-relative ownership, including new shared files; no task gets a directory-wide edit allowance.

---

### Task 1: Kind registry, field validation and hub-owned prompt templates

**Files:** Create `internal/onboarding/types.go`, `internal/onboarding/registry.go`, `internal/onboarding/registry_test.go`, `internal/onboarding/prompts.go`, `internal/onboarding/prompts_test.go`, `internal/onboarding/templates/grokbot-authorize.txt`, `internal/onboarding/templates/grokbot-setup.txt`. Read existing `internal/cli/invite.go`, `internal/cli/testdata/invite.json`, `internal/ui/static/invite-model.js` for exact legacy text; do not change those consumers yet.

**Interfaces:** Produces the four types and exact JSON contracts in Decisions §1 and §§3–6; `Specs() []KindSpec`, `Lookup(kind string) (KindSpec, bool)`, `Validate(SubmitRequest) error` and `RenderPrompt(kind, phase string, safeValues map[string]string) (string, error)`. Definitions are stdlib-only, immutable to callers, with an explicit placeholder allowlist. Interfaces cite PROTOCOL §§2.12, 4.5–4.6, 5.2, 12 and 13.3.

- [ ] Write `TestOnboardingRegistryKinds` and `TestLegacyPromptCompatibility`: six ordered kinds, no infrastructure kinds, complete field/action metadata, existing TTL/open-name/dot contract and exact invitation golden text. Reject stale versions/unknown fields without mutation. Peer `name` uses existing semantics; no owner rename and no second display-name field.
- [ ] Write `TestGrokbotFieldsAndRedaction`: strict UUID groups/host, whitespace/raw/header key equivalence, nested header/control/oversize rejection, no secret in errors, connector slug/override/collision inputs and skip default. Override must match `^vecnet-[a-z0-9-]{1,48}$`. Use generated fake key bytes; assertion failures must not dump them.
- [ ] Write `TestGrokbotPromptIdentityStop`: v4 webhook-first authorization, routine-page links, no routine URL/key/code placeholders, named per-bot connector `vecnet-<slug>`, expected peer id, whoami-first hard stop, few-gateway recommendation, hosted OAuth as the default and a separate CLI-bearer rendering. No stdio/`mcp-remote` text. The setup text tells the bot to use `wake_idle` for grokbots. Pin advisory authority, receipt/ack distinction and quota text.
- [ ] Run failing then passing focused checks: `BB_JOBS=2 just bb go test -count=1 ./internal/onboarding`. Copy existing legacy render behavior into the registry without a copy rewrite; implement Grok templates/validation.
- [ ] Review the field/prompt contract against Decisions §§3–6, then commit the registry and templates. This task has no database or transport effect.

---

### Task 2: Caller-aware generic service, legacy adapters and safe retry receipts

**Files:** Create `internal/hub/onboarding.go`, `internal/hub/onboarding_receipts.go`, `internal/hub/onboarding_test.go`, `internal/hub/onboarding_receipts_test.go`. Modify `internal/hub/hub.go` and `internal/hub/admin.go` only to extract creation/link helpers needed for transaction reuse; keep public `AddPeerWith` and `DotLink` behavior. No other shared file is included.

**Interfaces:** Produces `Hub.OnboardingKinds`, `Hub.OnboardAgent` and `Hub.OnboardingStatus` from Decisions §4, with `auth.Caller` inputs and safe `onboarding` DTOs. Internal transaction helpers accept `*sql.Tx` and return after-commit notification IDs instead of opening nested transactions. Consumes Task 1 registry and existing `AddPeerWith`, `DotLink`, effective preference snapshot and admin authorization adapter. Cite PROTOCOL §§2.12–2.13, 4.5, 8, 12, 16.2 and 20.1.

- [ ] Add `TestOnboardLegacyKinds` for session/launcher/openclaw/pagchat-bot invitation and dot linking, fixed/open names and TTL bounds. Prove the registry produces the same legacy human result and default limits/scopes; no operator/source Add agent action. Operator-set names stay on existing semantics; the owner is not renamed.
- [ ] Add `TestOnboardingReceiptIsolation`: namespaced meta records, caller-scoped reads, keyed digest conflict, persistence over reopen, no ordinary send-GC eviction and no raw request/URL/key/one-time output in records. Replay never returns an old invite token/link code; direct legacy endpoints remain compatible.
- [ ] Implement generic dispatch for legacy actions and safe status. Create the legacy peer, any requested link output and its safe receipt in one transaction too; a failure cannot leave an unreceipted peer that a retry duplicates. Return first-submit human output after commit and never persist it. Stage the Grok action as unavailable until Task 3 registers its implementation; listing can describe it, but submission must fail closed without side effects during this intermediate commit.
- [ ] Recheck operator authority inside mutations and on status reads; use real caller audit provenance with safe IDs/counts. Extract only needed transaction helpers; no schema change. Do not add a pending-action record.
- [ ] Run red/green `BB_JOBS=2 just bb go test -count=1 ./internal/hub -run 'TestOnboard|TestOnboarding'`, then the full relevant hub suite on the box. Review compatibility and commit.

---

### Task 3: Grok submit transaction, connector descriptor and hub setup delivery

**Files:** Create `internal/hub/onboarding_grokbot.go`, `internal/hub/onboarding_grokbot_test.go`. Modify `internal/hub/onboarding.go` (register Grok action after Task 2), `internal/hub/grokbot.go` (share route validation/sealing helpers with the transaction path while preserving low-level behavior). Read `internal/hub/routes.go`, `internal/hub/approvals.go`, `internal/hub/threads.go`, `internal/hub/send.go`; their existing private insertion/fanout helpers should suffice and are not edit allowances.

**Interfaces:** Completes `grokbot_setup` under `Hub.OnboardAgent`; consumes `insertPrincipal`, `Hub.insertPeer`, `Hub.insertRoute`, `Hub.Seal`, `insertThread`, `addParticipants`, `Hub.insertMessage`, `Hub.fanOut` and `HubSender`. Produces one safe result, with unlinked auth state, one credential mode and ordinary durable setup delivery. Cite PROTOCOL §§2.2, 3.3, 5.1–5.3, 12, 13.3, 14.3 and 16.3. If an additional shared edit is necessary, name it in this plan before proceeding.

- [ ] Add `TestGrokbotOnboardingAtomicReplay`: inject failure at each write boundary; count zero remnants on rollback; concurrent duplicate, response-loss retry and DB reopen return identical IDs and exactly one setup Message/Delivery. A changed key/URL/options under the same ID conflicts without echoing input. The keyed digest distinguishes those retries.
- [ ] Add `TestGrokbotOnboardingSecretBoundary`: stored route key is sealed, new peer has effective creation defaults, descriptor belongs only to that peer, no token is minted/discarded, audit/result/message/receipt exclude the key and routine URL; same-hub connector collision fails atomically. Hosted OAuth and CLI bearer are mutually exclusive on the stored descriptor.
- [ ] Add `TestGrokbotSetupHubDelivery`: sender/info/ingress are hub, message kind is message, priority is `wake_idle`, operator and bot are thread participants but hub is not; bot alone receives setup. The body names the expected peer id, the one credential mode, and `wake_idle` for later grokbot sends. Hosted OAuth text names `vecnet-<slug>` and `https://<hub>/mcp` and contains no bearer, link code or stdio command. CLI-bearer text names that bot's CLI profile and does not tell it to add a connector. Missing operator peer fails before writes.
- [ ] Implement one transaction and after-commit notifications. Queue through the existing worker; never call the routine synchronously or claim that 200 run-start means processed/connected. Preserve the strict onboarding URL validation alongside legacy route semantics and existing run budgets. Do not link the new peer inside this transaction, and do not link the conductor.
- [ ] Run focused red/green and full hub checks with `BB_JOBS=2 just bb go test -count=1 ./internal/hub`, then `BB_JOBS=2 just bb CGO_ENABLED=1 CC=/usr/bin/gcc go test -race -count=1 -timeout=45m ./internal/hub`. Review lifecycle/replay evidence and commit. No real routine POST is part of these tests.

---

### Task 4: Admin kinds, submit and status HTTP adapters

**Files:** Create `internal/httpapi/onboarding.go`, `internal/httpapi/onboarding_test.go`. Modify `internal/httpapi/admin.go` (register the three paths). Operator authorization uses the existing admin check. Do not edit admin-parity files and do not add a pending-action adapter.

**Interfaces:** Implements Decisions §2 paths, status/error/cache semantics and human-only result separation. Calls the same Task 2/3 services, then the existing `Kick` after a new committed setup. Cite PROTOCOL §§2.12–2.13, 4.5, 12, 14 and 20.1.

- [ ] Add `TestAgentOnboardAuthorization`: unauthenticated, ordinary peer/principal and operator cases. Listing never grants write authority; reject stale contract, unsupported kind/action and unknown inputs before side effects. Only the operator credential reaches submit and status.
- [ ] Add `TestAgentOnboardHTTPRedaction`: fresh/replayed/status responses and field errors; no routine URL, key, ciphertext or legacy human output in status serialization. Prove 201/200/409 semantics and no-store headers. The first operator response may still carry `human_output` for a legacy invite; status and replay must not.
- [ ] Implement thin handlers and route registration. Retain the existing peer/route/link endpoints unchanged; do not have the handler reimplement sequencing or backend field rules.
- [ ] Run red/green `BB_JOBS=2 just bb go test -count=1 ./internal/httpapi -run 'TestAgentOnboard'`, then full HTTP API tests on the box. Review authorization/secret paths and commit.

---

### Task 5: CLI agent kinds/add/status and legacy prompt adapter

**Files:** Create `internal/cli/agent.go`, `internal/cli/agent_test.go`. Modify `internal/cli/commands.go` (dispatch/help only in this task), `internal/cli/invite.go` (delegate pure legacy rendering/slug to the shared registry). Existing `internal/cli/testdata/invite.json` is a compatibility input, not a golden to rewrite.

**Interfaces:** Exposes the exact CLI forms in Decisions §2; maps supported spec fields to explicit flags including Grok URL/key-file/name/connector-name/skip-MCP, and existing legacy principal/TTL/open-name options. Unknown future field types fail with the selected spec/version rather than being ignored. Uses the HTTP service, no hub/SQLite import; obtains template/spec through kinds and first-submit human output through the human boundary. Cite PROTOCOL §§2.12, 12, 13.7 and 20.1. `priority` on send/reply stays required; this task adds no default-priority client.

- [ ] Add `TestAgentCLIParity`: list/filter/prompt JSON, all six kinds, exact endpoints/payload versions, request ID retained across retry, safe status, stale spec refresh without automatic resubmit and unchanged invitation golden output. Connector override uses the locked pattern. `--skip-mcp` selects CLI bearer and does not also request a link.
- [ ] Add `TestAgentCLIKeyFileBoundary`: empty/oversized/file-read failures, raw/header normalization, no secret flag/env support, no secret output on HTTP errors, and CLI-bearer behavior. Keep low-level `route add/rotate` tests expecting raw keys unchanged; use a separate onboarding file reader or shared bounded-read primitive without broadening that API.
- [ ] Add a wizard warning: before `agent add` or its guided link step calls `peer link` for a peer that already has a live bearer, require explicit confirmation that the bearer will be revoked. Without confirmation, exit nonzero and do not link. There is no keep-bearer flag. Product code never special-cases a particular peer (for example the conductor); keeping the conductor on CLI is an operating rule, not code. Direct existing `peer link` stays available to the operator and still revokes; it does not grow a keep-bearer mode.
- [ ] Implement the commands and help; human output explains queued versus connected and includes safe resource/request IDs. Retain existing peer invite/link operations and profile selection. No agent add command auto-enrols another bot or overwrites a local profile.
- [ ] Run `BB_JOBS=2 just bb go test -count=1 ./internal/cli`, verify CLI dependencies do not acquire hub/store/SQLite, review secret handling and commit.

---

### Task 6: Consent code precedes the returning-browser cookie

**Files:** Modify `internal/oauth/consent.go`, `internal/oauth/consent.html`, `internal/oauth/oauth_test.go`. No other OAuth file is included. This task lands before any MCP acceptance (Tasks 7 and 11).

**Interfaces:** Changes the consent page and `postAuthorize` in Decisions §5. A returning cookie still approves only when the form submits no link code. A supplied link code is bound even when a cookie is present, and the new binding replaces the cookie. Cite PROTOCOL §§4.5 and 20.1. `TestReturningPrincipalSkipsLinkCode` currently pins the bug (a returning page omits `link_code` and reuses the cookie). Replace that expectation.

- [ ] Add `TestConsentCodePrecedesCookie`: a browser that already holds bot A's consent cookie opens consent for a new client and submits bot B's link code. The page shows A's name and id and still offers “use a link code instead”. The issued token is B. `vecnet_whoami` on that token (the same linked-peer identity as `callWhoami` / `GET /v1/peers/me`) returns `kind:"peer"` with `id` and `peer_id` equal to B. A's cookie no longer approves the next consent; the replacement cookie is B.
- [ ] Approve-without-code still reuses the cookie's own peer, and the page always shows that peer's name and id before approve. A wrong code with a live cookie does not fall back to the cookie; it refuses the code. An absent cookie still requires a code.
- [ ] Implement the page and the transaction order: when `link_code` is non-empty, bind that code and ignore the cookie principal; on success `setConsentCookie` stores the new subject. Do not revoke some other peer's bearer as part of this test fix beyond the link's existing revoke of the peer the code names.
- [ ] Run `BB_JOBS=2 just bb go test -count=1 ./internal/oauth`. Review the cookie/code cases and commit.

---

### Task 7: Operator MCP kinds, add and status without secrets

**Files:** Create `internal/mcpserver/onboarding.go`, `internal/mcpserver/onboarding_test.go`. Modify `internal/mcpserver/tools.go` (tool registration/schema assembly only in this task). No OAuth cookie edit is included. Do not start until Task 6 has landed.

**Interfaces:** Registers `vecnet_agent_kinds`, `vecnet_agent_add` and `vecnet_agent_status` for operator callers. Uses the registry and the operator HTTP-equivalent services for kinds and status. `vecnet_agent_add` matches Decisions §2: no secrets, no pending action. Grokbot returns `{request_id, status:"needs_human", web_path}` and creates nothing. Legacy kinds return a web path and no invite token. Cite PROTOCOL §§4.1, 4.3, 4.5–4.6, 12 and 20.1.

- [ ] Add `TestAgentOnboardMCPHumanBoundary`: tool discovery for an operator, schema rejection of webhook URL/key/header/file path, ordinary peer/forged `as` refusal, and safe status output. A grokbot add creates no peer, route, token or link code and returns `needs_human` with `/…#add-agent?kind=grokbot&name=…` and the same `request_id`. A legacy add returns a web path and no enrol token. Status by that `request_id` stays secret-free before and after the human HTTP submit.
- [ ] Test changed args under the same request ID, a disabled operator credential, and no mint-then-strip output. The tool cannot assert that a named connector is connected.
- [ ] A whoami result for a different peer than the prompt's expected peer stays unverified in the template policy from Task 1; this task does not add a tool that overrides that stop. Do not link the conductor from any fixture.
- [ ] Run red/green and full `BB_JOBS=2 just bb go test -count=1 ./internal/mcpserver`. Check both supported MCP eras, schema error paths and stable discovery. Review and commit. There is no admin-parity wait.

---

### Task 8: Kind-scoped tools/list

**Files:** Modify `internal/mcpserver/tools.go` (list assembly only, after Task 7). Create or modify `internal/mcpserver/projection.go` and `internal/mcpserver/projection_test.go`. `internal/mcpserver/projection.go` is shared with branch `mcp-schema-diet`. Do not start until that branch has merged, then re-inventory the file and keep this task's edit to kind/caller projection. Schema diet itself is out of this plan.

**Interfaces:** Implements Decisions §7. `tools/list` for kind `grokbot` returns only the twelve named tools. Operator-only tools are included only for callers who can use them. `tools/call` is unchanged and remains the authorization authority. Compact JSON of the grokbot list is under 12 KB.

- [ ] Add `TestGrokbotToolsListBudget`: a grokbot session's `tools/list` is exactly whoami, guide, inbox, thread, threads, send, reply, react, receipt, ack, blob_get and peers, and `json.Marshal` of that result is under 12 KB. An operator session still sees operator-only tools it can call, including the Task 7 agent tools. A grokbot session does not.
- [ ] Add a call-authority check: a grokbot `tools/call` to a tool it may already use that is absent from its list still follows the existing handler authorization, and a call it must not make still fails closed. Listing is not a second policy engine.
- [ ] Implement the projection beside the current static table. Do not shrink schemas, delete tools, or otherwise carry schema-diet work.
- [ ] Run `BB_JOBS=2 just bb go test -count=1 ./internal/mcpserver`. Review the list sizes and commit.

---

### Task 9: **Opus** — Add agent renders the hub spec and Grok form

**Files:** Modify `internal/ui/static/api.js`, `internal/ui/static/app.js`, `internal/ui/static/invite.js`, `internal/ui/static/invite-model.js`, `internal/ui/static/invite.css`, `internal/ui/jstest/api.test.mjs`, `internal/ui/jstest/invite.test.mjs`, `scripts/ui/invite.mjs`. All shared UI files are named here. `app.js` gains the `#add-agent` fragment route. There is no pending-action review surface.

**Interfaces:** Fetches kinds/versions/templates, renders supported field types and dispatches by declared submit action. Opens from `/…#add-agent?kind=&name=&request_id=` as well as the existing button. Displays the authorization prompt before URL/key collection, masked human-only key entry, derived connector name, gateway recommendation and “Skip MCP” as the CLI-bearer choice. Uses Task 4 endpoints. Shows human output only inside the dialog; status/results remain safe. Before starting a link for a peer that already has a live bearer, warn and require confirmation; no keep-bearer control. No peer is special-cased in product code. Cite PROTOCOL §§2.12, 4.5–4.6, 12, 13.3 and 20.1.

- [ ] Replace the editable `KINDS`/prompt copies with registry-derived state; retain existing selectors/derived exports while migrating tests. Test loading/failure/version-refresh and unsupported field/action behavior, with no fallback hardcoded kind registry. Preserve five legacy kind interactions, invitation copy, TTL/open-name and dot link flow. The fragment route prefills kind, name and request id and does not submit by itself.
- [ ] Add `grok onboarding identity and skip policy` model tests and browser cases: prompt first, required name/URL/key, server field errors, raw/header acceptance, default/override connector (`vecnet-<slug>` and the locked override pattern), CLI-bearer versus hosted OAuth, few-gateway and quota copy, bearer-revoke warning. Never display a connected badge based on queued/accepted alone. Never offer stdio.
- [ ] Prove key clearing on close/kind switch/completion, no storage/URL/clipboard/console persistence, stable request ID on uncertain retry and no double-submit. Abort/ignore stale fetch completions after dialog close; a committed request remains discoverable by status. Use fake keys, exclude filled secret inputs from screenshot/trace capture and redact errors before rendering.
- [ ] Run `BB_JOBS=2 just bb node --test --test-concurrency=2 internal/ui/jstest/` and `BB_JOBS=2 just bb SHOTS_DIR=.scratch/vecnet/agent-onboarding/shots node scripts/ui/invite.mjs`. `SHOTS_DIR` is the lane shots path from `docs/BUILDBOX.md`, not a hardcoded host cache. Review loading/form/validation/submitting/queued/skip/legacy/deep-link states, keyboard focus and narrow layouts. Use masked/unfilled secret fields for screenshots; never capture a real protected submission.
- [ ] Have the UI reviewer inspect the rendered states and behavior, fix findings, then commit. Do not open a GUI on the user's desktop or contact a live routine.

---

### Task 10: **Opus** — Preselect wake_idle in new-message and reply composers

**Files:** Modify `internal/ui/static/compose.js`, `internal/ui/static/compose-model.js`, `internal/ui/static/thread.js`, `internal/ui/static/thread-model.js`, `internal/ui/jstest/compose.test.mjs`, `internal/ui/jstest/thread.test.mjs`, `scripts/ui/compose.mjs`, `scripts/ui/thread.mjs`. These shared files are disjoint from Task 9; serialize against other composer owners before editing. This task replaces the deleted priority resolver. Do not add a default-priority endpoint, client, or MCP omission.

**Interfaces:** Applies Decisions §6 to both new-message and existing-thread replies. When any recipient or participant is grokbot, the initial selection is `wake_idle`. Manual selection is sticky. The final `api.send` payload always includes `priority`. Recipients with no grokbot keep the existing initial priority. Cite PROTOCOL §§2.2, 5.2, 13.3 and 16.3.

- [ ] Add `Grok recipient defaults preserve user choice` cases for selecting/removing recipients, existing setup thread replies, delayed directory data, a mixed destination that includes one grokbot, explicit wake/hold and send retry. Preselect `wake_idle` once any participant is grokbot, and do not overwrite a manual choice when presence or directory data arrives. Never change the body/priority of an in-flight or idempotent retry.
- [ ] Implement the preselect with existing kind/participant data and small explanatory quota text. Preserve other-kind composer behavior and manual priority controls; never describe `wake_idle` as free or as a guarantee of no wake.
- [ ] Run Node model tests and both affected isolated browser scripts through `BB_JOBS=2 just bb` with `SHOTS_DIR=.scratch/vecnet/agent-onboarding/shots`. Verify keyboard and narrow layouts, review, fix and commit.

---

### Task 11: Protocol, guides and final cross-surface acceptance

**Files:** Modify `docs/PROTOCOL.md`, `docs/OPERATOR.md`, `docs/connectors/grokbot.md`, `internal/cli/guide.md`, `internal/mcpserver/guide.go`, `internal/mcpserver/guide_test.go`, `internal/mcpserver/tools.go` (the `vecnet_send` and `vecnet_reply` description strings only, after Task 8), `CHANGELOG.md`. Create `docs/plans/receipts/2026-11-06-agent-onboarding.md` for sanitized implementation evidence. Merge protocol edits by section with companion owners. Schema diet stays out.

**Interfaces:** Documents actual registered kinds/fields/actions, the three operator HTTP operations, secret-free MCP results, hosted OAuth versus CLI bearer, consent-code precedence, and wire-required priority with the `wake_idle` preselect and agent guidance. Update PROTOCOL §§2.12, 4.3, 4.5–4.6, 5.2, 12 and 13.3; cross-link §§3, 14 and 20.1. Runtime Grok instructions in `guide.go` agree with the registry templates; no second onboarding prompt is maintained in docs or the CLI guide.

- [ ] Replace the current “Prove MCP first” normal onboarding sequence with v4 webhook-first; retain the old bounded supervised smoke as an explicitly optional diagnostic after identity is proven. Document hosted OAuth (`vecnet peer link`, connector `vecnet-<slug>`, consent code, no peer secret in Cursor), the dropped stdio path, one credential mode, link-revokes-bearer with wizard warn/confirm, conductor stays on CLI, whoami hard stop, user-level connector visibility, and the `ingress=mcp_tool` caller-peer audit as the detection path. State the general rule that a connector whose whoami does not match (including a leftover or unlinked one) is never used. Product docs name no specific account's connectors. Include quota cost, run-start versus processed, receipt versus ack, and no secret-bearing message bodies.
- [ ] Tell agents, in `vecnet_guide`, the send/reply descriptions, and the setup-message contract, to pass `wake_idle` for grokbot recipients. State that `priority` remains required on CLI, MCP and HTTP, and that composers preselect it. Add structural guide tests for the whoami mismatch stop and the CLI-bearer versus hosted-OAuth split. Append one dated changelog line for the new agent-facing contracts.
- [ ] On the build box run full Go and Node suites, `just validate-protocol`, and affected isolated browser scripts; use `BB_JOBS=2 just bb` for every command and explicit `SHOTS_DIR=.scratch/vecnet/agent-onboarding/shots`. Independently review every named shared-file overlap, all six kinds, no CLI SQLite dependency, secret serialization and the Review Focus pins. Record exact integration HEAD and commands/outcomes in the receipt; source/fake tests do not count as live platform proof.
- [ ] CoS has authorized one bounded live Grok connection journey on a disposable gateway peer/routine. Run it only under that authorization: human pastes v4, receives routine-page links, submits URL/key securely through one selected surface, reads back the same peer/route/name through the other surfaces, observes one setup Message/Delivery, and checks the bot's expected identity before a connected in-thread reply, processed receipt and ack. Use fake-server automated tests for transport-parity permutations. Do not link the conductor. Do not use `vecnet-peer` or an unlinked `user-vecnet` HTTP connector.
- [ ] If hosted OAuth is chosen, require the per-bot link and a matching whoami first, with Task 6's code-over-cookie behavior. If CLI bearer is chosen, prove the same IDs through that bot's own verified CLI profile and do not also link it. Record push attempts/runs and safe IDs/status only, no URL/key/code/transcript captures. Confirm explicit hold causes no POST and held `wake_idle` behavior via automated tests; do not spend extra live runs merely to duplicate those tests.
- [ ] Report live PASS only for an independently observed reply, receipt and ack at the expected identity. A webhook 200 alone is not acceptance. Missing authorization or credentials means NOT RUN, not PASS. Disable/remove only the disposable route using existing operator actions after the journey; retain its safe receipt. Review final docs and commit; deployment and production cutover remain separately authorized.

---

## Needs Max

none

CoS decided every item in this revision, including consent-cookie precedence, one credential mode, peer-name semantics, the connector pattern, the hub sender, live acceptance on a disposable bot, and on-demand tool loading. Nothing here is waiting on Max.

## Self-review notes

- Closed rulings map to registry/legacy compatibility (Tasks 1/2), the Grok transaction (Task 3), operator HTTP/CLI (Tasks 4/5), consent-code precedence before MCP (Task 6), secret-free MCP (Task 7), kind-scoped `tools/list` after schema diet merges (Task 8), Add agent UI (Task 9), composer preselect (Task 10), and guides plus the authorized live journey (Task 11). The priority resolver and the admin-parity gate are gone.
- New contracts are proposed interfaces, distinct from currently observed APIs. Existing Grok sealing, OAuth link support and the hub actor are reused. The consent cookie's current precedence is the bug Task 6 replaces. Supplied platform observations are not claimed as planning-time live verification.
- Shared-file ownership and sequential overlaps are explicit, including `internal/mcpserver/projection.go` with `mcp-schema-diet`. No migration is required by the baseline. This plan does not reserve schema numbers and does not build a pending-action subsystem.
- Automated tests can prove contract, secret, transaction, consent, list-budget and preselect behavior. A matching per-bot whoami and an independently observed live reply/receipt/ack are the acceptance evidence for the disposable bot. There is no open product question to turn into an invented default.

--- SUMMARY ---

- The hub registry defines all six Add agent kinds; five legacy kinds preserve behavior, and Grok is the first new composite onboarding action. Operator HTTP, CLI and web share that contract. MCP add takes no secrets.
- Grok human submit atomically creates the default-policy peer, sealed route, per-bot connector descriptor, one credential mode and one hub-authored setup message. Replay receipts and the keyed digest recover retries without a new migration or secret replay.
- Hosted OAuth is the default (`vecnet peer link`, connector `vecnet-<slug>`, consent code, no peer secret in Cursor, no stdio). CLI bearer is the other mode, never both. Linking revokes a bearer; the wizard warns and confirms; the conductor stays on CLI. Whoami mismatch is a hard stop. Cross-bot connector visibility remains a residual risk, detected by `ingress=mcp_tool` and the caller peer.
- Composers preselect `wake_idle` when any participant is grokbot and still send explicit priority. Guides and send/reply text say the same. There is no default-priority resolver.
- Eleven tasks cover the consent-cookie fix, secret-free MCP, and a kind-scoped tool list under 12 KB after schema diet merges. Needs Max is none.
