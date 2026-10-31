# Grok Bot connector facts

Type: research
Status: open
Blocked by:

## Question

What exactly does a Grok Bot expose and accept? Outbound webhook payload shape and signing, how a message gets INTO a bot (SendToAgent target/priority semantics, inbound routine webhook with bearer auth as w2m-receiver uses, or tool only), auth, rate/quota limits, thread/conversation ids we can map to `original_thread_id`. Note webhook2mcp's "grokbot-contract" is bridge-to-receiver, not Grok Bot's own outbound shape; find the latter. Deliverable: `docs/research/grokbot.md` with sources, and a one-paragraph "what the grokbot connector must do".
