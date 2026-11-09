# Thumbnail owner transfer and explicit retry — Task2b2

The opt-in cache adds atomic owner transfer and explicit retry. N=1 transfer preserves the source permit, lease, URL and generation without a zero-owner cleanup interval. Retry revalidates saved eligibility, reuses its permit and starts fresh generations only from error/waiting. Existing budget tickets own aggregate priority and FIFO order. The legacy cache/DOM route remains unchanged.

Parent x-boss verification on exact model69a0d39f / testsba5c34bf:

| Gate | Result |
| --- | --- |
| Genuine extension RED | Six missing-method/behavior failures, 28 unchanged controls; actual/bb exit1 |
| Current focused cache/cleanup/legacy | 34 passed, zero failures/skips/cancellations; actual/bb exit0 |
| Current full UI | 548 passed, zero failures/skips/cancellations; actual/bb exit0 |
| Relevant current Go embedding checks | 7 passed, zero failures/skipped tests; actual/bb exit0 |
| Fresh independent explicit GPT-6.1-Sol review | Spec PASS, quality PASS, C0/I0/M0 |

All 2,160 source entries match before/after/current checks. Raw hashes and two empty owned-process drains per gate were independently verified. Each gate used one x-boss job, synthetic private homes and a physical owned temporary directory.

Production changes are +37/-1 model lines. Six roots append 197 lines to the exact preserved 375-line core prefix; budget, API, eight cleanup and eight legacy controls remain unchanged. Review traced owner identity/permit movement, callbacks, eligibility, priority/FIFO and reentry through actual budget and cache code. These are logical and observable unit contracts, with physical DOM/native memory validation still pending.

Raw SHA-256:

- Genuine RED: `dc525a8d35d29bd4e502a26d757913ae48e80ec337da468b7144be592a579e8f`.
- transfer-green: `68f245626b546686ba9ff90cb741ace36f819882c6ff5638c1111dcf9444c189`.
- transfer-ui: `3c4d99b90a6aacbda04158daa96ea6613c329880b1e8acb9af1745fec7e886c0`.
- transfer-go-ui: `de863c9cfe770d038b6ea8a0c33e49a50c6949e87fb1762d1f090180b3bf6d1f`.

Task3 still needs one atomic visibility/viewer/focus integration, bounded matched browser measurements and final combined release gates. Its source plan exposed a late-cleanup recovery reachability gap after route retirement discards the old teardown. A separate source-design resolution is in progress; these component checks do not resolve that caller-lifetime seam. No DOM activation, master merge, deployment, native/browser RAM ceiling or closure of issue #60 is claimed.
