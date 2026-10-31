# Inbound webhook sources and routing rules

Type: grilling
Status: resolved
Blocked by: 08

## Question

Per-source inbound URLs (GitHub first with signature verification): how a source is registered, how its payload becomes a message (summary text + raw JSON attachment), routing rules (static target peer/thread, or rule expressions), and whether sources are peers in the directory.

## Answer

Max approved by voice, 2026-11-01 ("print an ingress URL, give us a secret, shove it into GitHub and we're good to go").

- **Registration:** `vecnet source add --kind github --name <name> --route <peer|team|thread id>` prints the ingress URL `https://<hub>/v1/ingress/src_…` and a verification secret. GitHub is verified with its `X-Hub-Signature-256` HMAC; other kinds use Standard Webhooks or a shared header token.
- **Payload → message:** `sender` = the `src_…` id; `content.text` = a per-kind summary ≤ 4 KiB (GitHub: repo, event, title, actor, URL); the raw JSON is a hosted `application/json` attachment; the external conversation key (GitHub issue/PR number) becomes `original_thread_id`, so each issue threads separately; `metadata["vecnet.source_kind"]` and `["vecnet.event"]` are set by the hub.
- **Routing:** one static target per source (peer, team or thread) plus an optional event-type allowlist. Rule expressions are out of scope for v1.
- **Sources are not peers.** They appear in the directory as kind `source`, cannot receive messages, and the hub never posts back to GitHub; a bot that wants to comment does so with its own tools.
