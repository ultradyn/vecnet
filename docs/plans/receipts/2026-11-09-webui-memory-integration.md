# Web UI memory integration receipt — PR #63

2026-11-09. Ready for review and merge after combined gates. This integrates the independently reviewed dashboard, shared event-pump and Markdown retention components onto merged master `ac85db36` (PR #62), preserving the current sparklines, focus, accessible readouts and chart retirement behavior. Deployment is a subsequent controller step; these gates do not establish production installation.

## Resulting behavior

The dashboard retains one 50-entry live audit head and one selected history page. Older/Newer/Latest refetch the requested page without retaining the full history or accepting stale completions. Event-pump waits remove their abort listener when they settle or abort. Parsed Markdown uses a FIFO bounded by 1,000 entries and 256 Ki UTF-16 input units; oversized messages render fully and bypass memo retention. No API, migration, runtime dependency, authentication or native actor-policy change is introduced.

## Combined source and validation

The product/UI gate pin is `c075f63efe676a4377230c0003cfcdd001970d09`. The final screenshot source differs only in reviewed `scripts/ui/subs.mjs` SHA-256 `5191ee714794ae44ac33ae95feb67aa751da594783618ddff34684f6946f4c90`. Every other one of 2,104 inventoried inputs is identical; no Go/dependency/embedded UI/UI unit input changed. Closing documentation and generated PNGs are later artifacts. The all-files embedded asset guard moves from 640 to 656 KiB, the smallest normal 16 KiB step above the measured 657,219 bytes, without excluding files or changing its assertion.

All execution used x-boss, isolated private HOME/XDG/CODEX_HOME, a physical owned 0700 TMPDIR and at most two aggregate jobs. The controller checked actual process exits, build-box exit footers, source inventories and two empty owned-process scans after every accepted gate.

| Gate | Observed result | Raw SHA-256 |
| --- | --- | --- |
| Full Go suite (`go test -json -p 1 -timeout 40m ./...`) | 5,147 test passes, 2,014 top-level tests, 38 passing packages; actual 0 / bb 0 | `010e54b0158101afe310a6d10cc14b3b4706f6f847915132a9d747f9461c4fa5` |
| Canonical UI suite (`just test-ui`) | 492 passed; zero failures, skips or cancellations; actual 0 / bb 0 | `526af93b22d610e36e071fc1d3c28b400d97ee825585300365fc896bc6460752` |
| Corrected subscription browser journey | Six health states, read-only dashboard, conditional Resume, failure/recovery passed; actual 0 / bb 0 | `2497ffae7aa7246eecd00e9e4f141c27803fff7fe7a87c1a96df19287659e234` |
| Full screenshot suite (`just ui-shots`) | All 393 PNGs generated and indexed exactly once; actual 0 / bb 0 | `d6bd0c52bc4de68a15f1a171a769c5b39408cb009f197dff84f1adbea84dbd1f` |

The only skipped Go test is existing opt-in `TestWriteShots`; two additional package-level skip events are packages with no tests. The screenshot suite includes its eight passing harness unit tests. Exact raw files, manifests, all PNG hashes/dimensions and process drains are retained in the integration and merge-gate workspaces.

## Independent review and screenshot evidence

Fresh GPT-6.1-Sol whole-source review found no additional Important production defect, but rejected an incoming regression that expected PR #62's removed minute table. The corrected test checks the actual SVG, cursor/readout, focus, exact interval, error retention and recovery; the same independent reviewer accepted it and the asset guard correction. The original source review's minor missing-image finding is closed by the actual four current audit images, exact index validation and controller visual inspection.

The first full screenshot run exposed an obsolete subscription fixture assertion that the dashboard had zero buttons. A bounded correction requires exactly Older/Newer/Latest in DOM order in the actual audit section, while preserving zero subscription controls and every unrelated health/clock/Resume/failure/recovery assertion. The same independent reviewer passed this exact delta with C0/I0/M0 before the full rerun. No product code or timeout was changed to accommodate the fixture.

The controller inspected [live audit head](../shots/oui/12-dash-audit-head.png), [head controls](../shots/oui/12-dash-audit-head-controls.png), [historical page](../shots/oui/12-dash-audit-history.png), [history controls](../shots/oui/12-dash-audit-history-controls.png), the subscription view, desktop activity and the focused 320px peer readout. Text/control states remain legible and the narrow readout fits. These are isolated synthetic captures.

## Failures retained and limits

Preserved failures include the first integration runner's pre-test scratch-manifest refusal (`32268097`), the actual obsolete-test failure (`aa671e39`), the original asset-cap failure (`81a1dc0a`) and the first full screenshot run's obsolete global-button assertion (`a7c7d222`). None is described as a pass or a product regression it did not establish. Component receipts retain their genuine initial RED/GREEN evidence and the interrupted profiling baseline.

The qualified synthetic heap/DOM observations in the component receipts establish bounded retention at those seams. Renderer RSS/PSS, the reported multi-GB incident, thumbnail/history/viewer demand integration and production-volume measurement remain open under #60. This PR does not close that issue, activate native Codex delivery, satisfy its separate live actor-policy acceptance, or cut a release.

Evidence: `.superpowers/sdd/2026-11-09-webui-memory-integration/` and `.superpowers/sdd/2026-11-09-webui-memory-merge-gate/` in their respective worktrees. The standing worktree cleanup hold remains in effect.
