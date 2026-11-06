# Hub-Owned Agent Onboarding Kinds Implementation Plan

> **For agentic workers:** Implement task by task with the house task/review gates in `docs/plans/2026-11-05-hub-notices.md`. Workers receive their task, the contracts below and Global Constraints. Steps use checkbox (`- [ ]`) syntax. A `---` ends every task. This document is a plan; its creation does not authorize implementation, migration application, deployment or live hub changes.

**Goal:** The hub describes each supported agent kind's onboarding prompt, validated inputs and submit action once. CLI, operator MCP and web expose that same operation. Grok Bot is the first complete new instance: webhook first, one peer and connector identity per gateway bot, a sealed push route and one advisory setup message.

**Architecture:** Add a stdlib-only `internal/onboarding` contract/registry package, consumed by caller-aware hub services and thin HTTP, CLI, MCP and web adapters. Keep transport credentials behind the existing human invitation/key-entry boundary. Grok Bot submission writes the peer, route, connector descriptor, setup thread/message/delivery and safe retry receipt in one transaction; the existing push worker performs delivery after commit. Existing session, launcher, openclaw, pagchat-bot and dot behavior moves onto the registry without changing enrolment or linking semantics.

**Tech Stack:** Existing Go 1.27.1, SQLite, stdlib templates/JSON/crypto, existing MCP JSON-RPC/schema machinery and vanilla browser ES modules; Node 22.23.3 for browser-model tests. No new runtime dependency or separate Grok connector process. All builds/tests run through `just bb` per `docs/BUILDBOX.md`.

**Spec:** cc-vecnet-cos's locked 2026-11-06 owner decisions in this assignment; `/home/xertrov/src/vecnet4/.scratch/vecnet/integrations/cos-reply-2026-11-06-platform-facts.md` (conductor v4 and setup inputs) and `grokbot-onboarding-reply-2026-11-06.md` (v4 setup message and identity caveats); `docs/connectors/grokbot.md`; `docs/PROTOCOL.md` §§2.2, 2.12–2.13, 3, 4.1, 4.3, 4.5–4.6, 5.1–5.3, 8, 12–14, 16.2–16.3 and 20.1. Locked decisions take precedence over earlier drafts and the current MCP-first smoke sequence.

**Starts from:** `master` `0bf4beb350f4696004b4d5db1dcb103bf19a4689`, branch `plan/agent-onboarding`. This inventory is source inspection, not independently rerun platform or live-hub evidence. The scratch sources' “verified” observations are supplied evidence.

## Global Constraints

- **Plan only for this assignment.** Edit and commit only this document; write the separate result JSON and completion notifications required by the assignment. Implementation tasks below remain unchecked. No implementation, migration application, live connection test or hub change occurs during planning. No `attn`.
- **Generic first, Grok Bot first instance.** The hub owns prompt templates, field definitions/validation and submit-action dispatch. Web renders the spec; CLI and operator MCP have operation parity. Preserve the existing five kinds' behavior and copy initially. Peer kinds `operator` and `source` are not Add agent choices.
- **Identity hard stop:** the currently shared MCP connector authenticates as **grok-smoke**, not vecnet-conductor/the newly enrolled bot. Until a credential is bound to the intended bot and whoami proves that binding, use only that bot's correctly identified CLI profile, e.g. `vecnet --net gb-vecnet-conductor …` for the existing conductor. Never use a mismatched MCP connection, send `connected`, receipt or ack through it. A connector name alone is not credential isolation.
- **Few gateway bots, each with its own peer and connector `vecnet-<bot-slug>`.** This is the Max-confirmed direction relayed by CoS, not one connector for every bot or one shared peer for the account. Keep the recommendation prominent in the prompt, form and guide.
- Never print, log, echo or return a webhook key, Authorization value, link code or peer credential to a model. Routine URL/key are copied by the human from the routine page into the protected form or CLI; never paste either into chat, prompts, setup messages, diagnostics or receipts. Human invitation paste blocks retain their existing intentional output boundary; they never become MCP results.
- New onboarding accepts a raw key or a leading `Bearer ` header value, normalizes it in memory and seals only the raw key. Redaction covers validation errors, failed requests, panics, browser errors and test failure output. Keep the existing low-level route add/rotate commands' raw-key compatibility contract unchanged.
- Use existing scope, limits, enrolment, OAuth and egress services. Do not infer operator authority from a bot kind or from arbitrary webhook text. The setup message is advisory; the user's pasted conductor-v4 prompt authorizes only its stated actions.
- Prefer **no migration**. Registry definitions are code; existing peers/routes/messages/deliveries and namespaced `meta` receipts suffice. The DB build owns 0026. If integration proves new schema unavoidable, return the concrete reason to CoS and use the **next free number at build time; 0027 is currently expected**. Never reserve a number or edit an existing migration.
- **Tests and builds run on build boxes only:** `BB_JOBS=2 just bb …`, including Node/browser checks, protocol validation and race tests. Never heavy local builds on xsm. Use lane temp directories from `docs/BUILDBOX.md` and `/mnt/build-cache` for workstation scratch/cache/artifacts; never `/tmp`. Use fake credentials, injected clocks and in-process HTTP servers in automated tests. No real config, `.data/`, `.demo-data/`, development hub or production endpoint.
- UI implementation tasks are **Opus**. Name every shared file in every task that edits `internal/hub/**`, `internal/httpapi/**`, `internal/mcpserver/**` or `internal/ui/static/**` (the actual UI location; there is no top-level `ui/` in this checkout). Serialize the named overlaps below. Re-inventory after companion merges; do not edit `ui-addagent-kinds` or copy its implementation.

## Decisions and wire contracts

### 1. Current interfaces and the registry boundary

`internal/hub/hub.go:PeerKinds` accepts `grokbot`, `dot`, `openclaw`, `pagchat-bot`, `session`, `launcher`, `operator` and `source`. `Hub.AddPeerWith(ctx, AddPeerOpts)` creates an implicit principal when needed, loads preference-derived send/thread limits through `insertPeer`, and issues enrolment tokens except for dot. The five unchanged Add agent kinds retain those exact policies, TTL choices, open-name rules, single-use output and dot linking.

