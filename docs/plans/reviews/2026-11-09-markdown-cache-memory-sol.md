Spec PASS; Quality PASS; C/I/M: 0/0/0 (Critical/Important/Minor).
Fresh whole-component source verdict for WEB-MEM-4 only. No new findings.
Original I1 is addressed by the corrected oracle; production has no identified defect.

Scope and identity:
- Read final-component-review-brief.md FIRST, the canonical plan and receipt,
  the entire supplied five-path final-component-review.diff, its pins, and both
  source-sol-review.md and fix1-sol-review.md. Prior verdicts were context, not proof.
- Reviewed complete current md.js (62 lines), md-cache-lifetime.test.mjs (238 lines),
  both canonical documents and the changed CHANGELOG.md entry.
- Named external seams checked only for concrete cache/rendering risks:
  md-model.js parse/data/link behavior; dom.js h(); format.js clean();
  md-dom.test.mjs strict DOM, host disclosure and inert-markup controls.
- All five current files matched final-component-review-pins.json on initial
  hashing and final readback. Executable identities:
  md.js 41b1946769fc67df88e4b8744660496d5210711561640944bd93f199408e9c90
  test 4725f922b9abc3f0016f04d6c9a97d5d9f209934b645e9034f45f57fe0ec61a2
- The declared base is 3d06953f. Used the supplied delta; did not regenerate
  diffs or inspect history, index, refs, commits, parent or sibling checkouts.

ReviewFocus pins, quoted verbatim:
docs/plans/2026-11-09-markdown-cache-memory.md:14:
> One admission requiring multiple FIFO evictions must remove every required victim before re-admission can mask it. Pinned by WEB-MEM-4 one admission can evict multiple old keys and reclaim their charges; parent proved the corrected oracle rejects single-eviction and double-subtraction mutants.

docs/plans/2026-11-09-markdown-cache-memory.md:15:
> Budget bypass must render the complete oversized input safely while preserving earlier cached keys. Pinned by the oversized normalized-input regression and existing parser/DOM security controls.

First pin — internal/ui/jstest/md-cache-lifetime.test.mjs:95:
- Scenario: four 32768-unit old keys plus a 196608-unit admission total 327680.
  Meeting 262144 requires removing old[0] AND old[1] before any victim reload.
- Effect of the earlier oracle: probing old[0] first could itself evict old[1],
  masking both a single-eviction loop and double subtraction of a victim's charge.
- Fix-check: :100 probes the three required survivors before victim admissions;
  :106 probes old[1] FIRST and :108 requires its parse count to be 2.
  Correct source removes both victims. Either named mutant retains old[1], so
  that first probe sees 1 and fails before re-admission can hide the defect.
- Independent static trace supports I1 closure. Supplied six-case disposition
  records old/corrected outcomes 0/0, 0/1, 0/1 for correct/single/double variants,
  with matching executable hashes and accepted raw e439bb4494950666443f096d51f0896212c400637e2b5acb4272b1c9b5bceafc.
  These outcomes were not rerun here.

Second pin — internal/ui/jstest/md-cache-lifetime.test.mjs:112:
- Scenario: a String-converted object and its primitive key each supply 262145
  units, with real Markdown before MD_LIMIT and a hostile literal plain tail.
- Effect/check: :123 checks the heading, :124 emphasis, :126 actual link targets,
  :128 safe link attributes, :131 absence of script/image elements, :134 the
  complete tail and :135 complete expected text. :137 requires two real parses.
  The earlier sentinel still has count 1 at :139.
- internal/ui/static/md.js:17 parses the complete key; :18 returns oversized
  parsed data before ANY Map/accounting mutation. Thus bypass cannot displace
  earlier entries, including when the existing cache is already full.

Checked/fine:
- internal/ui/static/md.js:14 preserves null/undefined-to-empty and String
  normalization, charging primitive key.length before parser CRLF folding at
  md-model.js:39. Raw CRLF/LF keys remain distinct; astral characters cost two
  UTF-16 units. Tests :178, :194 and :204 exercise these distinctions.
- md.js:15 hits neither charge nor promote. md-model.js:47 returns arrays,
  including truthy [] for empty input, so the existing truthiness lookup is sound.
