# TUI archived-peer filtering — integration evidence

Ready for review and merge. The dashboard hides archived peers by default; `a` reveals them with an archived row marker. Disabled peers remain visible. Keyboard, mouse and Enter target displayed rows. Hiding a selected archived peer repairs selection without changing lifecycle data; empty and all-archived lists remain usable.

The reviewed implementation and regressions affect exactly four paths under `internal/tui`: `screen_dash.go`, `screen_dash_test.go`, and the 140×42 / 80×24 dashboard goldens. Master `08d39a62` merged cleanly into candidate `53c5a3a2`; these four files remain identical to component `75a0c2e2`.

| Controller gate on x-boss | Observed result |
| --- | --- |
| Canonical `just test`, two jobs | PASS: 38 packages with tests, two without tests; actual exit 0 and `bb: exit=0`. Individual test counts are not emitted. |
| Fresh independent Sol source review | Spec PASS, Quality PASS, C0/I0/M0. |
| Canonical `just ui-shots`, two jobs | Every journey and exact 393-PNG index passed; remote application exit 0 and source-after verification emitted. |
| Screenshot recovery | All 393 remote/local PNG hashes and canonical index match; two empty owned source/TMP process censuses and current source verified. |
| Controller visual inspection | Dashboard success: readable columns, panels and `a archived` hint; hostile content stays literal. |

Full Go raw SHA256: `065d6983a7f37f37960ff3e9f3d55dcc1517db6388843d0f6be4f29061b963f6`. Screenshot raw SHA256: `ed57a48eb2a8277fe5d657514ef33d94fa2d5e81ae43cb71bf8033a8bdf73e00`. All 2,121 frozen source entries matched before/after/current each suite.

The screenshot wrapper failed AFTER the successful application run: its pullback destination incorrectly used a remote `/work` path on xsm. Its original exit 1, absent bb footer and failed receipt remain preserved, never relabeled as a successful wrapper run. Recovery reverified the exact owned lane's source, empty drains and completed output before transferring and hashing it. A first recovery copied the PNGs but incorrectly sought an index in generated output; a separate corrected check uses the frozen canonical source index. No application suite or assertion was rerun or weakened. Screenshots: [existing index](../shots/oui/README.md).

Regressions were added after original implementation. Historical master visibility RED and marker-only mutation failure are substitute evidence; no test-first implementation history is invented. Coverage includes visible-row navigation/Enter queries, mouse hits, selection repair, empty lists, lifecycle preservation and row-local markers at both widths.

R-TUI52-1 ships this four-file TUI change independently of native Codex actor-policy acceptance. R-TUI52-2 accepts bounded output recovery with the failed wrapper receipt retained. R-TUI52-3 permits closing docs/PNGs and records Max's prompt serial Fly deployment request. Costs if wrong: isolated TUI revert or revisiting output evidence. No native authority expands; no deferred Important finding remains.

Closing docs and PNGs preserve all executable gate inputs. Merge, clean-master build, CLI/devhub install and Fly health/asset verification are subsequent controller steps recorded in the PR closeout. This establishes no native acceptance, automatic delivery installation, multi-GB memory resolution or release tag.

[Source review](2026-11-09-tui-archived-filter-review.md) · [Ledger](2026-11-09-tui-archived-filter-ledger.md).