`internal/ui/static/invite-model.js` currently hardcodes the six Add agent choices and mirrors CLI `inviteSlug`/`inviteBlock` from `internal/cli/invite.go`; `internal/cli/testdata/invite.json` pins both. `invite.js` calls `POST /v1/admin/peers`, with a separate dot-link panel. Move these definitions behind the hub registry; keep compatibility exports as derived adapters while their callers move, not a second editable registry. `format.js` can retain directory labels; it does not decide onboarding behavior.

Proposed shared Go types in `internal/onboarding/types.go` are `KindSpec`, `FieldSpec`, `SubmitRequest` and `SubmitResult`. Freeze their JSON contracts in Task 1:

- `KindSpec = {kind,label,spec_version,prompt_template,fields,submit_action,send_defaults}`. `spec_version` is an opaque revision of the kind contract. `fields` describe string/secret/boolean/integer/enum inputs, requiredness, defaults, bounded length/range/pattern, help and `human_only`. `submit_action` is a declarative ID (`invite`, `oauth_link`, `grokbot_setup`), never executable client code, arbitrary URLs or a client-selected backend function. Prompt placeholders are an explicit allowlist. `send_defaults` includes `priority:"wake_idle"` for grokbot.
- `SubmitRequest = {kind,spec_version,request_id,inputs}`. The client generates one stable random request ID per submission and retains it across uncertain retries. Reject unknown fields, unsupported actions and a stale spec version before mutation. `inputs` are validated against the selected spec on the server; frontend validation is assistance only. Grok secret inputs are accepted only on the authorized human/CLI path. MCP receives a separate secret-free schema.
- `SubmitResult` has safe metadata `{request_id,kind,peer_id,principal_id,route_id?,connector_name?,setup?,auth_state,send_defaults}`. `setup = {thread_id,message_id,delivery_id,state}` distinguishes queued/held, accepted, processed, failed, expired and dead-letter states. `auth_state` distinguishes unlinked/pending from independently verified identity; route acceptance never implies connected. A first successful legacy human submission may also return the existing invitation/link output in a separate `human_output`; status/replay/MCP serialization excludes it. Existing trusted human endpoints remain compatible.

The registry is code-owned; this plan adds no editable kind-definition API or database registry. All six specs are listed in the existing order: session, launcher, openclaw, pagchat-bot, grokbot, dot. Adding a future kind needs a registered server action plus parity tests, rather than another UI branch.

### 2. Operation parity, authority and secret entry

Proposed HTTP paths are `GET /v1/admin/agent-kinds`, `POST /v1/admin/agents` and `GET /v1/admin/agents/onboarding/{request_id}`. They map to CLI `vecnet agent kinds [--json]`, `vecnet agent add --kind KIND …` and `vecnet agent status REQUEST_ID [--json]`; operator MCP tools are `vecnet_agent_kinds`, `vecnet_agent_add` and `vecnet_agent_status`. Kind listing supplies the prompt template as well as fields; `agent kinds --kind KIND --json` selects one spec. Successful new creation is HTTP 201; a same-request replay is 200; bad shape is 400, bad field value 422 with field path and fixed safe text, stale spec/request mismatch 409, unauthorized 401/403. Reads and writes are `Cache-Control: no-store`.

Use the [admin parity plan](2026-11-05-admin-parity.md) operation manifest and human-request contracts, not a parallel permission or credential system. Its Tasks 1/2/4/7 provide operation registration, transactional authorization and protected pending actions. Register this composite operation with the required peer-create, route-write and credential authority, intersected with any delegation boundaries/ceilings. At the current base, only the operator credential has these admin powers; do not loosen the prefix to admit every peer. Before parity lands, operator-only HTTP/CLI and offline adapters can progress; MCP secret-bearing submit remains dependent on its human-request service.

MCP `vecnet_agent_add` accepts kind, spec version, request ID and safe name/options only. It accepts **neither webhook URL/key nor a server-local file path**, and never creates an enrol token then discards it. For Grok key entry or legacy credential issuance it requests the parity plan's pending action. The result is only `{request_id,status:"awaiting_human",revision,review_path,expires_at}`; the authenticated human supplies secret fields and confirms there. Pending status/cancellation use the existing request APIs and corresponding parity CLI/MCP operations; onboarding status handles the completed operation's safe receipt. No effect happens before human confirmation. Final confirmation rechecks issuer and approver authority. Do not treat a request ID or review path as an authorization capability.

CLI minimum example (the URL and path are human inputs, not model-visible samples containing real values): `vecnet agent add --kind grokbot --url URL --key-file PATH --name NAME [--skip-mcp] [--connector-name NAME]`. Read the key file in-process, bounded to the existing 4 KiB limit; recommend mode 0600 and never put its contents in argv/environment/output. `--key-file` is a local CLI concern; send normalized field values over authenticated HTTPS. The CLI obtains the kind/version contract and shows the same safe result as web. Existing `peer invite`, `peer link` and `route add/rotate` continue to work.

### 3. Grok fields, names and input normalization

Grok inputs are `name`, `webhook_url`, `webhook_key`, optional `connector_name`, and boolean `skip_mcp` default false. The hub URL comes from trusted hub configuration, not an arbitrary form field. The prompt is shown before submission so the human can authorize the bot and obtain routine-page links first.

- `webhook_url`: require exactly `https://api2.cursor.sh/automations/webhook/<lowercase-uuid>`, with UUID groups 8-4-4-4-12; no query, fragment, userinfo, port or other host/path. Also use the existing egress policy and redirect refusal. The current `GrokbotURLPattern` checks only 36 lowercase hex/hyphen characters; the onboarding validator must enforce the stricter grouped UUID without silently broadening legacy route behavior.
- `webhook_key`: trim surrounding paste whitespace, strip one leading `Bearer ` (case-insensitive scheme), then apply the existing printable raw-key validator with its prefix and nonempty suffix. Refuse nested schemes, embedded whitespace/control characters and oversized input; fixed error text never includes supplied bytes. Form input is masked, has no reveal/copy result, and is cleared on kind switch, submit completion and dialog close. Do not cache it in storage or request receipts.
- `name`: retain the existing operator-set peer name meaning/validation and use the same string in CLI and web. Do not silently adopt the conflicting 64/128-character or owner-renaming draft proposals. Those product questions are in Needs Max; the default build does not mutate the owner's display name.
- `connector_name`: default `vecnet-` plus the existing ASCII lowercase bot-name slug (40-character slug limit, `agent` fallback); allow an explicit `^vecnet-[a-z0-9-]{1,48}$` name. Store the chosen name against this peer in the safe receipt. Detect a same-hub collision with another peer transactionally and return `connector_name_conflict`; never silently reuse its credential or add a duplicate. External account collisions require the bot/human to check the named connector and whoami; the hub cannot enumerate Cursor's account connectors. Alternative naming product policy remains a Needs Max item.
- `skip_mcp`: still create peer, route, connector descriptor and setup message. State webhook plus an independently verified CLI profile as the operating path. Omit MCP-add/reauth/fallback instructions and do not mint MCP credentials or link codes. MCP remains recommended for gateways, optional for this bot.