- md.js:19 admits a miss once, :20 adds its exact charge, and :21 repeatedly
  removes the oldest insertion-order key while either ceiling is exceeded.
  :23 subtracts the exact victim length, :24 deletes that same victim.
- The loop maintains memoUnits equal to the sum of admitted key lengths.
  Before admission it is at most 262144; the new admitted key is no larger,
  so the temporary sum is an exact small integer. A fitting key can remain
  alone, guaranteeing termination before an empty Map could supply undefined.
  A zero-length victim still advances deletion and satisfies the entry ceiling.
- Tests :68, :81 and :159 check exact fit, next-unit eviction, aggregate admission
  below 1000 entries, no hit promotion and no repeated-hit charge. :222 checks
  exactly 1000 entries, an empty oldest key and FIFO after eviction/re-admission.
- md.js:32 renders fresh nodes from reusable data. Its block/span paths only
  read the cached arrays/objects. :142 tests disjoint descendant identities,
  mutation independence and the unchanged link target on a third cached render.
- md-model.js:41 retains the full plain rest; md.js:44 sends it through text.
  dom.js:10 allocates each element; :15 and :22 preserve clean() sanitization
  from format.js:130. Full rendering retains existing control-character cleanup.
- md.js:57 preserves link target/title/rel and image-as-link behavior; :58 retains
  host disclosure. md-model.js:21 retains the scheme allowlist. Existing controls
  md-dom.test.mjs:28 and :42 cover disclosure and literal hostile markup.
- Test :7 reads production md.js; :48 rewrites only its two import specifiers,
  with exactly-one assertions at :55. The wrapper at :42 observes each actual
  parse call and delegates to real parse; h() comes from the real dom.js.
  No copied cache algorithm, mock cache, replacement parser or public counters.
- Unique wrapper/module URLs isolate each case. :66 disables case concurrency;
  :60 restores the prior document descriptor and removes the temporary hook.
  El is a strict document shim, so these are source/DOM-structure controls.
- Production adds only a private budget/counter, bypass and repeated eviction.
  No TTL, LRU, configuration, dependency, truncation or introspection API appears.

Documentation, deviations and evidence:
- Plan :8, receipt :17 and CHANGELOG.md:3 agree on logical UTF-16 key retention,
  preserving complete rendering. They do not claim AST/native/tab/RSS bounds.
  Plan :4/:26 and receipt :17 explicitly leave final integration/release pending.
- R-M41 at plan :28 matches the implementation. R-M42 has a recorded report-only
  footer normalization with before/after hashes; no executable effect appears
  in the supplied component delta. It is not additional coverage evidence.
- I1 was a test-oracle defect, not another production defect. The receipt :13
  explicitly retains the initial missing-import fixture failure as INVALID.
  The stated transitive fixture additions are parent provenance, not code changes
  or independently executed proof from this review.
- Stored RED receipt lists seven named retention failures and sixteen passes.
  Final focused/full-UI/Go receipts list 23/488/10 passes, zero listed failures,
  actual/bb exits 0/0, equal 2151-entry before/after manifests and current
  executable hashes. Stored focused/full-UI summaries also say fail/skip/cancel 0.
  Each final gate has two stored empty drain records. These records agree with
  the canonical receipt; source inspection cannot independently establish runs.

Regressions:
None identified in accounting, FIFO, normalization, complete rendering, inert
markup, links, cached-data reuse, DOM freshness, instrumentation or scoped docs.

CannotVerify:
ZERO test/build/browser executors or subagents. No module imports, Node/Go,
SSH/network, profiling, messaging, ccc/c2c/attn/tmux, private/home scans or git
mutations. Only bounded source/evidence reads, hashes and this report were used.
Actual remote execution, isolation, live process cleanup, provider/session identity,
raw-log authenticity and full current-tree identity remain parent-owned checks.
Source/manifests and stored summaries are evidence records, not a reviewer rerun.
A contradictory raw result, fixture failure substituted for an expected assertion,
executable-pin mismatch or omitted required victim would change acceptance; the
parent must resolve such evidence conflicts from original receipts/logs and drains.
No browser/native/physical-memory, timing, deployed-asset, merge or issue #60
closure verdict is given. This is the logical key-budget component source verdict.
Only this report was written; production, tests and documentation were not edited.
=== DONE ===
