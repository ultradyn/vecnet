# 34: A principal can re-enable a peer the operator disabled

Found 2026-11-04 by the issue 28 shape check (`.scratch/vecnet/deploy28/issue28-shape-diff.md`, R1) on deploy28 @ 44e2767.

`PATCH /v1/peers/{id} {"disabled": false}` from the creating principal re-enables any peer it created and still owns, including one the operator disabled. `SetOwnPeerDisabled` records no disabler, so an operator disable is not sticky. Operator authority should win: an operator-disabled peer stays disabled until the operator re-enables it.

## Proposed fix
- Record who disabled a peer (`disabled_by`: `operator` | the principal id) in a new migration.
- A principal PATCH that would re-enable an operator-disabled peer is refused. That adds a refusal to the frozen issue 28 surface, so agree the code and shape with pag-chat first (proposal: 403 `operator_disabled`, `details.peer_id`).
- Operator disable/enable through the admin API is unchanged; an operator enable clears `disabled_by`.
- Audit the refusal.

## Also from the same check (no action unless pag-chat asks)
- D5: `idempotency_key` must match `^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`; confirm pag-chat's key format.
- D2: `kind_not_allowed` carries `details.kind`; PATCH 404 carries `details.peer_id` (additive).
- D3: refusals by a peer credential are audited with the peer as actor (§18.4 says principal); internal.
- R2: `peer_create_keys` rows are pruned only on key reuse; add a periodic prune with the next hub change.

## Agreed 2026-11-04
pag-chat OK'd 403 `operator_disabled` (details.peer_id). Their provisioner will treat it as 'this one peer is disabled' and won't halt (their Task 12). pag-chat's idempotency keys `pagd:<16 hex>:<bot_id>` match the key pattern (D5 closed).
