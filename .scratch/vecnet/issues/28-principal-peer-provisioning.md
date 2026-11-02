# 28: Principal-scoped peer provisioning

Requested by cc-pagchat-cos for pag-chat Plan 8, 2026-11-03 (msg_01M6F1QSVQPMRC8ZH9951NTZVT). pag-chat reports Max decided that bots are created only by bots, starting from a bot-making seed bot, and that each new bot must get its vecnet peer automatically. Today, peer create is operator-only (`/v1/admin/peers`), and pag-chat will not hold the operator credential (it reads every thread). The same mechanism is phase 3 of issue 26: a launcher creating one `launched` peer per session. One feature serves both.

## Shape (vecnet's answer)
- **No per-peer secret.** A principal already receives every event for its peers on one stream and sends as any of them with `as` (§2.3, §2.2). Peers a principal creates get no credential of their own, so nothing secret is returned, stored or leaked. (If a peer later needs its own credential, the operator can `peer rotate` it.)
- **Create:** `POST /v1/peers {kind, name, idempotency_key}` with a principal credential → `201 {peer, idempotency_key}` (the key echoed back, for pag-chat's logs). Retrying the same `idempotency_key` within 24 h returns the original peer (200), or 409 if the body differs. This follows §2.2's send idempotency.
- **Operator-set policy per principal:** `vecnet principal limits <prn> --max-peers N --kinds pagchat-bot[,…]`. The default is 0 peers and no kinds, so the feature is off until Max enables it for a principal.
  - Kinds outside the allowlist get 403 `kind_not_allowed`. `operator`, `source` and `session` are never allowed.
  - Hitting the cap gets 429 `peer_cap_reached`, with `details: {limit, active, disabled}`. pag-chat asked for this so it can tell the User that retired bots are what reached the cap.
- **Disable and enable own peers:** `PATCH /v1/peers/{id} {disabled: true|false}` works only for peers the principal owns (otherwise 404). Disabled peers count toward the cap until the operator deletes them; that is deliberate, so a create/disable loop is bounded.
- **Marked:** the directory shows `created_by: <prn>` on such peers, which receivers use for trust (issue 26's `spawned_by` generalises this).
- **Audit and events:** an audit row for every create, disable, enable and refusal, plus `directory_updated`, as for admin creates. The operator dashboard counts peers per principal.
- **Scope rules** (§12) apply to created peers as usual. They inherit the principal's default scope.

## Tests
- Idempotent retry; the differing-body 409.
- Cap and kind refusals; default-off.
- Cross-principal disable refused (404).
- `as` works for a created peer at once.
- Audit rows; `directory_updated` emitted; `created_by` visible in `/v1/peers`.
- The operator can still rotate, scope and move such peers.

## Order
Plan with, or right after, the reactions build. pag-chat Plan 8 and issue 26 phase 3 depend on it.

## Contract freeze
pag-chat builds against a fake hub using exactly this API (msg_01M6F1S4R4WZQVRDFXG0A4BZ80). Any change to these shapes must be agreed with pag-chat first.
