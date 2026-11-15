# Web UI pending thumbnail cancellation — GitHub #60 component

Reviewed source: `27529818041bf5f589ea24797ab2de180aab8517`, based on master
`c5f6429dfc12927670e18c13ce149f5284070751`.

Navigating away from a thread now aborts that view's unfinished thumbnail
body reads. Each cache owns an AbortController; release marks it disposed
before aborting, revokes its URLs and remains idempotent. The existing blob
API accepts an optional third `{signal}` argument and forwards it to local
and R2 reads. Existing one/two-argument callers and attachment checks remain
compatible. A different live cache can still complete a read of the same SHA.

The regression exercises the actual cache → API → native fetch/body path
against a real partial HTTP response. It requires client closure and native
reader rejection before fixture cleanup, without a server timeout supplying
cancellation. Four new regressions fail on the old source and pass after the
bounded fix; forty focused controls pass in both runs.

## Actual browser evidence

A fresh isolated Chromium runs the real served application, mounted thread,
thumbnail and blob API. The fixture forwards authentication to the real
synthetic hub before substituting the verified image's partial response.
Completed PNG decoding, viewer/Escape/focus, URL revocation and unauthorized,
wrong-origin/host/query controls pass. Before/after runs use identical helper
bytes, finite workload, viewport, quiescence and two supported GCs. Only the
two intended production source files differ in the actual remote manifest.

| Equivalent inbox checkpoint | Old open bodies | Fixed open bodies | Old nodes / listeners | Fixed nodes / listeners |
| --- | ---: | ---: | ---: | ---: |
| Completed control | 0 | 0 | 1602 / 87 | 1602 / 87 |
| Pending view unmounted 1 | 1 | 0 | 1823 / 120 | 1602 / 87 |
| Pending view unmounted 2 | 2 | 0 | 2044 / 153 | 1602 / 87 |
| Pending view unmounted 3 | 3 | 0 | 2265 / 186 | 1602 / 87 |

The measured finite criterion is zero obsolete bodies and zero additional
post-GC nodes/listeners after each pending unmount. The old source ends with
three bodies, 663 additional nodes and 99 additional listeners. Fixed-body
closures are observed before page or fixture teardown. The original minimized
one-cycle driver, with its original helper bytes, also passes on the fix.

Heap and native memory are separate populations. Completed→pending3 used heap
is 3,187,988→3,293,872 bytes old and 3,189,320→3,249,436 fixed. Renderer RSS
highwater is 179,441,664 old / 180,146,176 fixed; PSS is 125,879,296 /
126,412,800. Native noise/highwater is slightly higher in the fixed run; this
is not a native-memory reduction, whole-tab ceiling, unlimited plateau,
heap retaining-root proof or attribution of the reported multi-GB incident.

An attempted old eight-cycle run failed ordinary-network quiescence at the
fourth pending cycle. Its partial measurements and cleanup remain retained as
a failed probe, rather than a completed comparison. Both matched runs were
subsequently defined and executed with three pending cycles.

## Verification and review

All application/build/test/browser execution used the serialized x-boss lane,
two jobs/threads, synthetic HOME/XDG/data and private physical TMPDIR.

- Focused API/thumbnail tests: 44 passes; full canonical UI: 650 passes, zero
  failures/skips/cancellations. The established baseline is 646 passes.
- Narrow Go embedding/CSP package: two named tests and the package pass.
  The initial malformed `-p2` command failed setup; corrected `-p 2` passes.
- Existing real-browser attachment check passes PNG/SVG security/rasterization,
  viewer/focus, paste/drop, retries, hosted/reference-only and draft behavior.
  All five generated screenshots were inspected. Its retaining observers are
  used only for functional checks, never memory evidence.
- Actual served asset hashes, executed helper archives and remote/local source
  manifests agree. Final diagnostic runs each drain seven known PID identities,
  three listeners, proxy resources and temporary data/build directories.
- Independent diagnostic review: Spec/Quality PASS, no findings. Task Spec/Quality and fresh whole-change Spec/Quality reviews also PASS;
  no findings remain. See the [final reports](2026-11-16-webui-thumbnail-cancellation-review.md).

A repeatable regression command from a checkout with build-box tooling:

```sh
BB_HOST=x-boss BB_JOBS=2 just bb GOMAXPROCS=2 node --test --test-concurrency=2 internal/ui/jstest/api.test.mjs internal/ui/jstest/thumbs.test.mjs
```

GitHub #60 remains open for the wider memory incident and remaining thumbnail/
installed acceptance. This source component does not import the historical
thumbnail branch or claim a new installed release/deployment. Full raw logs,
failed attempts, exact helpers, receipts and reviews are retained outside the
worktree under `.scratch/vecnet/issue-loop/2026-11-15/issue-60-memory-evidence/`
after verified archival and before cleanup.