“Create a connector name” means allocate a per-peer descriptor and include its exact name in the setup instructions. Creating the connector in the user's app is the bot/human's subsequent platform action; there is no hub API here that silently installs a Cursor connector.

### 4. Atomic submit, one setup message and no migration

Implement proposed `Hub.OnboardAgent(ctx, caller, onboarding.SubmitRequest) (onboarding.SubmitResult, error)`, `Hub.OnboardingKinds(ctx, caller)` and `Hub.OnboardingStatus(ctx, caller, requestID)`. Caller-aware authorization belongs inside the service, including the mutation transaction. Legacy actions delegate to current creation/linking semantics and retain the one-time human output boundary. Grok setup uses transaction-level helpers, not a sequence of independently committed public `AddPeerWith`/`AddGrokbotRoute`/send calls.

For a new Grok request, validate/seal before transaction writes, then in one transaction: authorize; claim the request/connector name; create the implicit principal and grokbot peer; apply normal creation defaults; insert its live grokbot route with the sealed key; allocate the connector descriptor; create a setup thread containing the bot and operator peer; insert one hub-authored ordinary `message`; fan out one Delivery to the bot; append safe audit and receipt. No external webhook request occurs inside the transaction. After commit publish directory/activity/log notifications and kick the existing worker. Rollback leaves no peer, principal, route, name claim or setup message. A newly created Grok peer is `auth_state:"unlinked"`; do not generate and discard an enrolment token. Human credential issuance follows the existing invite/link paths separately.

Reuse the permissive `meta(key,value)` table for namespaced `onboarding.v1` records and the transaction's serialization for name claims. Records contain request subject/ID, kind/version, a keyed request digest, resource IDs and nonsecret options only. HMAC the canonical normalized request (including secret inputs) with a domain-separated hub-key derivation in memory to compare retries; never persist the raw request, plain key, URL, ciphertext copy or unkeyed key fingerprint in a receipt. Scope request lookup to verified subject plus ID; status requires current operation authority. A protected pending-action completion retains its verified issuer as the request subject and records the human approver separately; arbitrary caller-supplied `as` cannot set either identity. Store IDs/options needed to recover after restart; derive setup delivery state from deliveries and credential readiness from current state. Keep these records separate from send-idempotency's 24-hour GC so retrying an old submit cannot create a second bot. Ordinary peer disable/route removal is the existing recovery path; no new deletion policy is introduced.

An identical retry, concurrent double-submit, lost response or restart returns the same peer/route/name/setup IDs and produces no additional setup Message/Delivery. A changed request under the same ID is 409 `idempotency_conflict`; a previously completed legacy invitation's secret output is not recoverable through status/replay (use existing invite reissue or link UI). No plaintext human output is stored in retry records. One setup **message** does not promise one HTTP attempt/run: existing durable push retry semantics remain at least once, with `delivery_id` dedupe and quota accounting.

Default scope is the current operator-created peer policy (open allow/deny unless existing creation policy says otherwise), not a new hardcoded smoke restriction. Limits come from the same effective preference snapshot as `insertPeer` (factory values 60 sends/minute and 20 threads/hour), with existing route budgets 200 runs/routine/hour and conservative 300 runs/hub/hour. Preserve later operator changes; do not turn the supervised smoke values 6/2 into product defaults.

### 5. Conductor v4, hub sender and credential binding

Use conductor v4 from the platform-facts source as the base, corrected by locked choices: show the user's authorization prompt **before** URL/key entry. It asks the bot to create/reuse `vecnet incoming messages`, link the routine page/deep links for human copying, never paste its webhook URL/key, and wait for submission. `<HUB_URL>` is a nonsecret hub origin; “never paste URL” here means the **routine webhook URL**, not the hub/MCP origin or routine-page links.

The one advisory setup message contains the hub/MCP origin, expected peer ID, connector name, setup thread identity, selected MCP/CLI path and safe next steps. It contains no routine URL, key, bearer, enrol command with a token, link code, code-bearing consent URL or speculative fallback machine path. The setup text points the human to protected linking; a plain public authorization-page address is not a ready-made peer-bound consent link. If OAuth fails, offer existing CLI operation while a protected, version-pinned local MCP fallback is arranged; do not invent a machine, credential file or `mcp-remote` version.

Source evidence supports hub/system sending without a new kind: `internal/hub/approvals.go:HubSender` is `"hub"`; its expiry message uses `SenderInfo{Name:"hub",Kind:"hub"}`, `insertMessage` and `fanOut`; the Message actor schema already accepts it, and `thread-model.js:isSystem` renders it. Use that representation with `kind:"message"`, `ingress:"hub"`, and `priority:"wake_idle"`, not a new peer kind or impersonated operator. Keep hub out of the peer participant list; include the actual operator peer so the bot's in-thread connected reply has a real recipient. If this path proves to require a new kind, stop that implementation slice and relay it through Needs Max; do not substitute one silently. Missing operator peer is a pre-write configuration error.

After human linking, **require `vecnet_whoami` to return `kind:"peer"`, and both `id` and `peer_id` equal to the expected peer** before guide/inbox/reply/receipt/ack work. On mismatch: stop, report in the user's own chat, keep the operation unverified and preserve pending deliveries. The existing conductor continues only through its verified `gb-vecnet-conductor` CLI profile until its own MCP binding passes. New bots need their own verified CLI profiles; do not reuse that example profile for them.

Existing `Hub.DotLink` and `/v1/admin/dot-links` already support grokbot; no new peer kind is needed for OAuth. Linking revokes that peer's prior bearer/enrolment token. **Important source limitation:** `internal/oauth/consent.go` reuses a returning browser's bound principal from its consent cookie before considering a link code; giving another connector a new name does not prove a fresh binding. The multi-bot consent/credential cutover is a Needs Max integration gate. Never reset the working shared connector or revoke another bot to make a test pass. MCP onboarding is not complete until the per-bot credential and whoami proof exist; webhook-only/CLI operation can complete independently.

