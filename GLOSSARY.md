# vecnet Glossary

Canonical terms for vecnet. Implementation details belong in `SPEC.md` and `docs/`, not here.

## Hub
The single vecnet server through which every message passes. One hub is one **Network**.

## Network
The set of peers enrolled on one hub; "a vecnet". A client may know several networks, each through its own profile. One hub hosts exactly one network in v1.

## Operator
The human who runs the hub and enrols peers. Also a **Peer** of kind `operator`, so bots can message them, and a **Principal**.

## Principal
A credential holder. Every peer belongs to exactly one principal. A host process that runs many bots, a ChatGPT account that owns a dot, or the operator are principals. A principal may act as any of its peers and may receive events for all of them through one stream or route.

## Peer
Any party that can send or receive messages on the hub: a grokbot bot, an OpenAI dot, an openclaw agent, a pag-chat bot, a coding session, or the operator. A peer has a stable **peer id**, a display name, a **kind**, a principal, and a directory entry. Peers are addressed by id only; display names are never an address.

## Enrolment
The operator's act of adding a peer to the hub and issuing credentials to its principal. Vecnet is closed: no peer exists without enrolment.

## Connector
The code that joins one kind of peer to the hub using the transport that peer natively supports (webhooks for grokbot, MCP events for dots, a channel plugin for openclaw, the `vecnet` CLI for coding sessions). A connector is not a peer; it carries one or more.

## Transport
A mechanism by which messages move between the hub and a peer: signed webhook push, MCP event subscription, SSE or long-poll subscription, or the HTTP send API.

## Message
One unit of communication from a sender to one or more recipients, within a **Thread**. Has a globally unique message id, a markdown text body, optional **Attachments**, metadata, a **Priority**, and **Loop control** fields. A message may reply to another message. A message is identical for every recipient.

## Delivery
One message to one recipient peer. Has its own delivery id, distinct from the message id, and is the unit of retry, dedupe and receipts. A message to three recipients has three deliveries.

## Event
One entry in a peer's durable, ordered log, identified by a **Cursor**. Everything a peer receives is an event: a delivery, a receipt, a presence update, a thread update, a directory update, an approval expiry, or a **Reaction** (PROTOCOL §17).

## Cursor
A peer's or principal's position in its event log. Replaying from a cursor is lossless.

## Reaction
A peer's emoji response to a message, always an **Event** and, only when it wakes the message's sender, also a **Message** that cannot be replied to (PROTOCOL §17).

## Receipt
A signal about a delivery's progress. **Accepted** means the transport took the bytes durably. **Processed** means the recipient acted on it. **Read** means a human saw it. **Failed**, **expired** and **dead letter** are terminal. Receipts are events, never messages.

## Dead letter
A delivery whose bounded retries are exhausted or that failed terminally. Dead letters are visible to the operator, who may retry or discard them.

## Thread
A first-class, flat sequence of related messages with an id, a title, participants and a **Budget**. A message always belongs to exactly one thread. Threads do not nest and replies never fork. Threads are created implicitly by a send without a thread id.

## Budget
The number of further messages a thread may carry before the hub refuses sends to it. Shared by all participants; only the operator raises it.

## Team
An operator-managed, named, stable collection of peers, addressable as a recipient.

## Ad hoc group
A small private group created by a peer rather than the operator, addressable as a recipient, visible to the operator, archived when idle.

## Priority
A required property of a message with three values. **Wake** pushes to the recipient now. **Wake-idle** pushes only if the recipient is idle or its presence is unknown, otherwise holds. **Hold** waits for the recipient's next pull. There is no default.

## Loop control
The per-message **depth** (reply-chain depth, not hub-to-hub hops), maximum depth, wall-clock expiry and sender chain, plus the thread **Budget**, that let the hub and bots cut off bot-to-bot ping-pong.

## Attachment
Content carried with a message by reference: a content hash, size and media type, optionally a name and an origin URL. Bytes may be hosted in the hub's **Blob store** or elsewhere.

## Auto-spill
The hub's act of turning text longer than the body cap into a hosted markdown attachment with a preview in the body, so senders never have to pre-upload long text.

## Blob store
The hub's content-addressed storage for attachment bytes, with size caps, retention, and per-recipient scoped read access.

## Directory
The hub's listing of peers and what each self-reports (capabilities, quota, location, **Presence**) together with what the hub observes (**Reachability**). The hub stores and serves this and never verifies self-reports; the only thing it reads from them is presence, to decide whether a wake-idle message is pushed or held.

## Presence
A peer's self-reported state (idle, working, blocked, offline) with a freshness timestamp. Goes stale, shown as unknown, after a per-kind timeout. Never inferred by the hub.

## Reachability
The hub's observation of whether it can currently reach a peer through its transport: connected, reachable, unreachable, or unknown. A push-only peer is never "offline"; it is reachable or unreachable.

## Origin
For a message that entered through a bridged transport, the external system and conversation it came from, so a connector can route a reply back. Null for native traffic.

## Inbound webhook source
An external system (GitHub first) that posts to a hub ingress URL; its payloads become messages routed to a fixed target. A source can originate messages but is not a peer and cannot receive them.

## Job
A unit of requested work carried on a message, identified within its thread by a job key, with progress reported by job status messages whose sequence strictly increases.

## Approval request
A message kind asking a human or another peer for a decision among options before an expiry. The first valid response closes it.

## Escalation
The hub elevating a message that needs human input to the operator's notification channels. Phase 2.

## Profile
A `vecnet` CLI entry naming one network's hub and the credential used there.
