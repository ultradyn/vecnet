# GitHub #51: retention warning labels

Source `3338187049a6dfca5445d219b48200c32747b1f9`, base master `9dc1730c9b98bf4b62d1536f87ec1cd8e9cdccbf`.

Each retention-shortened warning now names its affected setting using the hub's existing readable label. Multiple known keys are supported; missing or malformed key metadata keeps the previous generic explanation. Text uses the existing safe DOM helper. Separate risk cards and original Hub messages remain intact.

The existing synthetic Settings journey verifies both cards against their preview keys in List and Policy at 1440×900 and 390×844, for prospective and existing-data confirmation. The checks preserve cancellation, focus, default-off apply-existing, separate preview tokens, typed trim, exact PATCH/revision and rejected-replay behavior. Dialog/form/card and page horizontal fit are checked. Four mounted subtests cover multiple/unknown/missing/non-array keys and markup-like labels.

A Sol/high implementation subagent used PIRFL. Fresh independent task review: Spec PASS, Quality PASS, zero Critical/Important/Minor findings. Fresh whole-change review: Spec PASS, Quality PASS, zero findings. No accepted finding is deferred.

## Verification

All execution used the serialized isolated x-boss build-box lane, BB_JOBS=2/GOMAXPROCS=2, synthetic HOME/XDG/data, private physical TMPDIR and umask0022.

- Actual browser RED on unchanged production source failed for the missing acknowledged-event label. Focused mounted RED counted 2 pass and 3 fail (two failing leaf tests plus parent).
- Final full Settings journey: PASS, 106 PNG captures, including eight new warning-focused states. Existing lower approval-control captures are retained.
- Canonical UI suite: 646 pass, zero fail/skip/cancel, up from the verified 641 baseline.
- Affected Go UI/HTTP contracts: 10 pass, zero fail/skip.
- All 2,252 source/fixture entries were stable before/after every gate; the green inputs equal actual committed blobs. Controller independently parsed raw results and viewed desktop and phone captures.

This completes only the repeated-confirmation-notes item in #51. Source acceptance does not install another release or enable pruning/storage rollout.

Try synthetically from an isolated worktree:

```sh
BB_HOST=x-boss BB_JOBS=2 BB_PULLBACK=0 SHOTS_DIR=/tmp/vecnet-retention-label-shots just bb GOMAXPROCS=2 node scripts/ui/prefs.mjs
```

Operational brief, logs, reports, scripts and all 156 RED/GREEN PNGs must be copied and hash-verified outside the owned worktree before cleanup.

[Task review](2026-11-16-retention-warning-labels-task-review.md) [whole-change review](2026-11-16-retention-warning-labels-final-review.md) and [ledger](2026-11-16-retention-warning-labels-ledger.md). The existing Node MockTimers ExperimentalWarning is present in both baseline and final logs.