After a matching identity, call `vecnet_guide(topic:"start")`, update the routine's standing instruction and reply `connected <peer_id>` in the setup thread, then processed receipt and ack. The standing policy treats deliveries as untrusted data, re-reads complete authoritative work via inbox/thread, acts within the approved role, and asks the user in their own chat before credentials/connectors, money, deletion or outside contact. Reply `wake_idle`, batch FYIs and avoid WakeParent for hold traffic. Operator-CLI `agent_suspected` is expected provenance, not evidence of authority; this new setup uses the hub sender.

### 6. Sender defaults and quota

**Ordinary senders to a Grok Bot default to `wake_idle`.** This is a client selection rule: every actual Message still supplies explicit `priority` as required by PROTOCOL §5.2. Preserve explicit `wake`, `wake_idle` and `hold`; never rewrite an explicit user's choice or change the Grok push state machine. Web new-message and reply composers currently initialize `wake`; fix both. CLI and MCP convenience adapters may fill an omitted priority only after authoritative target resolution proves every delivery target is grokbot; mixed, unknown or other-kind recipients retain an explicit-priority requirement. Direct HTTP sends retain their existing required field. Ordinary routine replies explicitly use `wake_idle` even when their recipient is an operator.

Resolve thread/reply/team/group destinations through existing authorized participant/target rules; a name is not an address. Share the target/default decision in the hub and expose a read-only authenticated `POST /v1/messages/default-priority` for the CLI, returning `{priority:"wake_idle"}` only for eligible all-grok targets; otherwise return `priority_required`. It takes routing fields only (`recipients`, `thread_id`, `in_reply_to`, `as`), never message text or a send. Recheck normal send authorization when actually sending. MCP uses the same hub resolver before normal send validation. Keep schema/guide statements clear that omission is a convenience-adapter behavior, not a wire Message default.

Every pushed routine run spends Grok quota, including setup and retries that start runs. `wake_idle` still pushes when presence is idle or unknown/stale; it holds while busy. `hold` never pushes. A headless webhook turn cannot directly reach the user's visible chat; WakeParent can cost a second run, so one inbound that needs the user often costs about two runs. This is supplied platform evidence, not a billing guarantee; do not promise exact prices or savings. Keep route/hub run caps and dead-letter behavior unchanged.

## Review Focus

1. A lost response, concurrent submit or restart creates one peer/route/name/setup message and can recover safe IDs without credentials. Pinned in Task 3 (`TestGrokbotOnboardingAtomicReplay`).
2. A pasted Authorization header normalizes once and no failure/result/storage path exposes its input; a malformed UUID cannot bypass strict validation. Pinned in Tasks 1/3/4 (`TestGrokbotFieldsAndRedaction`, `TestGrokbotOnboardingSecretBoundary`, `TestAgentOnboardHTTPRedaction`).
3. A connector named for bot B still returning grok-smoke/bot A cannot produce connected/receipt/ack for B; skip-MCP never attempts MCP. Pinned in Tasks 1/6/7 (`TestGrokbotPromptIdentityStop`, `TestAgentOnboardMCPHumanBoundary`, `grok onboarding identity and skip policy`). Live multi-bot consent isolation remains gated, not proven by these tests.
4. A second kind/caller cannot bypass grants or replace normal creation defaults; all five unchanged kinds retain invite/link results. Pinned in Tasks 2/4 (`TestOnboardLegacyKinds`, `TestAgentOnboardAuthorization`).
5. New-message and thread/reply senders use wake_idle for Grok recipients, retain explicit overrides, and do not default unknown/mixed targets. Pinned in Tasks 8/9 (`TestGrokbotSendDefaultResolution`, `Grok recipient defaults preserve user choice`).

## Dependency map and file ownership

```json
{"1":[],"2":[1],"3":[1,2],"4":[2,3],"5":[4],"6":[4],"7":[4],"8":[4,5,6],"9":[7,8],"10":[5,6,7,8,9]}
```

Tasks 4/6/7 also consume admin-parity Tasks 1/2/4/7 before enabling the protected MCP submission/human-confirmation journey. Do not manufacture that dependency during this build or reserve its migration. Task 1 owns the registry/templates; Task 2 hub generic/legacy service and safe receipts; Task 3 Grok transaction/setup; Task 4 HTTP; Task 5 CLI; Task 6 MCP; Task 7 Add agent UI (**Opus**); Task 8 shared sender resolution and CLI/MCP conveniences; Task 9 composer UI (**Opus**); Task 10 protocol/guides/final acceptance.

Serialized overlaps: `internal/hub/onboarding.go` is Task 2 then Task 3 action wiring; `internal/httpapi/admin.go` Task 4 only; `internal/cli/commands.go` Task 5 then Task 8; `internal/mcpserver/tools.go` Task 6 then Task 8; `internal/httpapi/server.go` Task 8 only. Task 7 exclusively owns the invite UI/API adapter; Task 9 owns compose/thread UI. Task 10 exclusively owns `internal/mcpserver/guide.go` and `guide_test.go`. Companion admin-parity owns its manifest/pending-action implementation; agree the concrete adapter file before that merge, and record any added shared path here before editing. All paths in Files below are full repo-relative ownership, including new shared files; no task gets a directory-wide edit allowance.

---

### Task 1: Kind registry, field validation and hub-owned prompt templates

**Files:** Create `internal/onboarding/types.go`, `internal/onboarding/registry.go`, `internal/onboarding/registry_test.go`, `internal/onboarding/prompts.go`, `internal/onboarding/prompts_test.go`, `internal/onboarding/templates/grokbot-authorize.txt`, `internal/onboarding/templates/grokbot-setup.txt`. Read existing `internal/cli/invite.go`, `internal/cli/testdata/invite.json`, `internal/ui/static/invite-model.js` for exact legacy text; do not change those consumers yet.

