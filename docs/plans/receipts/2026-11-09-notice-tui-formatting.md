# TUI received notice formatting — Batch2 component evidence

TUI host labels and known hub-notice formatting now validate structured URL fields before extracting a hostname or projecting raw notice JSON. Invalid values render fixed safe text. Valid signed values, optional empty NewURL, notice prose and unrelated event/error presentation retain their existing behavior. This adds two formatter defenses on top of Batch1's real HTTP/operator boundary guards.

| Parent x-boss check | Result |
| --- | --- |
| Genuine behavior RED | 90 failing leaves plus two failing roots; 256 passing test events, including all 218 HTTP/frame/error control events; actual/bb exit1 |
| Current focused formatter/boundary GREEN | 348 passed test events, zero failed/skipped; actual/bb exit0 |
| Current full TUI | 906 passed test events, zero failures; existing opt-in TestWriteShots skipped; actual/bb exit0 |
| Same independent explicit GPT-6.1-Sol source review | Spec PASS, quality PASS, C0/I0/M0 |

Each gate used one job, synthetic private homes and identity-checked physical TMP. All 2,160 source entries matched before/after/current checks; raw hashes and two empty owned-process drains were independently verified. Production changes are +13/-4 across model.go and screen_inbox.go. The new synthetic tests exercise host/formatter values and real HTTP operator/TUI effects, including no apply/ack/cursor advancement for rejected pages and preserving typed transport causes.

R-I47N4: independently reviewed TUI formatter defenses can precede the remaining CLI receive-policy work because Batch1 provides the operator boundary dependency. R-I47N5: the parent applied only canonical gofmt to the new test after preserving its original c844d50f source; current 03c3ad37 is exactly gofmt(original), with no assertion or fixture change. The behavior RED remains attributed to its original source pin.

Raw SHA-256:

- RED: `62c547127a8b142bdf5a121a6ee14f89ff5583d1d9e924c9f33e31727d1bc847`.
- Focused GREEN: `0bd1403b76e407ceeffd393976b9aae426da7456b6ec628c619d9fd61b577fbb`.
- Full TUI: `2119dd06ff357fe6de20341518cfc023c9a245dd16abde63614f9e1ab8c320bf`.

Source-only review checked the exact formatted test and product hashes, shared validation, accepted-output preservation, real HTTP controls, and unchanged Batch1 dependencies. Parent runtime gates separately establish execution on those same inputs. Current CLI normal/JSON output, hook/watch transactions, policy constructors, follow/enrol/preferences recovery and final combined integration remain outside this component. No whole-I47 closure, installation, live-state change, master merge or deployment is claimed.
