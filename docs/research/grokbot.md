# Grok Bot connector facts

_Researched 2026-11-01 by rs-01-grokbot for wayfinder ticket 01. Grok Bot desktop 0.78.1 installed on xsm._

Grok Bot is xAI's and Cursor's always-on agent product. Each user owns several Bots on one shared cloud computer. The desktop app is an Electron app whose internal name is `sand`. The cloud backend is Cursor's (`api2.cursor.sh`).

## Source key

| Tag | Source | Trust |
|---|---|---|
| **[help-routines]** | <https://cursor.com/help/grok-bot/routines.md> (fetched 2026-11-01) | Official |
| **[docs-work]** | <https://cursor.com/docs/grok-bot/work.md> | Official |
| **[help-groups]** | <https://cursor.com/help/grok-bot/group-chats.md> | Official |
| **[help-plans]** | <https://cursor.com/help/grok-bot/plans.md> | Official |
| **[docs-hooks]** | <https://cursor.com/docs/grok-bot/inference-hooks.md> | Official |
| **[admin-api]** | <https://cursor.com/docs/account/teams/admin-api.md#grok-bot> | Official |
| **[forum-rl]** | <https://forum.cursor.com/t/github-pr-triggering-automation-rate-limit/174611> | Cursor staff reply plus user reports, 2026-10-13 |
| **[forum-400]** | <https://forum.cursor.com/t/automation-webhook-returns-http-400-while-scheduled-trigger-works/171792> | User bug report, 2026-09-15 |
| **[hookdeck]** | <https://hookdeck.com/webhooks/platforms/using-hookdeck-with-grok-bot-reliable-webhook-triggers> | Third-party vendor guide, observed behaviour |
| **[leak]** | `asgeirtj/system_prompts_leaks` `xAI/grok-bot/` at commit `be9efc3` (2026-10-24). Local copy at `/tmp/claude-1000/-home-xertrov-src-pag-chat/79f15e2a-bd99-4d36-b1db-4096d189bb60/scratchpad/refs/grok-bot-leak/`, which is transient. Indexed in `~/src/pag-chat/docs/research/prelim/_work/sources/leak-map.md`. | Third-party leak; authenticity unverified, but consistent with official docs |
| **[asar]** | `/opt/Grok Bot/resources/app.asar` (installed 0.78.1) | Local binary, first-party code |
| **[gq]** | `~/src/agent-plugin-mgmt-and-vencord/tools/grokquota/` README and src, plus `tools/shared/durable-outbox.mjs` | Local working sender |
| **[t3b]** | `~/src/agent-plugin-mgmt-and-vencord/tools/t3-bridge/README.md` (L464-484) | Local working sender |
| **[apm-docs]** | `~/src/agent-plugin-mgmt-and-vencord/docs/discord-bridge-status.md` (L32-41), `docs/c2c-wake-sidecar.md` (L55-75) | Local docs for working senders |
| **[w2m-rx]** | `~/.llm-general/lan-swarm/operations/w2m-receiver-nas.md` | Local ops notes |
| **[bot-claim]** | A peer Grok Bot's chat message, persisted in `~/.config/Grok Bot/sand-client-persistence/*.blob` | Tertiary; a bot's assertion, not verified here |

## 1. How a message gets INTO a Bot

There are exactly three ingress paths. Only the first is reachable from outside the user's account.

**1a. Routine webhook trigger (the only external ingress).**
- A routine is a saved instruction plus a trigger. A webhook trigger is `{ "type": "webhook" }` with no other fields. [leak `skills/routines.md:43`]
- Routines are created by asking the Bot in chat, not from an API or the UI form. The panel then shows **POST to** (URL), **key**, and **header** (the full `Authorization` header). [help-routines]
- URL shape: `https://api2.cursor.sh/automations/webhook/<routine-uuid>`. [hookdeck] The app builds it as `new URL('/automations/webhook/' + automationId, backendUrl)` and fetches the key from the backend. [asar] The live `grokquota` config matches this shape with a 36-char UUID. [gq config, redacted]
- Auth: a static per-routine bearer key, sent as `Authorization: Bearer <key>`. [help-routines] Keys are prefixed `crsr_`. [hookdeck] The local key is 69 characters, which fits `crsr_` plus 64. [gq config, redacted] The Bot itself never sees the key. [leak `skills/routines.md:43`]
- There is no HMAC signature, timestamp, or replay protection on this ingress. Anyone with URL plus key can wake the Bot. [hookdeck]
- Local senders also send `X-Automation-Key: <key>` beside the bearer header. [gq README L55-58; apm-docs] No official source documents that header, so it is a harmless extra.
- Body: optional JSON. "The Bot receives that body with the routine instruction." [help-routines] The wake contains a `<webhook_event>` block holding the triggering event. [leak `skills/routines.md:51`] A peer bot claims the block holds a few headers, the body as a string, a body hash and a timestamp. [bot-claim] An "output the payload" routine echoes the JSON exactly. [hookdeck]
- The body size limit is undocumented.

