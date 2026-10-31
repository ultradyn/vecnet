# Grok Bot connector facts

Type: research
Status: resolved
Blocked by:

## Question

What exactly does a Grok Bot expose and accept? Outbound webhook payload shape and signing, how a message gets INTO a bot (SendToAgent target/priority semantics, inbound routine webhook with bearer auth as w2m-receiver uses, or tool only), auth, rate/quota limits, thread/conversation ids we can map to `original_thread_id`. Note webhook2mcp's "grokbot-contract" is bridge-to-receiver, not Grok Bot's own outbound shape; find the latter. Deliverable: `docs/research/grokbot.md` with sources, and a one-paragraph "what the grokbot connector must do".

## Answer

Full facts and sources: [docs/research/grokbot.md](../../../docs/research/grokbot.md).

- **Ingress:** The only external way in is a routine webhook trigger. POST optional JSON to `https://api2.cursor.sh/automations/webhook/<routine-uuid>` with `Authorization: Bearer <crsr_ key>`. The key is static and per-routine, with no HMAC. The routine is created by asking the Bot in chat. `SendToAgent` (`target_id`, `message`, required `priority`) only works between one user's own Bots and is not callable from outside.
- **Responses:** 200 `{"success":true,"runUuid"}` means a run started, not that it finished. 401 means a bad key. 400 means the routine is paused or rate-limited. Webhook runs are never retried server-side.
- **Limits:** 300 runs per hour per account and 200 per routine. A platform-wide cap also applies. Every run spends the weekly usage pool.
- **Outbound:** There is no native outbound webhook. The Bot replies only through tools: Shell/curl, an MCP connector such as webhook2mcp, Slack or email.
- **Thread ids:** None are exposed. The sender gets only a `runUuid`. `original_thread_id` must ride in the body and be echoed back by the Bot.
- **Connector:** Push uses a bearer-auth POST from a durable outbox with sender-side dedupe. Pull needs a vecnet MCP tool or signed endpoint that the Bot calls.
