# Automatic Codex delivery acceptance receipt

Status: INCOMPLETE. Product source and build-box correction evidence verified; remaining gates and actual live acceptance listed below. No master merge, deployment, or release is claimed.

Source: feature `80465490f42fa6b4762a04007eb2245303f7f377`, integrating corrected child `55ffe9cad0c35504ed6cc4cb7451cd0fe4c57bb2` with a documentation-only plan delta. Origin/master, gh/master, and Fly remain `1b189f1`.

All builds and tests used x-boss with two jobs and physical temporary paths. The original seven CLI baseline failures were path-alias fixture failures; the physical-path correction removes them rather than accepting a failing baseline. Standard Go tests use umask022; restrictive private restoration fixtures retain umask077.

| Gate | Actual tested revision | Result | Raw SHA256 |
| --- | --- | --- | --- |
| go | `a786a23` | 5515 pass / 1 skip / 0 fail; bb exit0 | `1e6cfef9b78be2e6082825a12c1145874e3e516c464bc579322b695f00911d0e` |
| ln | `55ffe9c` | 1927 pass / 0 skip / 0 fail; bb exit0 | `fc51947e9b0d08ba7df0915644b666c48baa0ca1cff61e2d871f526096744a1d` |
| e2e-clean | `55ffe9c` | 44 pass / 0 skip / 0 fail; bb exit0 | `a3ca3d30f571193697ed655bb40f13d3b5b91ca467eba40c4139cf37e5b53f4a` |
| e2e-stale | `55ffe9c` | 44 pass / 0 skip / 0 fail; bb exit0 | `2ba9106dee8ea3342cc6b501aed238dfee915e752d509a616a20f5fb95218f54` |
| harness | `a786a23` | 115 pass / 0 skip / 0 fail; bb exit0 | `7f13f9786b6caf0f56429ed0af18a2f12a94658c915b8f8e7431a5e92b51973e` |

The Go/harness executions retain their original a786a23 revision. The only later source difference is the test observer busy_timeout insertion; all other frozen source bytes match. The controller verified all 2,018 local/committed source files and 1,728 remote before/after checks. Current clean and stale E2E runs each exercise the standard recipe without manually building generation B. Original native source artifacts were copied read-only from the verified x-boss output and checked against recorded hashes.

Independent correction reviews: the harness-source and test seats close their source/build-box findings with C0/I0/M0. Their approval retains operational and integration obligations. The owner/source seat also closes FI1/FI2/FI3/FM1 (4/4), C0/I0/M0. All three correction evidence seats retain actual operational and integration obligations.

Dot onboarding: integrated `2b703ee` adds a short visible prompt after the connection instructions and a primary **Copy prompt** button using the existing clipboard component. The prompt excludes the link code. Its focused UI/browser/mobile evidence was checked before integration. Final full UI/screenshots remain pending.

Known evidence limits and retained failures:

- Initial FI4 RED failed during socket setup and was invalid as a regression proof. The later immutable original-recipe reproduction is explicitly AFTER code: 43 pass/1 missing-native-B assertion, bb exit1. This establishes sensitivity without claiming test-first chronology.
- Long socket fixtures, runner umask, plaintext stale-binary setup and SQLite observer contention failures are retained. Final corrected runs are the counts above.
- Expected cancellation/proxy ERROR/WARN output, the earlier supervisor-warning obligations, and the SQLite experimental warning are disclosed. Fixture capture/assert cleanup is deferred; later quiet output does not erase historical records.
- An early historical orphan fixture lost its PID before root removal. Scoped inventory found no current matching writer; that does not reconstruct historical cleanup ordering.
- Exactly-once evidence concerns the supported shared claim/ledger and recovery paths. Bounded observations do not prove model attention or absence for all future time. SIGKILL/kernel/filesystem failure, uninterruptible I/O, and lost supervision retain explicit ambiguity. FIFO/TTY notices are best effort; regular-file sink latency has no hard bound.

Remaining before ready for review / master:

- Private preparation actual optimized refusal controls, observer controls, full matrix, scoped review, concrete staged artifacts/manifest, and rehearsal.
- Final UI406/0, protocol, fresh npm8/0, and full screenshot gates are complete (326 PNGs); desktop and mobile Dot screenshots were viewed.
- Direct Max approval of the concrete xsm account/unit/path/thread/rollback plan; actual Fly/dev live acceptance and owned teardown.
- Final independent evidence approval, final receipt/ledger closure, merge/push both remotes, main-derived install/deploy, persistent Fly daemon dogfood, and v0.2.0 release verification.

Further final gates at feature8046549:

- ui: bb exit0; raw SHA256 `71715eff50180bb8704e8de41b54fddff204ca62d4f78ee9c97e77bfb1ad6952`.
- protocol: bb exit0; raw SHA256 `c6a0e3df96c90748fb7a843b756c0ffa3f6a1b5ef75cb76c3b8d4b9060c7e9ea`.
- npm: bb exit1; raw SHA256 `cfe021c9ec83b21938182df4acbb2db74bd38ba0b14a78c4c52241ffc98b0d0d`.
- npm-pack-check: bb exit0; raw SHA256 `12bcdcc63d2e6113fac1d688ea518f4ba785efc725f8a743e0bd69ac5b6f444d`.

UI406/0 and protocol validation passed. Initial npm-check used an obsolete generated tarball and failed7/1. After the documented npm-pack preparation, the unchanged strict package suite passed8/0 including extracted delivery-entry execution. The stale-artifact failure remains preserved. Full screenshots passed: 326 generated PNGs, complete index, bb exit0. Controller viewed inv-12-dot.png and inv-13-dot-narrow.png; Copy prompt is visible at both widths, with the prompt in the scrollable dialog body.

Private helper execution: optimized and inherited original-implementation controls each failed at exactly one unauthorized synthetic config read (expectedbb1); focused observer3/0, full258/0bb0. All actual counters/frozen helper hashes were independently checked. R60 changes only the external-resource grammar loop; every fixed-manifest equality, collision, no-follow, credential and metadata guard remains. Independent R60 private source/synthetic review PASS C0/I0/M0. Subsequent actual staging refused an export-function comment in the private import scanner before publishing artifacts. R63 stages the bounded parser correction and two realistic regressions; its additional 264-case test/review remains pending. No actual config content was read or copied.

Final full-shots raw SHA256 `22947d522c3dab17b9afa60c75913ca3363c35b0883d01b48cc626c7421b14b6`. Generated Dot desktop/mobile screenshots are in `docs/plans/shots/oui/inv-12-dot.png` and `inv-13-dot-narrow.png`.
