# Dot clipboard I1 test-first fix

# Dot clipboard I1 — tests-only handoff

Status: DONE with authorized test editing; execution remains parent-owned.
Read the dated plan and source-sol-review.md, including I1 verbatim.
The newer clipboard-tests brief explicitly authorizes this regression addition.

Diff: scripts/ui/invite.mjs, +43 / -0 lines.
- One reusable async helper, assertDotCopyFailure, at line 68.
- Called after existing desktop Dot clipboard checks and after the existing
  mobile Dot screenshot at 390 x 844.
- Every existing assertion, screenshot and journey statement is preserved.
- No imports, dependencies, separate framework or production edits.

Regression behavior:
- Uses actual Copy prompt, visible #inv-dot-prompt and existing fixture grants.
- Rejects navigator.clipboard.writeText with synthetic NotAllowedError;
  document.execCommand returns false. Both attempts are counted.
- Requires the exact existing manual-copy guidance in a role=status/alert
  inside the active dialog, with visible bounds, accessibility and hit testing.
- Requires Copy prompt to remain named correctly, without false Copied feedback.
- Checks unchanged prompt text, focusability and actual DOM text selection;
  rejects fixture code/URL, peer/token markers and synthetic routine secrets.
- Restores original own descriptors, or deletes temporary own methods to
  expose their original inherited methods, in finally even on assertion failure.
- Partial override setup also restores before rethrowing; no global test fields.
- Retries with restored mechanisms, checks normal Copied and clipboard equality,
  then waits for the original Copy prompt label using bounded locator waits.
- Scopes Copied checks to existing .inv-acts so transient Copy URL/Copy code
  success labels cannot create a false failure.
- Adds no sleeps, polling loops or request mocks.

Expected RED on frozen source:
- Desktop helper reaches the four-second feedback.waitFor at line 88.
- Current copier sends guidance only to body-level #toast, so the dialog-scoped
  role locator remains absent. Expected result is that timeout, followed by
  clipboard restoration; this is a source-based prediction, not an observed RED.
- A fixture lacking secure clipboard/fallback explicitly reports NEEDS_CONTEXT;
  no fallback test weakening or production workaround is introduced.

Self-review: traced real copier/copyText, error text, existing dialog structure,
clipboard grants, desktop/mobile insertion points, cleanup and retry paths.
git diff --check passed (whitespace inspection only).
No tests, imports, Node/Go, builds, browsers or application execution performed.
No workers, network, SSH, c2c, staging, commits or pushes performed.

SHA-256 of edited test:
cfda7c64c74c0be64784a378fc2ad80ad34a07d1fbb3f22c96b1a3db3e2e528d

Production hashes match before/after this edit:
- internal/ui/static/invite.js:
  9ac7d5a138c63308697d43bf8f961a3db9f44f84a8d95a338a0cc9cb6e6f74d1
- internal/ui/static/dom.js:
  8aedfc4f05ec06696b1e08fd87b73751d07c88f8838b816c1c0dc21d32f22c24
- internal/ui/static/invite.css:
  7be091cceafd3ab11ca73df570715b4708aad1376e508602596d994430ca65d1
- internal/ui/static/index.html:
  aba2438149e81f2e2af5188cd24a78325ba9dbbad13492c160ccf70a57f7c747

This report is the last write. Parent must observe RED before production release.

=== DONE ===


# Dot clipboard I1 — production handoff

Status: DONE with bounded production edit; all GREEN validation UNRUN here.
Only internal/ui/static/invite.js was edited, plus this final report.
Diff size: +9 / -3 lines (12 changed product lines, within the <=15 target).