**Interfaces:** Produces the four types and exact JSON contracts in Decisions §1; `Specs() []KindSpec`, `Lookup(kind string) (KindSpec, bool)`, `Validate(SubmitRequest) error` and `RenderPrompt(kind, phase string, safeValues map[string]string) (string, error)`. Definitions are stdlib-only, immutable to callers, with an explicit placeholder allowlist. Interfaces cite PROTOCOL §§2.12, 4.5–4.6, 5.2, 12 and 13.3.

- [ ] Write `TestOnboardingRegistryKinds` and `TestLegacyPromptCompatibility`: six ordered kinds, no infrastructure kinds, complete field/action metadata, existing TTL/open-name/dot contract and exact invitation golden text. Reject stale versions/unknown fields without mutation.
- [ ] Write `TestGrokbotFieldsAndRedaction`: strict UUID groups/host, whitespace/raw/header key equivalence, nested header/control/oversize rejection, no secret in errors, connector slug/collision inputs and skip default. Use generated fake key bytes; assertion failures must not dump them.
- [ ] Write `TestGrokbotPromptIdentityStop`: v4 webhook-first authorization, routine-page links, no routine URL/key/code placeholders, named per-bot connector, hard mismatch stop before any work, few-gateway recommendation and separate skip-MCP rendering. Pin advisory authority, receipt/ack distinction and quota text.
- [ ] Run failing then passing focused checks: `BB_JOBS=2 just bb go test -count=1 ./internal/onboarding`. Copy existing legacy render behavior into the registry without a copy rewrite; implement Grok templates/validation. The source draft conflicts remain explicitly deferred below.
- [ ] Review the field/prompt contract against both v4 sources and locked decisions, then commit the registry and templates. This task has no database or transport effect.

---

### Task 2: Caller-aware generic service, legacy adapters and safe retry receipts

**Files:** Create `internal/hub/onboarding.go`, `internal/hub/onboarding_receipts.go`, `internal/hub/onboarding_test.go`, `internal/hub/onboarding_receipts_test.go`. Modify `internal/hub/hub.go` and `internal/hub/admin.go` only to extract creation/link helpers needed for transaction reuse; keep public `AddPeerWith` and `DotLink` behavior. No other shared file is included.

**Interfaces:** Produces `Hub.OnboardingKinds`, `Hub.OnboardAgent` and `Hub.OnboardingStatus` from Decisions §4, with `auth.Caller` inputs and safe `onboarding` DTOs. Internal transaction helpers accept `*sql.Tx` and return after-commit notification IDs instead of opening nested transactions. Consumes Task 1 registry and existing `AddPeerWith`, `DotLink`, effective preference snapshot and admin authorization adapter. Cite PROTOCOL §§2.12–2.13, 4.5, 8, 12, 16.2 and 20.1.

- [ ] Add `TestOnboardLegacyKinds` for session/launcher/openclaw/pagchat-bot invitation and dot linking, fixed/open names and TTL bounds. Prove the registry produces the same legacy human result and default limits/scopes; no operator/source Add agent action.
- [ ] Add `TestOnboardingReceiptIsolation`: namespaced meta records, caller-scoped reads, keyed digest conflict, persistence over reopen, no ordinary send-GC eviction and no raw request/URL/key/one-time output in records. Replay never returns an old invite token/link code; direct legacy endpoints remain compatible.
- [ ] Implement generic dispatch for legacy actions and safe status. Create the legacy peer, any requested link output and its safe receipt in one transaction too; a failure cannot leave an unreceipted peer that a retry duplicates. Return first-submit human output after commit and never persist it. Stage the Grok action as unavailable until Task 3 registers its implementation; listing can describe it, but submission must fail closed without side effects during this intermediate commit.
- [ ] Recheck operator/companion grant authority inside mutations and on status reads; use real caller audit provenance with safe IDs/counts. Extract only needed transaction helpers; no schema change.
- [ ] Run red/green `BB_JOBS=2 just bb go test -count=1 ./internal/hub -run 'TestOnboard|TestOnboarding'`, then the full relevant hub suite on the box. Review compatibility and commit.

---

### Task 3: Grok submit transaction, connector descriptor and hub setup delivery

**Files:** Create `internal/hub/onboarding_grokbot.go`, `internal/hub/onboarding_grokbot_test.go`. Modify `internal/hub/onboarding.go` (register Grok action after Task 2), `internal/hub/grokbot.go` (share route validation/sealing helpers with the transaction path while preserving low-level behavior). Read `internal/hub/routes.go`, `internal/hub/approvals.go`, `internal/hub/threads.go`, `internal/hub/send.go`; their existing private insertion/fanout helpers should suffice and are not edit allowances.

**Interfaces:** Completes `grokbot_setup` under `Hub.OnboardAgent`; consumes `insertPrincipal`, `Hub.insertPeer`, `Hub.insertRoute`, `Hub.Seal`, `insertThread`, `addParticipants`, `Hub.insertMessage`, `Hub.fanOut` and `HubSender`. Produces one safe result, with unlinked auth state and ordinary durable setup delivery. Cite PROTOCOL §§2.2, 3.3, 5.1–5.3, 12, 13.3, 14.3 and 16.3. If an additional shared edit is necessary, name it in this plan before proceeding.

- [ ] Add `TestGrokbotOnboardingAtomicReplay`: inject failure at each write boundary; count zero remnants on rollback; concurrent duplicate, response-loss retry and DB reopen return identical IDs and exactly one setup Message/Delivery. A changed key/URL/options under the same ID conflicts without echoing input.
- [ ] Add `TestGrokbotOnboardingSecretBoundary`: stored route key is sealed, new peer has effective creation defaults, descriptor belongs only to that peer, no token is minted/discarded, audit/result/message/receipt exclude the key and routine URL; collision with another peer fails atomically.
- [ ] Add `TestGrokbotSetupHubDelivery`: sender/info/ingress are hub, message kind is message, operator and bot are thread participants but hub is not; bot alone receives setup. Validate envelope, reply-to-setup/history and receipt/ack paths for a matching bot identity. Missing operator peer fails before writes. Skip-MCP still has peer/route/setup and no MCP instructions/credentials.
- [ ] Implement one transaction and after-commit notifications. Queue through the existing worker; never call the routine synchronously or claim that 200 run-start means processed/connected. Preserve strict onboarding URL validation alongside legacy route semantics and existing run budgets.
- [ ] Run focused red/green and full hub checks with `BB_JOBS=2 just bb go test -count=1 ./internal/hub`, then `BB_JOBS=2 just bb CGO_ENABLED=1 CC=/usr/bin/gcc go test -race -count=1 -timeout=45m ./internal/hub`. Review lifecycle/replay evidence and commit. No real routine POST is part of these tests.

