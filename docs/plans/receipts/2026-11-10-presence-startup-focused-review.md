# Final focused Settings raster review

Vision: **UNVERIFIED for complete requested acceptance**; inspected editor/current-policy/flag/result/dialog regions PASS. The remaining gap is the ready-to-save phone action bar, not a demonstrated product failure.

Layout/source: **PASS retained** from the prior independent layout and helper reviews. No new material imagery concern required reopening production source.

Product findings: **C0 / I0 / M0**. E1 is substantially resolved, with the precise remaining evidence gap below. No polish or redesign task is proposed.

## Attribution and method

Read prefs-final-visual-review-brief.md FIRST, then my immutable prior E1 and helper-review report, followed by release-capture/receipt.json and the four before/after drain receipts. The prerequisite supplemental receipt exists and reports phase release-capture, status PASS, actual_exit 0, bb_exits [0], source_unchanged true, remote_source_after_verified true, entries 2126, and head 7b5659e666747a6c61e707aa2d723d8eb5565538. Both before-drain and both after-drain artifacts have empty rows for the recorded capture/cache roots. These are parent-supplied terminal/source/drain evidence, read here; I did not query remote processes or independently inventory all inputs.

Independently hashed the local release-capture/raw and obtained 93d3ca68a1b15a7ba205d75081ab9b4833751f31e72615c4813f6915ba0ab4f8, matching the receipt. The reviewed helper remains 4581081654e863da305adb12418cc645c99924b3b09a699322af7fe3079c08ce; canonical scripts/ui/prefs.mjs remains 6ab8139bd664a629196fb75fbe465cb31a2aafd5c4438a033e623003c976a2ad. These match my source-only helper review. No refs/index/head check or executing source comparison was performed.

Actually opened **all 24 requested individual PNGs at original detail using view_image**, rather than relying on source, assertions or contact sheets. Also opened six existing same-run phone captures to look for the remaining Save-bar evidence. These additional images are explicitly identified below. No rendering, executable image processing, crop generation, browser, test, build or application was run.

All screenshot names in this report are relative to:
`/home/xertrov/src/vecnet4/.worktrees/presence-startup-release/.superpowers/sdd/2026-11-10-presence-startup-release/release-capture/shots/`.

Desktop originals are 1440 × 900; phone originals are 390 × 844. The reviewed pixels belong to this supplemental capture run. The canonical 413 journeys/index/Go/UI/protocol results remain separate parent evidence; this supplemental run is not claimed to be that canonical run or a replacement for it.

## E1 disposition and exact visible checks

**Resolved — Grok draft editor and current-versus-draft distinction.** prefs-list-grok-draft-desk.png now shows the actual Grok row, edited badge, 10m input, Store override, Revert, inherited current 2h from presence.default_ttl (stored), and the separate unsaved bar with Undo and Save changes. prefs-list-grok-draft-phone.png shows the 10m editor, Store override, Revert, full current 2h/controller/source paragraph and staging/Save/reset explanation within the frame. Its saved toast overlaps the top header/badge region, but not those core editor/current-policy checks. prefs-policy-grok-draft-desk.png and prefs-policy-grok-draft-phone.png show the actual Grok drawer and its 10m control, Store override/Revert and committed inherited 2h paragraph. The desktop Policy frame also shows the complete unsaved bar and Save changes. The phone frames do not show the separate hub Save bar.

**Resolved — stored result and reset action.** All four prefs-{list,policy}-grok-stored-{desk,phone}.png originals now contain the actual 10m Grok editor, Reset to start-up value and current Grok 10 min controlled by presence.grokbot_ttl (stored). Desktop source badges are directly visible. Phone current-policy provenance is readable even where the transient toast overlaps the header badge. The List row's post-save green highlight is visible; it does not overlap controls or change the displayed value/controller.

**Resolved — completed RESET result.** All four prefs-{list,policy}-grok-reset-{desk,phone}.png originals now show raw 10m, Store override instead of the stored-row reset action, and current Grok 2h inherited from presence.default_ttl (stored). This visibly separates the raw Grok preference from its resolved current policy after RESET. These are post-save RESET results, not pending RESET screenshots.

**Resolved — Grok and default flag source/action geometry.** The four prefs-{list,policy}-ttl-flags-{desk,phone}.png frames show Grok 10m, Store override, starting origin --grok-ttl, stored-precedence explanation and current Grok 10 min controlled by G (flag, --grok-ttl). The four prefs-{list,policy}-ttl-flags-default-{desk,phone}.png frames show Default TTL 1h, Store override and starting origin VECNET_PRESENCE_TTL. The List default-phone image additionally contains the Grok flag row and its directly visible teal flag badge, 10m editor, Store override and full current-policy paragraph. The Policy default-phone frame contains the D drawer and current G policy below it. Inputs/actions stay within the 16px phone side margins; text wraps without meaningful horizontal clipping or collisions. No RESET action appears in these flag-result editor frames.

