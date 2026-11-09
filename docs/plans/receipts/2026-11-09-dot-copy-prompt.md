# ChatGPT Dot Copy prompt — final component and integration evidence

Ready for review and merge onto `fbbf664f`, preserving the deployed dashboard and memory fixes. The wizard shows a short, selectable setup prompt and Copy prompt action. Clipboard failure now announces manual-copy guidance inside the active dialog; a successful retry clears it and copies the same displayed prompt. Credentials, link codes and server URLs are absent from the prompt.

The independent component is based on historical reviewed `2b703eeb`; its six-line patch was copied onto current master as `c1e82eba`. Fresh whole-source Sol review found I1 (shared failure toast outside the modal). A fresh Sol fixer added 43 canonical browser-regression lines before the product fix. Parent x-boss RED failed at the missing in-dialog status (actual/bb 1), raw `d0691756811d98315fdc24f0f301ad4c65439317f3f2389b675da8106ae08013`; the same fixer then changed only invite.js (+9/-3). Same independent source reviewer closed I1 with Spec/Quality PASS and no regressions. Reports distinguish source inspection from parent execution.

| Parent x-boss gate | Result | Raw SHA-256 |
| --- | --- | --- |
| Full Go (one job) | 5,147 passed; 1 existing TestWriteShots skip; 2 packages with no tests; actual/bb exit 0 | `464c705c38b288ae541dab84711e0e31196ce2e7eabd8b36db9f992ddd03b53d` |
| Full UI units (one job) | 492 passed; zero failures/skips; actual/bb exit 0 | `26b918702d5d00b1abde19e3fd0def1b2625b5b1ba3dbea99c1b7113c3415190` |
| Full screenshots | 393 PNGs; exact index; actual/bb exit 0 | `bd535a9d297de604cef42549de13580619dc21d5e0b7205abc097792c987f537` |
| Canonical desktop/mobile clipboard failure and retry | PASS; existing invite journey retained; actual/bb exit 0 | `e1dd80164b1c75a9c79d795ebe74c218b122063ed484f0449a03bf9a6d36d046` |
| Clipboard feedback image captures | PASS; existing invite journey retained; actual/bb exit 0 | `5c993549bca21373c0d808f2db474c57a28ef11f0aabef976f2ed9f999a86a97` |

All 2,110 frozen source entries matched before/after/current each run. Raw hashes, actual process exits, `bb: exit=0`, and two empty owned-process drains per final phase were checked. Tests used private synthetic HOME/XDG/CODEX roots and identity-checked physical TMPDIR; the global two-job cap and one full screenshot suite were observed. The original pre-I1 Go run was intentionally interrupted (255), raw `31769f07…`, and remains an interrupted receipt, never a pass.

The frozen product hash is `5e96ee05d2e77833b48ff6519aa923d774346b41e91806506811f61fb1b501d2`. The canonical browser regression hash is `cfda7c64c74c0be64784a378fc2ad80ad34a07d1fbb3f22c96b1a3db3e2e528d`. Closing documentation and generated PNGs are the only later source differences. Required full-suite screenshots are in [oui](../shots/oui/README.md); additional desktop/mobile failure and success feedback is in [Dot clipboard states](../shots/dot-copy-prompt/README.md). Controller inspected the mobile initial, failure and successful-retry images: prompt/actions are readable and the error remains inside the dialog.

R-DP1 ships this UI independently of the frozen native Codex release. R-DP2 permits the small Dot-local live-status seam and a meaningful permanent browser regression for the observed error path. No task instructions, credentials, native actor policy, binding/daemon/session activation or migration changed. Costs if wrong: revise the isolated UI patch and its clipboard feedback. No deferred Important issue remains in this component.

These fixtures prove displayed/copied text, clipboard failure/recovery, accessibility attributes and desktop/mobile geometry. They do not prove actual ChatGPT task execution, manual assistive-technology announcements, native Codex acceptance or resolution of the wider multi-GB memory report. Merge, installation and Fly health/asset verification are subsequent controller steps and are recorded in the PR closeout.

[Ledger](2026-11-09-dot-copy-prompt-ledger.md) · [Original source review](2026-11-09-dot-copy-prompt-review.md) · [Fix report](2026-11-09-dot-copy-prompt-fix.md) · [Scoped re-review](2026-11-09-dot-copy-prompt-rereview.md).