Exact patch hunks:
```diff
-function copier(label, value, cls) {
+function copier(label, value, cls, feedback) {
   const b = h('button', { type: 'button', class: 'btn' + (cls ? ' ' + cls : ''), text: label, onclick: async () => {
     const text = typeof value === 'function' ? value() : value;
+    if (feedback) { feedback.replaceChildren(); b.textContent = label; }
     if (await copyText(text)) { b.textContent = 'Copied'; setTimeout(() => { b.textContent = label; }, 1500); }
-    else toast('Could not copy. Select the text and copy it instead.', true);
+    else if (feedback) {
+      feedback.append(h('p', { class: 'inv-note', text: 'Could not copy. Select the text and copy it instead.' }));
+      feedback.scrollIntoView({ block: 'nearest' });
+    } else toast('Could not copy. Select the text and copy it instead.', true);

+    const feedback = h('div', { role: 'status', 'aria-live': 'polite', 'aria-atomic': 'true' });
     lede.textContent = 'Connect ' + (d.name || 'the dot') + ' from ChatGPT.';

-      h('div', { class: 'inv-acts' }, h('button', { type: 'button', class: 'btn', text: 'Done', onclick: () => close() }), copier('Copy prompt', () => prompt.textContent, 'pri')),
+      h('div', { class: 'inv-acts' }, h('button', { type: 'button', class: 'btn', text: 'Done', onclick: () => close() }), copier('Copy prompt', () => prompt.textContent, 'pri', feedback)),
+      feedback,
```

Self-review:
- Only Dot Copy prompt supplies the optional feedback target; all other copier
  call sites retain their existing toast, success label and clipboard behavior.
- The initially empty live status remains inside the active Dot modal. Failure
  adds the exact existing guidance through text-only h and existing inv-note.
- Status follows the sticky action row so scrolling it into view need not put
  the message beneath that row. Actual desktop/mobile geometry is UNRUN.
- Empty status adds no note border/padding; note styling exists only on failure.
- Every Dot copy attempt clears stale guidance and resets Copy prompt before
  awaiting copyText, including attempts during the prior Copied label interval.
- Copied is set only when the existing copyText returns success.
- Prompt text, textContent copy source, focusability/selection, native button,
  clipboard mechanisms, URL/code copiers and onboarding/OAuth/recovery stay intact.
- No dom.js, styles, templates, global toast, test, dependency or debug API edits.
- git diff --check passed; this was whitespace inspection, not execution.

Parent RED attribution (clipboard-production-brief.md, not executed here):
- Parent reports genuine missing in-modal status/alert Timeout4000 in
  assertDotCopyFailure, actual exit 1 / build-box exit 1, all 2110 sources current,
  two empty drains verified, raw SHA-256:
  d0691756811d98315fdc24f0f301ad4c65439317f3f2389b675da8106ae08013
- That RED is not a setup failure. Earlier Go exit 255 remains interrupted,
  not PASS. Neither historical evidence is a current GREEN claim.

SHA-256:
- invite.js before:
  9ac7d5a138c63308697d43bf8f961a3db9f44f84a8d95a338a0cc9cb6e6f74d1
- invite.js after:
  5e96ee05d2e77833b48ff6519aa923d774346b41e91806506811f61fb1b501d2
- Frozen scripts/ui/invite.mjs, unchanged:
  cfda7c64c74c0be64784a378fc2ad80ad34a07d1fbb3f22c96b1a3db3e2e528d
- dom.js, unchanged:
  8aedfc4f05ec06696b1e08fd87b73751d07c88f8838b816c1c0dc21d32f22c24
- invite.css, unchanged:
  7be091cceafd3ab11ca73df570715b4708aad1376e508602596d994430ca65d1
- index.html, unchanged:
  aba2438149e81f2e2af5188cd24a78325ba9dbbad13492c160ccf70a57f7c747

Zero executors: no tests, browser, Node/import, Go, build, network, SSH, agents,
models, c2c, staging, commit or push. Parent owns frozen-test GREEN, full UI,
full Go, full screenshots, actual visual inspection and SAME independent review.
All those current-candidate gates and review remain UNRUN here.

This report is the last write. Stopping within the authorized production scope.

=== DONE ===
