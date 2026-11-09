Spec PASS
Quality PASS
C-I-M counts: C=0 / I=0 / M=0

Fresh independent SOURCEONLY review of PR52's current master-merged tree.
Reviewed HEAD: 53c5a3a2c9248444f7bc946775a68fb81754adb6.
Parents: PR52 75a0c2e26bd92a7bc0389b7815571e5d04a9e623 and
master 08d39a620c66e761055b7bab8ec289c360be799c.
This identifies the local merge-gate tree; it does not assert a subsequent master landing.
The supplied brief was read first. No earlier review reports were consulted.

Findings: no actionable correctness, spec, or maintainability defects found.
No fixes requested; the evidence below explains the PASS verdicts.

Merge scope and integrity:
- Relative to 08d39a62, exactly four files differ: screen_dash.go,
  screen_dash_test.go, and TestDashGolden-{140x42,80x24}.golden under internal/tui.
- Those four files are byte-identical to their PR52 parent 75a0c2e2 versions
  according to git diff. The master merge did not weaken their implementation,
  regression assertions, or golden expectations.
- No change to native actor policy, session handling, authentication, or runtime
  execution is part of this four-file delta. This verdict grants no such acceptance.

Source behavior and caller trace:
- internal/tui/screen_dash.go:43-46,81,189-197: showArchived starts false;
  peerRows removes only rows with non-nil ArchivedAt. Disabled peers with no
  archive timestamp remain visible. Clone before DeleteFunc preserves the
  shared peer slice and its order; reveal returns the original rows without edits.
- The real data path supplies archived rows: internal/opview/dash.go:100-120
  selects all peers, including archived_at, with no lifecycle exclusion;
  internal/httpapi/op.go:101-108 returns them, internal/opclient/client.go:204-209
  decodes them, and internal/tui/data.go:416-419 stores the complete slice.
- internal/tui/keys.go:41-66 routes unclaimed normal-mode `a` to the active
  dashboard; screen_dash.go:137-146 toggles locally, preserves a visible selected
  ID, selects the first visible peer when hiding its selected archived row,
  and clears selection when none remain. The toggle creates no API command.
- internal/tui/screen_dash.go:177-197,739-765: keyboard movement and rendered
  cursor use peerRows and the same ID-to-index lookup. The rendered window
  records exactly its displayed peer IDs in peerVis.
- internal/tui/layout.go:138-171 resets hits for each frame;
  screen_dash.go:415-429 builds peer hits from peerVis, and Click selects that ID.
  internal/tui/mouse.go:94-120 dispatches the recorded row hit. Wheel dispatch
  at mouse.go:75-80 reaches screen_dash.go:440-451 and the same filtered move.
  Thus hidden first/middle rows cannot shift the displayed row's mouse target.
- internal/tui/screen_dash.go:147-172 resolves peer Enter from the filtered list,
  guards an empty list before indexing, preserves the source/stopped-client
  guards, and narrows threads to the resolved peer ID. Thread Enter remains
  independently resolved against displayed thread data at lines 148-153.
- internal/tui/screen_dash.go:573-594,782-790 prioritizes the archived marker
  over disabled/revoked and mutes the archived name. Column fitting at 553-568
  retains the peer column; lifecycle text has reserved space within that cell.
  Lines 746-748 add the explicit `archived shown` pane label.
- internal/tui/screen_dash.go:342-365 still resolves names from all Data.Peers,
  preserving archived identities in thread, health, and audit history.
  threadPage/threadRows and narrowed history are not filtered by the peer toggle.
  An existing narrowing retains its own peer label at 795-803; repairing the
  peer cursor does not silently replace that historical thread query.

Actual test oracles inspected; none executed:
- internal/tui/screen_dash_test.go:236-260 checks initial archived absence,
  independently disabled visibility, reveal, exact frame dimensions at both
  140x42 and 80x24, and `archived` on the retired peer's own row, with `disabled`
  absent there. A title-only marker cannot satisfy that row-local assertion.
- Lines 263-329 place archived peers before and between visible peers; assert
  hidden-hit absence, j/k selection, wheel selection, mouse-selected Enter's
  participant query, selection preservation, hide-time repair, and no queries
  to either hidden ID. DeepEqual checks retained peer data/order after toggles.
- Lines 332-358 cover both an initially empty list and an all-archived list:
  hide, movement, and Enter leave no selected peer or narrowing, issue no hidden
  participant query, and retain an 80x24 frame.
- Query checks inspect recorded Participant values, not just selected labels
  (screen_dash_test.go:38-58). Mouse helpers submit coordinate events through
  Model.Update (internal/tui/mouse_test.go:210-229).
- The no-write checks at screen_dash_test.go:325-328 cover the seven methods
  listed at lines 25-26. They are not exhaustive API mutation instrumentation:
  fake_test.go:191 does not count PeerLifecycle. The toggle's absence of any
  write call is also directly established by the inspected source branch.
- Existing TestDashGolden still uses exact golden comparison at both sizes
  (screen_dash_test.go:183-199; golden_test.go:17-35). Only the status hint line
  changes in each golden. Existing layout/column assertions remain intact.
- These are regression tests added after implementation; no test-first history
  or independently reproduced RED/mutation outcome is claimed by this review.

Validation boundary: source and read-only Git inspection only. No tests, builds,
browser, syntax checker, network, SSH, runtime executor, delegation, or live
deployment checks were invoked. No RAM/native/live behavior is certified.
Full Go, screen, visual, merge, and deployment gates remain the parent's work.
Only this new source-review.md was written.

DONE

=== DONE ===
