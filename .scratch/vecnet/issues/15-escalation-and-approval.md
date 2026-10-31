# Escalation and approval requests

Type: grilling
Status: resolved
Blocked by: 08, 14

## Question

`approval_request` kind with expiry and response; automatic elevation to the operator of messages that need human input; how the priority bit interacts with operator notification webhooks; what happens on expiry.

## Answer

Max approved the approval lifecycle by voice, 2026-11-01, and asked that escalation not be part of getting the core working: **escalation is phase 2**, specified here but not a v1 build item.

- **Approval lifecycle (v1):** `approval_request` is a message kind with `approval_id`, `question`, `options` (default yes/no), `expires_at`. The first valid `approval_response` from an addressed recipient closes it; later responses are rejected with 409. At expiry the hub emits an `approval_expired` event to the thread's participants and posts an `approval_response` with `decision: "expired"` into the thread.
- **Operator notification (v1, minimal):** a `wake` message addressed to the operator peer fires the operator's configured notification webhooks; `wake_idle` and `hold` land in the inbox only. Webhook config: one or more URLs (attn bridge, Telegram relay), each with a filter (`wake` | `approvals` | `dead_letters` | `all`).
- **Escalation (phase 2):** when an approval addressed to a non-operator is still open at the halfway point of its window and `escalate_to_operator` is true, the hub forwards a copy to the operator with `wake` priority. Not built until transports, delivery and the CLI work end to end.