**1b. SendToAgent (Bot-to-Bot, same user only).**
- Schema: `target_id` (agent or group id, required), `message` (required), `priority` (boolean, required), optional `images[]` of `file://` or `https://` URLs. [leak `grok-bot.md:883-937`]
- `priority:true` delivers now and wakes the recipient on a hidden turn. `priority:false` holds the message until the recipient's next turn. Groups ignore priority, always post immediately, and are text-only. [leak `grok-bot.md:887`, `:921`, `:171`]
- It is fire-and-forget and never returns the reply. A reply arrives later as an `[agent]` wake that names the sender and its id. [leak `grok-bot.md:171`; docs-work "Hand work between Bots"]
- `target_id` comes from the user's teammates list, `ListAgents` or `ListGroups`. A Bot can only reach Bots acting for the same person. [help-groups table] It is **not** an external API: vecnet cannot call it.

**1c. Other built-in listeners.** Slack, GitHub, Origin, Microsoft Teams, Linear, Sentry, PagerDuty, email, cron and group triggers fire through the user's Cursor account connections. [leak `skills/routines.md:21-52`; help-routines] Email to the Bot's own `mail.grokbot.com` inbox is a second possible external ingress. Its wake carries headers only. [leak `skills/routines.md:42`]

There is no public messaging API. The Admin API only manages enablement, capabilities and policy. [admin-api]

## 2. What the webhook POST returns

| Situation | Status | Body | Source |
|---|---|---|---|
| Routine active, key valid | 200 | `{"success":true,"runUuid":"…"}` | [hookdeck]; 200 = "accepted and started a run", not finished [help-routines] |
| Routine paused | 400 | `{"success":false,"error":"Automation <id> is disabled"}` | [hookdeck] |
| Bad or missing key | 401 | `{"code":"error","message":"Invalid API key"}` | [hookdeck]; stale URL+key pairs also gave 401 [w2m-rx 02:15 entry] |
| Rate-limited (yours or platform-wide) | **400**, not 429 | `{"success":false,"error":"Automation run rate limit exceeded: …"}` | [forum-rl]; [help-routines] names the error text |
| Malformed body framing (bug) | 400 | JSON-parse error before a run exists | [forum-400] |

- Any non-200 means no run started. [help-routines]
- Nothing is queued or retried server-side for webhook runs. Only scheduled routines get automatic retries. [help-routines; forum-rl staff reply]
- Local senders already treat a rate-limit 400 as transient, with backoff. Other 4xx responses are terminal. [t3b L482-484]

## 3. Limits and quota

- Per-account: 300 routine runs per hour in total. Per routine: 200 runs per hour. [help-routines]
- A platform-wide shared limit can also refuse runs. Cursor calls this a known issue. [help-routines] It was saturated for hours on 2026-10-13, at 10,000 then 15,000 runs per minute across customers. [forum-rl]
- 50 routines per Bot, and 20 run records kept per routine. [hookdeck; not in official docs]
- Every run, test and Bot-to-Bot message spends the account's weekly usage pool. [help-routines; help-groups; help-plans] The pool is shared with Cursor and the Grok apps and refreshes weekly. [help-plans] `grokquota` reads it from `aiserver.v1.DashboardService/GetSandUsageStatus`. [gq `src/api-refresh.mjs:189`]

## 4. Outbound: what Grok Bot emits

