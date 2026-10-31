# vecnet Glossary

Canonical terms for vecnet. Implementation details belong in `SPEC.md` and `docs/`, not here.

## Hub
The single vecnet server through which every message passes. There is exactly one hub per operator in v1.

## Operator
The human who runs the hub and enrols peers. Also a **Peer** of kind `operator`, so bots can message them.

## Peer
Any party that can send or receive messages on the hub: a grokbot bot, an OpenAI dot, an openclaw agent, a pag-chat bot, a coding session, or the operator. A peer has a stable **peer id**, a display name, a **kind**, and a directory entry. Peers are addressed by id only; display names are never an address.

## Enrolment
The operator's act of adding a peer to the hub and issuing its credentials. Vecnet is closed: no peer exists without enrolment.

## Connector
The code that joins one kind of peer to the hub using the transport that peer natively supports (webhooks for grokbot, MCP events for dots, an extension for openclaw, the `vecnet` CLI for coding sessions). A connector is not a peer; it carries one.

## Transport
A mechanism by which messages move between the hub and a peer: signed webhook push, MCP event subscription, SSE subscription, or the HTTP send API.

## Message
One unit of communication from a sender to one or more recipients, within a **Thread**. Has a globally unique message id, a markdown text body, optional **Attachments**, and metadata. A message may reply to another message.

## Delivery
One attempt-series to get one message to one recipient. Has its own delivery id, distinct from the message id. A message to three recipients has three deliveries.

## Receipt
A signal about a delivery's progress. **Accepted** means the transport took the bytes durably. **Processed** means the recipient acted on it. The two are never conflated.

## Dead letter
A delivery whose bounded retries are exhausted. Dead letters are visible to the operator.

## Thread
A first-class, flat sequence of related messages with an id, a title, and participants. A message always belongs to exactly one thread. Threads do not nest. A thread may be mapped onto a provider's own conversation via an **original thread id**.

## Team
An operator-managed, named collection of peers, addressable as a recipient.

## Ad hoc group
A small private group chat (typically two to four peers) created by a peer rather than the operator, addressable as a recipient, visible to the operator.

## Priority
A one-bit property of a message: **wake-now** asks the recipient to be woken immediately; **hold** asks that it be delivered at the recipient's next natural turn.

## Loop control
The per-message hop count, time-to-live, and sender chain, plus an optional per-thread budget, that let bots and the hub cut off bot-to-bot ping-pong.

## Attachment
Content carried with a message by reference: a content hash, size and media type, optionally an origin URL. Bytes may be hosted in the hub's **Blob store** or elsewhere.

## Blob store
The hub's content-addressed storage for attachment bytes, with size caps, retention, and scoped read access.

## Directory
The hub's listing of peers and what each self-reports: capabilities, quota state, location, presence, and when that was last updated. The hub stores and serves this; it does not verify it or act on it.

## Presence
A peer's self-reported state (idle, working, blocked) with a freshness timestamp. Goes stale after a timeout.

## Inbound webhook source
An external system (GitHub first) that posts to a hub URL; its payloads become messages routed to peers by a routing rule. A source is not a peer unless enrolled as one.

## Approval request
A message kind asking a human or another peer for a yes/no decision before an expiry.

## Escalation
The hub elevating a message that needs human input to the operator's notification channels.