---

### Task 4: Admin kinds, submit and status HTTP adapters

**Files:** Create `internal/httpapi/onboarding.go`, `internal/httpapi/onboarding_test.go`. Modify `internal/httpapi/admin.go` (register the three paths). Register operations through the companion manifest's supported adapter without editing unlisted companion/shared files.

**Interfaces:** Implements Decisions §2 paths, status/error/cache semantics and human-only result separation. Calls the same Task 2/3 services, then the existing `Kick` after a new committed setup. Consumes the parity plan's authorization/pending-action extension point; the protected human confirmation executes the same submit service with secret fields supplied by the human. Cite PROTOCOL §§2.12–2.13, 4.5, 12, 14 and 20.1.

- [ ] Add `TestAgentOnboardAuthorization`: unauthenticated, ordinary peer/principal, operator and delegated boundary/credential cases. Listing never grants write authority; reject stale contract, unsupported kind/action and unknown inputs before side effects. Exercise direct human submit and pending-action completion without bypassing either identity's grants.
- [ ] Add `TestAgentOnboardHTTPRedaction`: fresh/replayed/status responses, field errors, route failure and cancellation/expired pending action; no routine URL, key, ciphertext or legacy human output in status/MCP-safe serialization. Prove 201/200/409 semantics and no-store headers.
- [ ] Implement thin handlers and operation registration. Retain the existing peer/route/link endpoints unchanged; do not have the handler reimplement sequencing or backend field rules. If companion human-request support is absent, leave that integration disabled and report its dependency rather than inventing a secret-bearing MCP shortcut.
- [ ] Run red/green `BB_JOBS=2 just bb go test -count=1 ./internal/httpapi -run 'TestAgentOnboard'`, then full HTTP API tests on the box. Review authorization/secret paths and commit.

---

### Task 5: CLI agent kinds/add/status and legacy prompt adapter

**Files:** Create `internal/cli/agent.go`, `internal/cli/agent_test.go`. Modify `internal/cli/commands.go` (dispatch/help only in this task), `internal/cli/invite.go` (delegate pure legacy rendering/slug to the shared registry). Existing `internal/cli/testdata/invite.json` is a compatibility input, not a golden to rewrite.

**Interfaces:** Exposes the exact CLI forms in Decisions §2; maps supported spec fields to explicit flags including Grok URL/key-file/name/connector-name/skip-MCP, and existing legacy principal/TTL/open-name options. Unknown future field types fail with the selected spec/version rather than being ignored. Uses the HTTP service, no hub/SQLite import; obtains template/spec through kinds and first-submit human output through the human boundary. Cite PROTOCOL §§2.12, 12, 13.7 and 20.1.

- [ ] Add `TestAgentCLIParity`: list/filter/prompt JSON, all six kinds, exact endpoints/payload versions, request ID retained across retry, safe status, stale spec refresh without automatic resubmit and unchanged invitation golden output.
- [ ] Add `TestAgentCLIKeyFileBoundary`: empty/oversized/file-read failures, raw/header normalization, no secret flag/env support, no secret output on HTTP errors, and skip-MCP behavior. Keep low-level `route add/rotate` tests expecting raw keys unchanged; use a separate onboarding file reader or shared bounded-read primitive without broadening that API.
- [ ] Implement the commands and help; human output explains queued versus connected and includes safe resource/request IDs. Retain existing peer invite/link operations and profile selection. No agent add command auto-enrols another bot or overwrites a local profile.
- [ ] Run `BB_JOBS=2 just bb go test -count=1 ./internal/cli`, verify CLI dependencies do not acquire hub/store/SQLite, review secret handling and commit.

---

### Task 6: Operator MCP parity through protected human actions

**Files:** Create `internal/mcpserver/onboarding.go`, `internal/mcpserver/onboarding_test.go`. Modify `internal/mcpserver/tools.go` (tool registration/schema assembly only in this task). No changes to OAuth cookie binding or shared tool auth are included implicitly.

**Interfaces:** Registers `vecnet_agent_kinds`, `vecnet_agent_add`, `vecnet_agent_status` in the current static tool table. Uses the exact registry and HTTP-equivalent services; credential-bearing operations consume admin-parity Tasks 1/2/4/7 pending-action API from Decisions §2. Known unauthorized tools return the companion plan's forbidden/insufficient-scope contracts on every call. Cite PROTOCOL §§4.1, 4.3, 4.5–4.6, 12 and 20.1.

- [ ] Add `TestAgentOnboardMCPHumanBoundary`: tool discovery, schema rejection of webhook URL/key/header/file path, ordinary peer/forged `as` refusal, current authority recheck and safe pending/status output. No peer/route/credential exists before confirmation; human completion yields the same safe IDs as HTTP/CLI, with secrets available only to that human.
- [ ] Test legacy invite/dot link and Grok flows: pending expiry/cancel/duplicate request, disabled caller/revoked grant, changed args under same request ID and no mint-then-strip output. Existing request lifecycle tools provide cancellation; document their names from the merged companion manifest.
- [ ] Keep expected peer identity separate from caller/issuer identity; no tool can assert that a named connector is connected. A grok-smoke result for another expected bot remains unverified; template policy refuses all follow-up work on it.
- [ ] Run red/green and full `BB_JOBS=2 just bb go test -count=1 ./internal/mcpserver`. Check both supported MCP eras, schema error paths and stable discovery. Review and commit only after the human-request dependency is available; otherwise report the dependency, not completed parity.

---

### Task 7: **Opus** — Add agent renders the hub spec and Grok form

**Files:** Modify `internal/ui/static/api.js`, `internal/ui/static/invite.js`, `internal/ui/static/invite-model.js`, `internal/ui/static/invite.css`, `internal/ui/jstest/api.test.mjs`, `internal/ui/jstest/invite.test.mjs`, `scripts/ui/invite.mjs`. All shared UI files are named here. Use the companion protected request-review component instead of adding an independent confirmation surface.

