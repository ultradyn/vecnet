# PR54 requirements reconciliation

Base: PR72 `b4bb120974d2333f700e189cbc4ec403d0c5abeb`, incorporating current
master's exact-root delivery isolation. Original PR54 source is
`a9d4ee3ef0a726073d6a1505631ff81176eda684`; its plan and receipt remain historical
evidence, not acceptance of this reconciled source. Controller owns history
reconciliation, independent review, integration and merges.

| Original requirement | Reconciled behavior |
|---|---|
| Warm ordinary-use setup | PR72 saves explicit invitation-enrollment intent and retries its exact config/credential/root on use or repair. Ordinary existing profiles never acquire ownership. Historical broad auto-acquisition is intentionally superseded. |
| Durable opt-out | Separate private canonical-config/profile/as sidecar under the existing authority lock. Unbind persists refusal; saved intent checks before activation and under the final bind lock. Explicit addressed bind/setup clears only its target. Strict authority schema remains unchanged. |
| Shared shown-state / dedupe | Separate bounded `Displayed` proof from reservations and unresolved claims. Strict state updates preserve unknown top-level/reference metadata, decoded reference identity, and case-folded known-field aliases without creating ambiguous JSON. |
| Safe monitor acknowledgments | Complete output before proof/seen/ack. Pending claims and uncertain state defer one bounded event, preserving opaque cursor order and bounded retry. Explicit replay remains available. A failed output or post-output commit stops retry and ack. |
| Receipt recovery across upgrades | Preserve modern owner generation, pending-send guard and exact-thread recovery. Only exact-receipt/history settlement supplies the original key and full batch for display proof. RPC acceptance alone still retains accepted claims. Proof publication precedes recovery deletion and is idempotent after response loss. |
| Generic-hook isolation | Retain modern Codex signal-only early return and authority-to-state lock ordering. Generic successful output publishes proof; outstanding claims conservatively prevent cumulative hook ack. |
| Diagnostics | PR72 status and saved setup reasons cover activation/readiness, runtime and host limits. Historical runner-specific manager diagnostics are superseded by this explicit-intent runtime model. |
| One owner, stable state, two hubs, upgrades | Retain modern canonical HOME authority, config/generation pins and shared standalone/launcher recovery. No old per-hub registry/runner restoration. |
| Copy prompt | Already independently landed and improved on master. Preserve current UI bytes. |
| Manual inbox consumed replay | PR57 owns the exact text/JSON display transaction and truthful opaque-predecessor footer, consuming the proof-only `recordDisplay` API. PR54 alone does not close issue56. |
| Live acceptance / rollout | Historical real-account xsm, Fly/dev, persistent dogfood and release obligations remain separately attributable operational work. No live state, credential, service or message changes are made by this patch. |

Validation is through coordinated build-box lanes with two jobs and private
tmpfs test roots. Initial behavioral RED reproduced missing opt-out and six
failed/short-output premature-ack cases. Reviewer-driven RED reproduced metadata
loss for escaped reference identity and ambiguous Unicode case aliases. The first
broader focused run exposed old fixture assumptions (reservation equals display,
lock fail-open, foreign-state auto-reset, reused synthetic delivery IDs) and a new
fixture HOME mode error; those failures are retained and fixtures are adjusted to
exercise the new guarantees. Final commands/results belong in the controller's
campaign receipt; no historical suite counts are attributed to this source.