**Resolved — confirmation and confirm/save geometry at both widths.** All four prefs-{list,policy}-grok-confirm-{desk,phone}.png originals show the full modal, title, raw Now/After saving values, consequence copy, resolved Hub message, cancel/confirm semantics and both buttons. Desktop dialogs occupy approximately x440–1000 and y279–621. Phone dialogs occupy approximately x0–390 and y463–844; Cancel and Confirm and save both remain fully inside the raster. The raw table says 10 min → 10 min, while the separate Hub message visibly describes grokbot 2h0m0s → 10m0s. This is the reviewed equal-raw override scenario, not evidence that the confirmation is spurious. No modal text or action is covered by a toast.

**Unresolved — ready-to-save hub action at 390px.** Neither prefs-list-grok-draft-phone.png nor prefs-policy-grok-draft-phone.png includes the separate unsaved hub bar or its ready Save changes button. The desktop draft counterparts establish it at 1440px. Six additional same-run phone originals were checked to avoid requesting already-available evidence: the dirty/error frames do not contain that bar, while prefs-list-saving-phone.png and prefs-policy-saving-phone.png show the entire bar at the bottom with disabled Undo and Saving… during an in-flight save. Those two support phone bar geometry in the busy state, but do not show the longer ready Save changes label or its unsaved summary arrangement. The visible Confirm and save button belongs to the modal's later step and does not supply the missing ordinary Save-bar pixels.

**Smallest missing evidence:** one 390 × 844 ready, valid, unsaved hub-bar frame in List and one in Policy, with the separate Undo and Save changes buttons fully in frame. Names could be prefs-list-grok-draft-actions-phone.png and prefs-policy-grok-draft-actions-phone.png. These can complement the existing editor/current-policy frames; everything need not fit into one viewport. This is a framing request only, not a product/source/runtime change request. Until those pixels exist and are inspected, E1 remains narrowly OPEN and complete Vision acceptance remains UNVERIFIED.

## All 24 requested originals actually inspected and hashed

Each row below was individually viewed at original detail and independently hashed. The observations above are based on those actual rasters.

| Filename | SHA-256 |
| --- | --- |
| prefs-list-grok-confirm-desk.png | 818701888df2b443b90106011b7b204b4e91108924bfcd806ea279e97e7426e4 |
| prefs-list-grok-confirm-phone.png | f865eea970fcd56f23ed7c7393e6a186cb59746abc6cdc85ebdd70f627671331 |
| prefs-list-grok-draft-desk.png | 86ab2e7ce1c4f8ad67f52739b37093b84369819a5141ca89bd7e5efd27ebaa7a |
| prefs-list-grok-draft-phone.png | 20ad4e72c1c4562ef314384d63a28746f1846dfea51be1447ef884e0ca7b26a4 |
| prefs-list-grok-reset-desk.png | d432ea943cd98b947f409780cc8e4a60a8a85814b854387bbad45cf35eca8510 |
| prefs-list-grok-reset-phone.png | c8f5646dccabd62a0f4d77e53978002c0b40d19aa1d8908f63bcf5305a80946f |
| prefs-list-grok-stored-desk.png | a96870bda671824a6f63429b5b161ee96dc3f28c2bd5a654664ae21fae5e4b67 |
| prefs-list-grok-stored-phone.png | cee3e42518d8e2580f8cfea4784be62b7e6f81ca8c5ecebc0096d27ba02e959c |
| prefs-list-ttl-flags-desk.png | 335f0c4e02f2dd925ba739ff5ab67f866d127b50d3149fb2bc63bc522f76f24c |
| prefs-list-ttl-flags-phone.png | 0e17cab81a172d1612622dd786eb68817a9d77eed304fe83348f6f0d7d68e9bd |
| prefs-list-ttl-flags-default-desk.png | 3a2e26b96fba204f93736d94877ea33809f84ef0854179b2eb34d7544c95c5e4 |
| prefs-list-ttl-flags-default-phone.png | 449ac3ff6db3f2f5a7d457336a7b871ccf539fb4919386d24d90b2d45e1882ae |
| prefs-policy-grok-confirm-desk.png | 3a518e39a1908c9d43aac47018a23cafb376aed03283e6fafca7285fac4b5c5b |
| prefs-policy-grok-confirm-phone.png | 966d5c10e9faaaec8888f1bad99673ce0a6e2f4d491eeac546b80b7cce2c7e83 |
| prefs-policy-grok-draft-desk.png | 8d590c404272f3aa11ed9acd293b3b1bde9387249d8682fecd279db69554fca1 |
| prefs-policy-grok-draft-phone.png | 32d072200648d17af887c945759c045b2c65d9546ec3924bdd2f325bda289c25 |
| prefs-policy-grok-reset-desk.png | 94d0ab65b545fc2b6b38bf65c30c71aa848591400c7146742d3e950f4cfa54c3 |
| prefs-policy-grok-reset-phone.png | c7484244e88a9ff21bc0d34e35c59967d5b6aabd3db9ceb46e03e1738d1f5dd9 |
| prefs-policy-grok-stored-desk.png | 0008dba81b3d977a3e9a64b9afcb36121bee9361f2bfa5e77c26c15ba10653b9 |
| prefs-policy-grok-stored-phone.png | 8dc7516b2eafba6a63861f93a267aec57495f12e1b9c524ab3897b690b1e3b7a |
| prefs-policy-ttl-flags-desk.png | 2d788b151483102efd1a230199de0b956a81427c336579fe69d44bdb29ffb887 |
| prefs-policy-ttl-flags-phone.png | c665bf1ec1b70a3e3cad68f5519b93cf8df9ae8b91f7214ca288cfb5ed11c795 |
| prefs-policy-ttl-flags-default-desk.png | a7c1b2e7bf6520bf6b18591dd3439ddbed606f9ea699a0248226fdefb128d37f |
| prefs-policy-ttl-flags-default-phone.png | 728efb90cd64c639d970f50eefc2578501090ab90cb7fdb8871cbf9318ab9bfb |

