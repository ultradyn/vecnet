# Search A integration receipt — 2026-11-08

PR: ultradyn/vecnet#31. Master before integration: f06465f8c7daabf87e5b9b01a3d8e4f4e4a22b60. Original PR tip cecc182e79d44c69235c0e93e40acb614f199cc5 is retained as an ancestor through history-only merge805ce3d02af8601568ed675e8cbb3426f00e427f. Its complete Git tree equals the independently reviewed and fully tested b24fab932df296ab16d4ec7c8b5b7f07ae7c5ab1 tree,94e07d60ab24abb0dbea857298ac9d6ac4a438d7. No original feature loss remains identified by the independent parity review.

The sidebar searches loaded peer names and hub thread titles, supports participant filtering and paged results, restores the list on Escape/clear, and handles keyboard retry and stale responses. Queries are bounded at128 Unicode code points. It searches names/titles only. The UI cap increases one16KiB step to624KiB.

## Validation

All executable gates used x-boss only, BB_JOBS=2/GOMAXPROCS=2, synthetic HOME/XDG/CODEX_HOME and physical TMPDIR through the corrected wrapper from the auto-delivery checkout. The source freeze and every raw log/PNG digest were independently verified by the controller. No local test/build ran.

| Check | Actual tested source | Result |
| --- | --- | --- |
| Full just test | b24fab9 | PASS,38 package success rows; wrapper exit0; run587.1s. No inferred individual-test count. |
| UI unit tests | b24fab9 working-tree bytes, recorded before commit |435 passed,0 failed,0 skipped; wrapper exit0. |
| UI serving/CSP/size guards | same source |8 passed,0 failed; wrapper exit0. |
| Search browser journey | same source |PASS; held More, append/error focus, stale query/participant responses, hostile text, long chips at1440/1000/390px. |
| Full just ui-shots | b24fab9 tree |PASS;336 PNGs,index covers each exactly once; wrapper exit0,run460.3s. |
| Formatting/whitespace | integration source |gofmt lists no files; git diff --check passes. |

The original Unicode regression tests were observed failing before the fixes. Final sharp-S/Kelvin regressions:21pass/8fail before the production change,29pass/0fail after; identical regression-test bytes across RED/GREEN. Earlier sigma and Retry/More focus regressions also retain their RED/GREEN artifacts. Unchanged prior-head results are preserved as prior-head evidence; the aborted6294f14 whole-Go run is not a PASS.

Independent source reviewer: native inherited strong model, separate context; final Spec PASS/quality APPROVED,C0/I0/M0. Original PR parity is explicitly checked. Independent screenshot/geometry reviewer inspected all 28 contact sheets/336 PNG entries and changed full search PNGs: Spec PASS, quality APPROVED, no blocker or major. Controller separately viewed sr-phone.png at 390 px and accepted the visual report. This verifies the captured fixtures, not arbitrary future runtime state.

Raw whole-Go SHA256:0ffcbbd0f709811cd4e0e09c2001aeca74cff3802eff41eb4d69548b92dc1ccf. Raw full-shot SHA256:967dd2f0bf9ad0ce1d3c1deebcc03dbefd8a620754eb93b4d00c1d869d3cb3cf. Local raw logs,facts,source freezes,reviews and all PNG digests are retained in `.superpowers/sdd/2026-11-08-ui-search-a/` in the gate worktree.

## Scope and remaining operations

Closing documents and copied screenshots do not change tested runtime source. Launcher/npm/automatic-delivery live acceptance are separate work; this receipt grants no real Codex/systemd activation approval. No schema migration or storage-flag change is included. Deployment and health verification receive a separate operations receipt after master integration.

Nonblocking visual/source follow-up M1: a directory rename replaces the selected peer chip and drops keyboard focus from its remove button. Retain/restore that control on refresh and add the named browser regression in visual-review.md. More/Retry focus behavior is covered and passes. This minor is intentionally deferred; no blocker/major remains.