**Interfaces:** Fetches kinds/versions/templates, renders supported field types and dispatches by declared submit action. Displays the authorization prompt before URL/key collection, masked human-only key entry, derived connector name, gateway recommendation and “Skip MCP” checkbox. Uses Task 4 endpoints and companion pending-action review path. Consumes human output only within the dialog; status/results remain safe. Cite PROTOCOL §§2.12, 4.5–4.6, 12, 13.3 and 20.1.

- [ ] Replace the editable `KINDS`/prompt copies with registry-derived state; retain existing selectors/derived exports while migrating tests. Test loading/failure/version-refresh and unsupported field/action behavior, with no fallback hardcoded kind registry. Preserve five legacy kind interactions, invitation copy, TTL/open-name and dot link flow.
- [ ] Add `grok onboarding identity and skip policy` model tests and browser cases: prompt first, required name/URL/key, server field errors, raw/header acceptance, default/override connector, skip-MCP, few-gateway and quota copy. Never display a connected badge based on queued/accepted alone.
- [ ] Prove key clearing on close/kind switch/completion, no storage/URL/clipboard/console persistence, stable request ID on uncertain retry and no double-submit. Abort/ignore stale fetch completions after dialog close; a committed request remains discoverable by status. Use fake keys, exclude filled secret inputs from screenshot/trace capture and redact errors before rendering.
- [ ] Run `BB_JOBS=2 just bb node --test --test-concurrency=2 internal/ui/jstest/` and `BB_JOBS=2 just bb SHOTS_DIR=/mnt/build-cache/vecnet-agent-onboarding/shots node scripts/ui/invite.mjs`. Review loading/form/validation/submitting/queued/pending/skip/legacy states, keyboard focus and narrow layouts. Use masked/unfilled secret fields for screenshots; never capture a real protected submission.
- [ ] Have the UI reviewer inspect the rendered states and behavior, fix findings, then commit. Do not open a GUI on the user's desktop or contact a live routine.

---

### Task 8: Shared Grok recipient default resolution and CLI/MCP conveniences

**Files:** Create `internal/hub/send_defaults.go`, `internal/hub/send_defaults_test.go`, `internal/httpapi/send_defaults.go`, `internal/httpapi/send_defaults_test.go`. Modify `internal/httpapi/server.go` (register the authenticated read-only resolver), `internal/cli/commands.go`, `internal/cli/agent_test.go`, `internal/mcpserver/tools.go`, `internal/mcpserver/onboarding_test.go` after Tasks 5/6. No envelope schema change is planned.

**Interfaces:** Produces caller-aware `Hub.SendDefaultPriority(ctx, caller, routingInputs) (string, error)` and `POST /v1/messages/default-priority` from Decisions §6. Reuses existing authorized sender/target/thread/group expansion rules; returns wake_idle only for a nonempty all-grok target set. CLI consults it when send/reply has zero priority flags; MCP's convenience input permits omission and resolves before normal send/reply validation. Explicit values and actual HTTP Message schema stay unchanged. Cite PROTOCOL §§2.2, 4.3, 5.2, 7, 13.3 and 16.3.

- [ ] Add `TestGrokbotSendDefaultResolution`: direct/thread/reply/group/team expansion, sender exclusion, no empty/unknown/mixed default, private-thread access, scopes, spoofed `as` and changing membership. The resolver writes nothing; a later send still runs all normal checks.
- [ ] Test CLI send/reply with zero/one/multiple priority flags and MCP send/reply with omitted/explicit priority: grok targets become explicit wake_idle on the final send; mixed/other kinds require a choice; explicit wake/hold survive. Raw HTTP missing priority retains its existing schema failure.
- [ ] Implement target resolution with existing hub helpers, not a second addressing/authorization model. Override the MCP convenience input's priority description to explain conditional omission; the shared wire schema still requires priority. Keep default resolution separate from idempotent send-body generation so retries retain the same resolved priority and body. Do not use an all-grok default to force a busy recipient awake.
- [ ] Run focused then full hub/HTTP/CLI/MCP suites with `BB_JOBS=2 just bb go test -count=1 ./internal/hub ./internal/httpapi ./internal/cli ./internal/mcpserver`. Review permission and compatibility cases, then commit.

---

### Task 9: **Opus** — Grok defaults in new-message and reply composers

**Files:** Modify `internal/ui/static/compose.js`, `internal/ui/static/compose-model.js`, `internal/ui/static/thread.js`, `internal/ui/static/thread-model.js`, `internal/ui/jstest/compose.test.mjs`, `internal/ui/jstest/thread.test.mjs`, `scripts/ui/compose.mjs`, `scripts/ui/thread.mjs`. These shared files are disjoint from Task 7; serialize against other composer owners before editing.

**Interfaces:** Applies Decisions §6 to both new-message and existing-thread replies using known effective recipient kinds. Automatic initial state for Grok is wake_idle; manual selection is sticky and explicit. Mixed/unknown targets require deliberate priority selection instead of silently keeping the current wake initialization. Final `api.send` payload always includes priority. Cite PROTOCOL §§2.2, 5.2, 13.3 and 16.3.

- [ ] Add `Grok recipient defaults preserve user choice` cases for selecting/removing recipients, existing setup thread replies, delayed directory data, mixed destinations, explicit wake/hold and send retry. Never change the body/priority of an in-flight or idempotent retry when presence/directory changes.
- [ ] Implement the default with existing kind/participant data and small explanatory quota text. Preserve other-kind composer behavior and manual priority controls; never describe wake_idle as free or as a guarantee of no wake.
- [ ] Run Node model tests and both affected isolated browser scripts through `BB_JOBS=2 just bb`; inspect screenshots with artifacts under `/mnt/build-cache/vecnet-agent-onboarding/shots`. Verify keyboard and narrow layouts, review, fix and commit.

---

### Task 10: Protocol, guides and final cross-surface acceptance

**Files:** Modify `docs/PROTOCOL.md`, `docs/OPERATOR.md`, `docs/connectors/grokbot.md`, `internal/cli/guide.md`, `internal/mcpserver/guide.go`, `internal/mcpserver/guide_test.go`, `CHANGELOG.md`. Create `docs/plans/receipts/2026-11-06-agent-onboarding.md` for sanitized implementation evidence. All shared MCP files are named; merge protocol edits by section with companion owners.

