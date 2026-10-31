# OpenAI dots MCP event subscription facts

Type: research
Status: resolved
Blocked by:

## Question

How does an MCP server push events to an OpenAI dot? Exact mechanism (resources/subscribe + notifications? a vendor extension? Streamable HTTP requirements?), auth model, public-URL constraints, payload limits, and how a dot sends a message back (tool call on our MCP server?). Deliverable: `docs/research/dots.md` with sources plus "what the dots connector must do".

## Answer

A dot is reached only through OpenAI's MCP Events profile in webhook mode. Core `resources/subscribe` is not the mechanism; spec 2026-07-28 replaced it with `subscriptions/listen`. The dot's user asks ChatGPT to subscribe. ChatGPT then calls `events/subscribe {name, arguments, delivery:{mode:"webhook", url, secret: whsec_}}` on our MCP server. We verify the callback with a signed challenge echo and push one Standard-Webhooks-signed event per POST, `{eventId, name, timestamp, data, cursor}`, capped at 256 KiB. ChatGPT re-subscribes before `refreshBefore` with its last cursor.

Auth is OAuth 2.1 with PKCE S256 as the ChatGPT user, with no API keys or client credentials. The hub sees a user principal, never "dot X". Our `/mcp` must be on public HTTPS or behind OpenAI's outbound-only Secure MCP Tunnel; event push needs only outbound HTTPS, DNS-pinned with no redirects.

A dot replies by calling write tools on our server. These require confirmation by default, and whether that blocks a dot is unverified. The profile is ChatGPT's version of draft SEP-3415: top-level `events` capability and error code `-32015`. webhook2mcp already implements it live.

Full facts, connector checklist and open questions: [docs/research/dots.md](../../../docs/research/dots.md)
