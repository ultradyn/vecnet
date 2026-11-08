# Launcher UI fix2 receipt — 2026-11-08

Plan-Task: launcher-ui-fix2

Source commit: `ceec0cafa82ebd2a5365c5fbccb9b1efa1248865`.
Source parent: `256d7f78c1bca525f0838036d56b450274afe1cf`.
Branch: `feat/launcher-codex-live`.

This bounded batch addresses reviewer I1-R/C0 and I2-Q2/M0. It changes only
`internal/ui/static/launcher.js`, `internal/ui/static/launcher-model.js`, and
`internal/ui/jstest/launcher.test.mjs`. This report is committed separately,
after the tested source. No native, ACP, schema, CLI, smoke, or frozen2759
source was staged by this worker.

## Observed RED before production

Test drafts preceded production edits. The controller ran the following while
this worker held production unchanged, then authorized the fixes after RED:

```sh
node --disable-warning=ExperimentalWarning --test --test-concurrency=1 --test-name-pattern=fix2 internal/ui/jstest/launcher.test.mjs
```

Direct command exit **1**; **0/4 pass, 4 fail**, no skipped/cancelled tests.
Raw log: `/tmp/vecnet-launcher-ui-fix2-red.log`. This worker read the raw log
before editing production and confirmed these intended failures:

1. Late original 429 erased the stored unresolved operation while a remounted
   retry was in flight; the accepted retry's lost response then retained nothing.
2. Successful retry followed by the original 429 erased its receipt and changed
   the form from Open job thread to Launch job.
3. A no-workspace-tag request failed the exact ed88 closed input schema with
   `/workspace_mode: unknown key`.
4. Reload marked a valid legacy record with absent workspace mode invalid.

## Source behavior

Every send failure now retains the operation for explicit reconciliation or
discard. In particular, an earlier component's definitive error has no authority
to delete a newer retry or accepted receipt sharing that request key. This is
the conservative approach from the brief; a definitive rejection alone no
longer automatically enables a new operation.

Discovery retains the distinction between explicit workspace tags and legacy
implicit worktree behavior. Legacy requests omit `workspace_mode`; modern
requests continue sending the explicit selected mode. Recovery accepts an
absent mode and displays it as worktree without modifying the submitted body,
its serialization, key, deadline, or persisted record. An ordinary repository
alias named `temporary` remains valid. Backend workspace restrictions, scratch
metadata, 24-hour retry bound, identity scope/refusal, storage warnings, and
explicit discard warning remain in place.

The four new behavioral tests use two independently deferred responses across
a real component replacement, then reload recovery from storage. The legacy
schema fixture copies the full immutable ed88 schema and validates with the
daemon's existing schema validator. It retains `additionalProperties: false`
and explicitly proves that the modern field is rejected. Existing assertions
remain; the existing explicit modern payload assertion now advertises worktree
in its fixture. Modern worktree/project-root/temp and scratch cases remain.

## GREEN and source freeze

The controller granted one exclusive sequential CX slot after native GREEN.
Commands ran directly, without build2/bb or another executable wrapper:

| Command | Command/Node exit | Result | Raw log |
| --- | --- | --- | --- |
| `node --disable-warning=ExperimentalWarning --test --test-concurrency=1 internal/ui/jstest/launcher.test.mjs` | 0 / 0 | 22/22 pass | `/tmp/vecnet-launcher-ui-fix2-green-focused.log` |
| `node --disable-warning=ExperimentalWarning --test --test-concurrency=1 internal/ui/jstest/` | 0 / 0 | 457/457 pass | `/tmp/vecnet-launcher-ui-fix2-green-full.log` |

Both runs had zero failures, skips, or cancellations. No bb exit exists because
no bb wrapper was invoked. The three sources were frozen before GREEN, and
`sha256sum -c /tmp/vecnet-launcher-ui-fix2-source.sha256` confirmed all unchanged
after both runs and before the source commit. Scoped `git diff --check` passed.

```text
f35fd489ebf30880a83f12d76a8ef2725a50ea37d94625102d3c74e9df1d76ee  internal/ui/static/launcher.js
a45aede633c861e1a00bbf48004f645b27a228c4fda198e92bfa614d01b11578  internal/ui/static/launcher-model.js
5395cbd2a5b1399c7f8d649de5881327f6070f92510ad8dd2821cd3519795f88  internal/ui/jstest/launcher.test.mjs
```

## Static assets

Measured after GREEN by summing every file under `internal/ui/static`:

```python
from pathlib import Path
paths = sorted(p for p in Path('internal/ui/static').rglob('*') if p.is_file())
total = sum(p.stat().st_size for p in paths)
cap = 656 * 1024
print(f'raw_static_bytes={total} files={len(paths)} cap_bytes={cap} cap_KiB=656 headroom_bytes={cap-total}')
print('PASS' if total <= cap else 'FAIL')
raise SystemExit(0 if total <= cap else 1)
```

Direct measurement exit **0**, raw `/tmp/vecnet-launcher-ui-fix2-assets.log`:

```text
raw_static_bytes=664308 files=75 cap_bytes=671744 cap_KiB=656 headroom_bytes=7436
PASS
```

The **656 KiB cap is unchanged**. Static source grew by 24 bytes from the
previous 664,284-byte frozen source. No Go gate/build was run for this batch;
this is an exact raw-byte measurement against the existing gate constant.

## Raw evidence digests

```text
d8e172dd43bdfe58ea1fac1c6aacbe13ae7481b9593d8d710fed19833d9c04e8  /tmp/vecnet-launcher-ui-fix2-red.log
b9b05ccd998d00f8da72d191aff9e16381f6287418d47a353857cc6d576576c7  /tmp/vecnet-launcher-ui-fix2-green-focused.log
b17769d74d9ef947ca7789cc0b1ceba199ff2e77d0a5096bbc02795828938a65  /tmp/vecnet-launcher-ui-fix2-green-full.log
77672d58818d8d94dd7a74d28001243affe500ee49dab17b1f2808486b2a6df1  /tmp/vecnet-launcher-ui-fix2-assets.log
```

## STOP / drain / handoff

Both Node children exited and were reaped. A process inventory after GREEN
found no UI test, launcher browser script, or owned fresh-demo child. The CX
executable slot was explicitly released to the controller before committing or
writing this report. No further test/build/browser executable was started.

`scripts/ui/launcher.mjs` is unchanged, so no fresh browser run was required or
performed for fix2. Earlier fix1 browser screenshots are not claimed as fix2
behavioral verification. No provider, intake, live canary, deploy, or packaging
build was run. Independent scoped reviewer acceptance remains controller-owned.

The staged list was inspected before the source commit and contained exactly
the three UI files above. This report is the final, separate UI fix2 commit;
the handoff supplies its immutable HEAD. Push scope is the GitHub feature
branch only.

DONE