**Interfaces:** Documents actual registered kinds/fields/actions, three new admin operations, CLI/MCP parity and pending-human lifecycle, sender convenience resolver and wire-required priority. Update PROTOCOL §§2.12, 4.3, 4.5–4.6, 5.2, 12 and 13.3; cross-link §§3, 14 and 20.1. Runtime Grok instructions in `guide.go` agree with the registry templates; no second onboarding prompt is maintained in docs or the CLI guide.

- [ ] Replace the current “Prove MCP first” normal onboarding sequence with v4 webhook-first; retain the old bounded supervised smoke as an explicitly optional diagnostic after identity is proven. Include all-gateway/per-bot identity warnings, skip-MCP/CLI path, human key/link entry, OAuth revocation and returning-cookie caveat, quota cost, run-start versus processed, receipt versus ack and no secret-bearing message bodies.
- [ ] Add structural guide tests for hard whoami mismatch stop and skip-MCP distinction; document that external consent/credential isolation awaits the Needs Max gate. Append one dated changelog line for the new agent-facing contracts.
- [ ] On the build box run full Go and Node suites, `just validate-protocol`, and affected isolated browser scripts; use `BB_JOBS=2 just bb` for every command and explicit SHOTS_DIR. Independently review every named shared-file overlap, all six kinds, operation manifest coverage, no CLI SQLite dependency, secret serialization and the five Review Focus pins. Record exact integration HEAD and commands/outcomes in the receipt; source/fake tests do not count as live platform proof.
- [ ] Describe and, only under separate implementation-time authorization, run **one bounded live Grok connection journey** on a disposable gateway peer/routine: human pastes v4, receives routine-page links, submits URL/key securely through one selected surface, reads back the same peer/route/name through the other surfaces, observes one setup Message/Delivery, and checks the bot's expected identity before a connected in-thread reply, processed receipt and ack. Use fake-server automated tests for transport-parity permutations to avoid spending real quota three times.
- [ ] In that journey, if MCP is chosen, require a per-bot human credential handoff and matching whoami first; exercise a second named connector only when consent isolation has been resolved without disturbing grok-smoke. If skip-MCP is chosen, prove the same IDs through the bot's own verified CLI profile and never attempt mismatched MCP. Record push attempts/runs and safe IDs/status only, no URL/key/code/transcript captures. Confirm explicit hold causes no POST and held wake_idle behavior via automated tests; do not spend extra live runs merely to duplicate those tests.
- [ ] Report live PASS only for independently observed reply/receipt/ack at the expected identity. A webhook 200 alone is acceptance, and missing authorization/credentials means NOT RUN, not PASS. Disable/remove only the disposable route using existing operator actions after the authorized journey; retain its safe receipt. Review final docs and commit; deployment and production cutover remain separately authorized.

---

## Needs Max

CoS relays these questions; none reopens the six locked owner decisions. They are implementation/cutover gates, not a reason to leave this plan uncommitted.

- **Per-bot credential handoff and returning-cookie isolation:** approve the intended user flow for a second gateway when the shared MCP currently binds grok-smoke. Existing OAuth can bind grokbot, but a new connector name can reuse the old consent cookie. CoS needs the per-bot credential/consent path and any account-isolation policy; do not silently log out/rebind/revoke a working bot. Until resolved and whoami matches, use verified per-bot CLI only and mark MCP connection acceptance pending.
- **Name versus owner display:** the two v4 drafts disagree on display-name meaning and limit (64 versus 128 characters), and suggest mutating owner display when unset. Confirm whether the bot-known user name is a separate field and whether any owner update is desired. Baseline retains existing peer `name` semantics and does not rename the owner; changing that is a separate product decision.
- **Connector naming policy beyond the locked pattern:** confirm whether the default bot slug should continue using the existing name slug or gain an explicit stable identity suffix, and whether humans may customize it across hubs sharing one Cursor account. Baseline derives it from name, permits the draft's optional override, refuses local cross-peer collisions and never reuses another peer's auth. The required per-bot `vecnet-<bot-slug>` pattern stays locked.
- **Conditional only — hub sender:** source supports `sender:"hub"` without a new peer kind. If implementation reveals a concrete protocol/lifecycle obstacle requiring a kind, CoS must bring that evidence to Max before adding one. No unresolved sender choice is assumed today.
- **Live acceptance authority:** supply/authorize a disposable routine, human key entry and the chosen per-bot CLI/OAuth credentials for Task 10. This planning job has no live-test, install, deployment or credential-rotation authority. Admin-parity availability is an engineering dependency for protected MCP submission, not permission to expose secrets while waiting.

## Self-review notes

- Locked decisions map to registry/legacy compatibility (Tasks 1/2), peer/route/name/setup transaction (Task 3), HTTP/CLI/MCP parity and protected human handoff (Tasks 4–7), wake_idle and quota (Tasks 8–10), and explicit Needs Max gates. Both mandatory identity/gateway flags appear in constraints, prompts, tests and acceptance.
- New contracts are proposed interfaces, distinct from currently observed APIs. Existing Grok sealing, OAuth-link support and hub actor support are reused; supplied platform observations are not claimed as planning-time live verification.
- Shared-file ownership and sequential overlaps are explicit. No migration is required by the baseline. Admin-parity owns pending actions and grants; this plan neither reserves schema numbers nor implements that subsystem again.
- Automated tests can prove contract/secret/transaction/default behavior. A correct per-bot external credential and independently observed live reply/receipt/ack are separate acceptance evidence; outstanding product answers do not become invented defaults.

--- SUMMARY ---

- The hub registry defines all six Add agent kinds; five legacy kinds preserve behavior, and Grok is the first new composite onboarding action. CLI, MCP and web consume one server contract.
- Grok submit atomically creates the default-policy peer, sealed route, per-bot connector descriptor and one hub-authored setup message. Durable safe receipts make retries/restart recoverable without a new migration or secret replay.
- Conductor v4 is webhook-first and links the routine page for human copying. MCP is recommended but optional; every bot must prove its own identity. The existing grok-smoke connector is unsafe for other bots until proper per-bot binding, so their verified CLI profiles remain the operating path.
- Senders select wake_idle for Grok by default while wire messages retain explicit priority; quota cost, busy/idle behavior and WakeParent's possible second run are documented. Few gateway bots each get their own peer and connector.
- Ten scoped tasks include explicit shared files, Opus UI ownership, build-box-only test gates and one separately authorized live journey. Needs Max isolates credential/consent, display-name and naming-policy questions; this assignment delivers only the committed plan and external result receipt.
