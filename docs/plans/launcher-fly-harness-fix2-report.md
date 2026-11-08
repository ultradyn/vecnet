# Fly harness F1 oracle correction

Source commit: `018bf56d0cbaa4103958e18aa1fed4db7f70cfff`, based on frozen
candidate `872e0710bfba0263fcc989809f291e47e2d65e4a`. This report is LAST.
Only `scripts/native-launcher-fly-smoke.test.mjs` changed in the source
commit. Production, shared helpers, fake daemons, build inputs and package
metadata are byte-identical to that candidate.

F1 identified nine negative cases that could accept unrelated post-enrolment
exceptions. The lost-response assertion could compare two undefined values;
retry's empty history predicate was vacuous. Each case now records that the
specific injected fault was reached and asserts its diagnostic, owned request
or command, durable journal phase and retained recovery state. Lost-response
requires one nonempty journaled request and no accepted IDs/history reads.
Retry requires two identical requests, all three original accepted IDs and the
actually supplied changed-thread response. Cancellation and teardown cases
require exact owned sender/job/thread proof; foreign acknowledgements cannot
reach unit removal. Provider rejection requires an actual running response
with the supplied mismatched provider before restart.

## Observed chronology

First, fixture instrumentation and an unrelated post-enrolment setup-failure
control were added for all nine boundaries, while the old oracle bodies were
extracted unchanged. Executing those old assertions produced **15 pass / 10
fail / 25**, actual exit **1**: all nine controls exposed false positives,
plus their parent test failed. This was genuine behavioral RED, not a
missing-module or fixture setup failure. Its exact test source is retained as
`old-oracles-red.test.mjs` beside the raw receipts.

Only then were the oracle assertions strengthened. The unfocused Fly,
shared-support and cleanup suites passed **77/77**, actual exit **0**, with
no skips or cancellations. Both commands ran sequentially in CX ONE; the
last child exited **2026-11-08T05:54:59.250595Z**. CX then STOPPED/drained
and returned the slot; no build/test executable was started afterward.

Raw evidence: `/work/package-cache/xertrov/vecnet-fly-harness-fix2/`.
Each receipt contains exact argv, actual exit, timestamps, log digest and
matching before/after inventories of all 2093 tracked files. The modified
test was tracked throughout. HEAD remained at base872 during execution;
the source commit subsequently captured those tested bytes.

| Evidence | SHA-256 |
| --- | --- |
| RED tracked-source inventory | `94e2d10988ec85eed50abcbe728ee6ab79a80988c0cf6b9718c19fb10f6cbd63` |
| RED log | `752df7f841d99a0d64531d260ecacc2f52ca2b36a5b22f9c6816e0ff1b6599dc` |
| GREEN tracked-source inventory | `8ed0f0d4b0e2278b5709ddfcc952ac678bf1d8c1dd734197d7e345a768669efe` |
| GREEN log | `66e48ef5d93e50430610d5254800217f11942301e5e6b3096010e0a983ee3117` |

This synthetic correction establishes oracle sensitivity, not real daemon,
provider, Fly or installation acceptance. SAME Lens5th scoped rereview remains
required. The separate native whole-branch W-I1/W-I2 production correction
invalidates treating872 as a final product freeze: its passing gates remain
an explicitly attributed baseline, with fresh combined gates required after
integration. No live, install, merge, release or xsm activation clearance is
claimed by this report.

=== DONE ===
