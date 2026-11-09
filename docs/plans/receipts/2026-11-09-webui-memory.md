# Dashboard memory retention component receipt

2026-11-09. WEB-MEM-1 / issue #60. This records isolated component evidence before integration and deployment. Max reported multiple pages on vecnet.xk.io using several GB without a known trigger. These synthetic measurements establish a dashboard retention defect and its bounded replacement; they do not reproduce or close the reported multi-GB incident.

## Change and behavior

The dashboard held a permanent union of audit entries and retained departed peer row/chart owners. The candidate keeps one 50-entry current audit page, preserves explicit Older/Newer/Latest history navigation, rejects stale async completions and prunes departed peer owners after a successful directory render. Existing peer focus, expanded tables and last-good data on errors remain covered. No hub API/schema changes.

## Source and gates

Base `3d06953f21299b268d84649ea3a488b62807ddad`. Production `internal/ui/static/dash.js` SHA-256 `c15c4b88aba291233520c192cdbfc085a911f2ab9608471e5e93e83443aaba54`; regression `internal/ui/jstest/dash-memory.test.mjs` SHA-256 `7ad91d49c1a3a0023e7ab47953cc45ee0bdd904ba0ec7c8b5f08d2d795b7629f`. Full hashes and before/after inventories are retained in the workspace receipts.

All execution used x-boss, one job per lane, isolated private homes and synthetic data, within the aggregate two-job cap. The controller verified actual process exits, `bb: exit`, raw hashes, relevant source pins and two empty owned process censuses.

| Gate | Observed outcome | Raw SHA-256 |
| --- | --- | --- |
| Initial retention regression | 6 expected failures, 5 controls pass; actual exit 1 / bb 1 | d1bc089b18a32c869969cdfa54abcecd518e04fd45277fb4b94f433318183156 |
| Focused UI regression | 11 pass, zero failures/skips/cancellations; actual exit 0 / bb 0 | 36a4d0de4cdcb3fc8902300ee3e8d9acd0b6eeb5b9f8bbe7406de6904c6165e7 |
| Full UI unit suite | 489 pass, zero failures/skips/cancellations; actual exit 0 / bb 0 | 9740fb8cca5f184bad65a373d3e254ab8cc0c8a1f6c1e110c17eedd5b69be846 |
| Go UI embedding/CSP/size checks | 10 pass; actual exit 0 / bb 0 | ff97cd726c9cbf97e32e960f2b6ed9607a20a717732ea750c50f581b9de8eda5 |

Independent GPT-6.1-Sol dashboard source review: Spec/Quality PASS, C0/I0/M1. The minor is supplemental malformed-page, deeper-Newer-failure and delayed-head-after-unmount coverage, retained as follow-up in issue #60. The separately reviewed profiling harness passed its scoped review after fixture/timeout/process-classification corrections.

## Comparable browser observations

Same profiler SHA-256 `500e9b93e493a7fc14112e5000434a4df0a7f4e8ef2e4eba1eaa8c2a26be089b`, immutable original assets and pinned candidate assets; private Chromium, loopback synthetic server, warmup 10, checkpoints 100 and 500, seed `webmem1`, quiet 20 ms; 50 audit entries per head, two visible peers, sixty chart buckets and 256 detail bytes per audit entry. Heap values below are retained JavaScript bytes after GC. They are not total tab/process memory.

| Arm | Refreshes | Baseline audit rows | Baseline retained JS bytes | Candidate audit rows | Candidate retained JS bytes |
| --- | --- | --- | --- | --- | --- |
| control | 0 | 50 | 1329828 | 50 | 1323696 |
| control | 100 | 50 | 1453364 | 50 | 1450824 |
| control | 500 | 50 | 1484044 | 50 | 1496164 |
| audit-heads | 0 | 50 | 1318176 | 50 | 1335276 |
| audit-heads | 100 | 5050 | 4172108 | 50 | 1452788 |
| audit-heads | 500 | not completed | not completed | 50 | 1498000 |

The candidate completed control, audit-heads and peer-churn arms through 500 refreshes: all nine checkpoints, functional navigation/focus checks, zero reported errors, actual exit 0 / bb 0; raw `18c0506bf7b08af309c84a498fa769149b4434c222c78591ce48daf8eeb720d1`. Candidate peer-churn heap at 500 was 1,535,256 bytes; audit row count remained fifty.

The original baseline was intentionally interrupted with SIGINT during its growing audit render workload after completing the audit checkpoint at 100. Its final record is FAILED with signal SIGINT and bb exit 1; preserve that failure. Baseline audit 500 and peer-churn measurements are incomplete and are not passes. Five completed checkpoints remain usable observations with verified source/harness correspondence. Parallel execution used separate immutable source/runtime lanes; no wall-time or host-wide memory comparison is claimed.

Renderer RSS/PSS could not be classified reliably, so both completed candidate measurements and smoke runs report COMPLETE_PARTIAL_INSTRUMENTATION. The retained JS/DOM metrics are valid within their recorded scope. There is no multi-GB total-browser claim, renderer budget, long-term plateau proof or production-incident attribution.

## Remaining work

The mandatory audit pagination browser gate passed on the current script and production/test source: actual exit 0 / bb 0, raw SHA-256 `64b4f22e604d87e3cf89e9b379e29a04550dfb80c4204e5941ca7c229c72abae`. It verified exact fifty-row Older/Newer/Latest replacement, a new head while browsing history, section loading/error isolation, the page write guard and absence of unexpected console/CSP errors. Eight PNGs are committed under `docs/plans/shots/webui-memory/`; the controller visually inspected the new head controls screenshot. The final independent GPT-6.1-Sol component source review passed with C0/I0/M1 (same supplemental coverage follow-up). Source inventories and two empty owned process scans were verified before collecting generated screenshots. Later closing receipt/changelog/ledger edits and generated PNGs do not change the executable source exercised by these gates. Master integration requires the full merged-source Go and screenshot gates, visual inspection, final review, publication, installation and deployment. Shared event-pump abort bookkeeping and eager thread history/image allocation remain separate follow-up lanes. Keep issue #60 open until the wider report and installed behavior are established.

Evidence workspace: `.superpowers/sdd/2026-11-09-webui-memory/` in the component worktree; raw failures, source inventories, browser JSONL, qualified summaries, reviews and drains are retained.