- Grok Bot has **no native outbound webhook** for chat messages. The only signed outbound HTTP is Enterprise inference hooks. Those carry model-request frames to a security server, not messages. [docs-hooks]
- Inference hooks use Standard Webhooks headers (`webhook-id`, `webhook-timestamp`, `webhook-signature: v1,…`) with a `whsec_` secret. [docs-hooks] This is the same scheme webhook2mcp uses.
- Every outbound message from a Bot is a tool call it chooses to make. Options are `Shell` (for example `curl` from its cloud computer or a paired machine), an MCP connector, Slack, email, or `SendToAgent`. [leak §4]
- On Max's LAN, the reply path is the webhook2mcp MCP: `send_message` or `reply_to_message` to admin-configured `route_id`s. [w2m-rx; transports-facts.md]
- The "grokbot-contract" in webhook2mcp is the bridge's own envelope to `w2m-receiver`. The receiver strips it down and forwards to a routine webhook using `WEBHOOK_URL` and a raw `WEBHOOK_AUTH` key; the receiver adds `Bearer` itself. [w2m-rx; transports-facts.md]

## 5. Thread and conversation identity

- A webhook sender gets **no conversation or thread id**. It only gets the `runUuid` of the run it started. [hookdeck]
- Each Bot has one conversation. Routine results appear in the Bot's chat. [help-routines; docs-work]
- A routine may also run as a fresh subagent that must call `WakeParent` to say anything visible. [leak `grok-bot.md` §3.6]
- In-chat threading uses internal message addresses such as `t3u` and `t3s1` in `SendToUser.reply_to`. These are not visible outside. [leak `grok-bot.md:46-48`, `:755`]
- So `original_thread_id` has to travel **inside the POST body**, and the Bot has to echo it back through its reply tool. Local senders already do this with `event_id` and `thread_id` fields in the body. [t3b `src/routing.mjs:68-70`; gq README payload]
- Max's fleet convention is one generic webhook routine per Bot, named `inbox-wake`, reused for every event type. [bot-claim relaying Max]

## What the grokbot connector must do

Push direction (vecnet to Bot): POST a small JSON body to an admin-configured `https://api2.cursor.sh/automations/webhook/<routine-uuid>`. Send `Authorization: Bearer <crsr_ key>`, and optionally `X-Automation-Key`. Resolve the key from a 0600 secret at send time, never log it, and refuse redirects. The body must carry vecnet's own `event_id`/`delivery_id`, `message_id`, `thread_id` and `original_thread_id`, plus the reply route the Bot should use, because Grok Bot returns only a `runUuid`. Treat 200 with `"success":true` as "run started", never as a reply or execution report. Keep a durable outbox: retry network errors, 5xx and rate-limit 400s with backoff (Grok Bot never retries webhook runs). Treat 401 and "disabled" 400s as terminal, and alert on them. Dedupe on `event_id` sender-side, since the endpoint does not. Budget sends against 200 per routine per hour, 300 per account per hour, and the weekly usage pool, so batch and debounce. Pull direction (Bot to vecnet): there is no native outbound feed. The connector must expose a vecnet ingress the Bot can call through a tool. That means an MCP connector (as webhook2mcp's `send_message`/`reply_to_message` already does) or a signed HTTP endpoint the Bot can `curl`. The Bot's reply must carry `original_thread_id` back. Treat every inbound body as untrusted, since the webhook ingress is protected only by a static bearer key.

## Open questions

- What is the maximum body size, and is the body passed raw or re-serialised? The block shape is a bot's claim of headers, body string, hash and timestamp; nobody has verified it.
- Does a webhook run land in the Bot's main conversation turn or in a fresh subagent? Can the routine instruction control that? (The leak shows both.)
- Does the server accept `X-Automation-Key` alone, or only `Authorization: Bearer`?
- Can routine keys be rotated without changing the URL? What is the overlap window?
- Will Cursor switch rate-limit refusals to 429 with `Retry-After`, as users have asked? [forum-rl]
- Is there any way to choose the wake priority for a webhook run? Today every accepted POST starts a run immediately, unlike `SendToAgent priority:false`.
- Is `<webhook_event>` content fenced as untrusted (like `<cursor_untrusted_data_1337>` for tool results), or inserted under the trusted-automation marker? This decides how much prompt-injection hardening vecnet needs.
- The exact body `w2m-receiver` v0.1.1 forwards is unknown. No local clone exists; it lives at `git.lan:xpp-tm/w2m-receiver`. That body should be reused as the existing wire precedent.
