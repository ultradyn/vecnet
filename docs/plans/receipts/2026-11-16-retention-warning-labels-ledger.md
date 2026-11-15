Plan: issue-51-retention-risk-labels (BASE 9dc1730c9b98bf4b62d1536f87ec1cd8e9cdccbf)
Mode: Durable-state campaign (1.1 row 5); one bounded task in the existing serial GitHub campaign.
R1 Ruling: User explicitly requires implementation subagent + independent review; use Sol/high implementer, fresh Sol/high task reviewer then fresh whole-change reviewer before merge. All accepted findings, including minors, fixed per user instruction.
R2 Ruling: Existing full synthetic Settings browser journey is the runnable acceptance surface; no new installed release or activation is required for this label slice. Cost if wrong: release follow-up only.
R3 Ruling: Use label-each-note option from GitHub51, retaining separate risks, rather than grouping risks; least change to validated confirmation lifecycle. Cost if wrong: wording revision.
Harness: headless codex exec exact model gpt-6.1-sol/high via x-boss launch helper; controller resumes fixes and owns integration. Artifacts on shared filesystem; no child workers. Gate serializes isolated build-box runs.
Pre-flight: no shared interfaces between tasks (single task).
T1 self: Clean. Browser assertions precede source fix, existing transaction remains authoritative, scope matches GitHub selection.
Stage-0: source and synthetic journey available; no unverified external dependency needed.
Task 1: pending baseline, then dispatch.
Task 1: dispatched (BASE 9dc1730c9b98bf4b62d1536f87ec1cd8e9cdccbf, gpt-6.1-sol/high, agent impl51-labels, expect 45m). Baseline 641 passed/0 skipped; exit0.
Task 1: DONE at 3338187049a6dfca5445d219b48200c32747b1f9 (suite646passed/0skipped,10GoUI,106PNG full Settings). Controller verified2252sourceentries stable through all gates and green source equals committed bytes. RED browser and focusedNode both genuine on unchanged production source. Review dispatched gpt-6.1-sol/high rev51-labels, expect15m.
Task 1 review (gpt-6.1-sol/high): Spec PASS; quality PASS; C0/I0/M0. Review focus ET1..ET3 pinned and10actualimages inspected.
R4 Ruling: Existing Node MockTimers ExperimentalWarning preserved from baseline; no application failure or new finding, no unrelated runtime/test-framework change.
Task 1: complete (commits 9dc1730c..33381870, allacceptedfindingsfixed,0parked).
Metrics Task 1: model_impl=gpt-6.1-sol model_rev=gpt-6.1-sol wall=12 dispatches=2 fix_rounds=0 findings=C0/I0/M0 tests_added=5 suite=646/0 diff_lines=+55/-4 blocked=0 stuck_kills=0
Whole-change review dispatched (gpt-6.1-sol/high final-rev51-labels; BASE9dc1730c..f407edaab3ea8ba25686c4d6130ca2b966ac4846); taskreviewPASS, source unchanged.
Package filename normalization: initial HEAD alias produced review..HEAD.diff; exact pinned SHA package generated immediately before review input consumption. Identical diff, no source/test rerun needed. Helper now resolves refs before naming.
Whole-change review (gpt-6.1-sol/high): Spec PASS; Quality PASS; C0/I0/M0. Full9dc1730c..f407edaa reviewed, allsource/receipt claims and10actualPNGstates checked. No accepted finding deferred.
Metrics Plan labels: tasks=1 agents=3 tokens=n/a final_findings=C0/I0/M0 final_fix_passes=0 rerev_regressions=0 deferred_minors=0 parked=0 merge=pendingff deploy=n/a(sourceonly)