## Six additional same-run phone originals actually inspected

| Filename | Visible result | SHA-256 |
| --- | --- | --- |
| prefs-list-dirty-phone.png | Edited Agents rows; ready hub bar outside frame. | 5a382f0d57121dfd15e91ed289919fa7dace32d10bc7406876da17f0f2b228dd |
| prefs-policy-dirty-phone.png | Edited policy/drawer; ready hub bar outside frame. | fae46f78fd4ef7ab761c896f5629cfa1d816623db2864dd34f1e5123cfffc847 |
| prefs-list-error-phone.png | Save-error notice and top Settings content; hub bar outside frame. | 42b38e292b3df7e880c43b2ba1127f3af8a8e0e149ba122740cc8d14ea08a2a7 |
| prefs-policy-error-phone.png | Save-error notice and retention lanes; hub bar outside frame. | d479e8b024e98cbdfc87295661148aba5dca4a1eb1ad2e5942ab071cb9e49ad0 |
| prefs-list-saving-phone.png | Complete busy bar, spinner, disabled Undo and Saving… fit at bottom. | 1313331981af4d849d68038689f0cfeada2f5276a89d85e2fb6efc24b82a650f |
| prefs-policy-saving-phone.png | Complete busy bar, spinner, disabled Undo and Saving… fit at bottom. | d676dbbceb5718531bb17bc50461eaae9897903c6ed1bfd7edc9d1fb995ed26f |

## Findings, checks and limits

No material clipping, overlap or horizontal overflow was established within the visible target editor/current-policy/action/dialog regions. Inputs, Store override and Reset actions fit the observed desktop and phone layouts. Current versus draft and inherited versus explicitly controlled values are readable in the named frames. Warning, flag, stored and edited treatments remain consistent with the existing UI where visible. Secondary metadata is subdued but readable at original inspection size; no numerical contrast certification is made.

Transient Saved toasts obscure parts of several phone row titles/source badges and some desktop background descriptions. Those obscured pixels are not accepted as independently inspected text. Their occurrence in these immediate post-action captures is a fidelity limit; the unobscured current-policy paragraphs and companion frames supply the stated controller/origin checks. No product redesign follows from this transient overlay. Pending RESET prose, unlike the completed RESET result, still lacks its own screenshot and is not newly visually certified.

A missing viewport region is an evidence gap, not proof of product clipping or an unreachable control. No product fix is requested for the two missing ready-bar frames. The prior geometry/source and helper PASS remain attributed to their immutable reports, with the current helper/canonical hashes confirmed above. The current image pass does not strengthen source review into new behavior evidence.

Static screenshots cannot certify interaction, keyboard/focus behavior, enabled-state transitions in use, scrolling reachability, timing, contrast measurements, garbage collection, live persistence or backend delivery behavior. The parent receipt/source/drain attribution is not an independent remote/process audit. No canonical result was rerun or inferred from this supplemental receipt.

Only this NEW report was written, LAST. Source, tests, styles, docs, old reports and all PNGs were preserved. No executable imports/parsers, compiler, Go/Node/SQL/tests/build/browser/network/SSH, private home/configuration/credentials, foreign sessions, refs/index/commits/push, delegates, communications/c2c or attention tools were used. One brace-expansion hash command was rejected by a local shell-classification hook before execution; hashing succeeded with explicit filenames and required no permission escalation.

=== DONE ===
