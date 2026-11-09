# Route statistics A3 — Events capture and telemetry isolation

Component ready for review; master integration, read/API/CLI wiring, whole-feature gates, old-binary compatibility, rollout backup and deployment remain separate.

The Events worker captures an invocation-local observation only for actual payload HTTP Do. It freezes exact request byte length, status, monotonic elapsed through the existing bounded drain and completion time before authoritative settlement. Invoked record and EndDispatch operations return before the independent NoteRouteAttempt call, including errors and cancellation. Telemetry cannot change retry, pause, counters, dispatch or replay authority.

Test-first x-boss evidence at executable source SHA worker621da661, Events fixture d6131d72 and Hub fixture f07cb362:

| Gate | Actual result | Raw SHA-256 |
| --- | --- | --- |
| Initial RED | 30 missing Events-capture failing leaves,20 passing controls; exit1/bb1, zero skips | a33973f15ef3929be4f48cc0821f3cb2395e3c63781799dc098d93aa0a53fb0e |
| Focused GREEN |60 passing test nodes,zero failures/skips; exit0/bb0|ca6674cb653a27020d2ea6bca801b92ffef6f38ba00c02671ef1165927412786|
| Full Events |141 passing test nodes,zero failures/skips; exit0/bb0|2fbec29226ee5db7e9e3c0897d91ce8ef488003127a2702ae3c1a6144fc795d0|
| Full Hub |816 passing test nodes,zero failures/skips; exit0/bb0|760561deb20d6bd2b3f16e6d84e04e7e4af574e37b9cb542cd53ebda48c6a878|

Parent verified the complete2131-entry source manifest and raw hash plus two empty exact-owned process drains after each gate. The initial eight fault fixtures stopped at missing observation, so later rollback/replay assertions were only established by GREEN. Real modernc WAL writer-lock trials retain busy_timeout5000 and the1250ms elapsed oracle; both actual initial trials completed near1.0006s, with connection-release/next-writer and finite50ms no-later-retry controls. This is environment-specific evidence, not a universal one-second SLA.

The SAME independent GPT-6.1-Sol/high fixture and production reviews both returned Spec PASS/Quality PASS,C0/I0/M0. The production reviewer traced actual record/EndDispatch transactions and reachMu release before Note, no Step reentry or mutable Worker measurement state, and exact17-line candidate transplant. Only internal/events/worker.go changes production;600-line Events and433-line Hub fixtures add coverage. No native launcher ancestry, live actor, migration renumbering or deployment is included.
