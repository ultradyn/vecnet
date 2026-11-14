# Automatic delivery reconciliation — PR54

Original PR head: `a9d4ee3ef0a726073d6a1505631ff81176eda684`.
Reviewed production source: `3738310dc25053d2ba1ea490e4d2f5dad403be8f`,
including current master/PR72. Requirements and superseded historical mechanisms
are mapped in `../2026-11-15-pr54-residuals.md`.

Independent Standards/Spec review passed after correcting two findings:
generic hooks cannot acknowledge unproved legacy reservations, and monitors
cannot adopt evidence lacking complete Hub/Caller identity. Exact RED reproduced
two reservation cases and twelve partial-identity cases; the final nine targeted
controls passed. Same-subject credential rotation requires fresh authentication
and retains its documented identity contract.

Final controller gate used x-tall, jobs2, private tmpfs TMPDIR and normal umask022:

- `TestWatchAdoptsFollowByOtherCommand`, count10: PASS, 12.100s.
- Full `internal/cli`: PASS, 116.096s; full `internal/localruntime`: PASS, 1.605s.
- Race-enabled hook proof, partial identity, opt-out, unbind, receipt, guard,
  transfer, incomplete/pending/deferred watch and strict metadata controls:
  PASS, 4.026s.
- Whole Codex engine: 137/137 PASS, 131.416s on the preceding integrated tree.
  Subsequent production changes are Go hook/identity corrections; engine bytes
  are unchanged. Exact receipt retry control also passed independently.

The final test-only follow correction is independently reviewed. Initial replay
output did not prove an SSE request existed; the fixture now waits for the actual
log waiter before relocation and prevents its scheduled follow from racing the
other command. It retains the destination, adoption and identity assertions.
No production timeout was relaxed. Earlier umask077 gate failures came from
permission fixtures expecting0644; those expectations remain unchanged.

The reviewed modern tree retains exact-root authority, generation guards and
receipt recovery. The obsolete per-hub runner is intentionally superseded.
History reconciliation retains the original PR as a second parent without
changing the tested source tree. This source merge does not release packages,
replace real persistent services, or clear a real retained backlog. Manual inbox
consumption/footer behavior belongs to PR57.
