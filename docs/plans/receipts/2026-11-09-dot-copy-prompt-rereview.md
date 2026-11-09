Spec PASS
Quality PASS

I1 ADDRESSED — internal/ui/static/invite.js:74, :678 and :689.
The Dot Copy prompt action now supplies its own feedback target to copier.
The initially empty role=status, aria-live=polite, aria-atomic=true region is
inserted into the Dot body inside the dialog, not into the inert page outside
it. On copy failure, the exact manual-selection guidance is inserted there
through the text-only h helper and scrolled into view. This fixes the source
cause identified in source-sol-review.md I1; raising a page toast's z-index
is no longer involved in this action's failure path.

Regressions: none found in this scoped change.
No known blocker/major issue found in this pass.

Checked:
- Read i1-rereview-brief.md first, then i1-fix-review.diff, clipboard-tests-brief.md,
  clipboard-production-brief.md and the corresponding reports. Reports were
  treated as claims; conclusions below come from the actual relevant source.
- internal/ui/static/invite.js:69 through :77: the optional target is cleared
  and the button label reset at the start of each Dot copy attempt. A false
  copyText result inserts guidance; only a true result takes the Copied branch.
  A subsequent successful retry starts by clearing stale failure guidance.
- internal/ui/static/invite.js:676 through :690: unchanged visible prompt is
  the source of copied text via prompt.textContent. Neither the status nor the
  prompt interpolates invitation material, URL, code, token or peer data.
- internal/ui/static/dom.js:10 through :24 and :100 through :116: feedback
  becomes textContent, not markup. Existing Clipboard API rejection falls
  through to execCommand, whose false/throw result returns false after textarea
  cleanup and the existing focus restoration. No clipboard mechanism changed.
- internal/ui/static/invite.js:139, :678, :690 and :824: the status is a real
  descendant of the modal dialog and exists before the failure text is added.
  It has no hidden/inert attributes and uses the existing inv-note styling.
- internal/ui/static/invite.css:22 and :42 through :44: failure guidance follows
  the action row in the scrolling body; scrollIntoView uses block=nearest.
  The empty status has no note padding/border. Prompt wrapping and selection
  styling, native button keyboard activation and focus styling are unchanged.
- internal/ui/static/invite.js:325, :675 and :696: other copier calls omit the
  optional target and retain their prior shared toast and success behavior.
- internal/ui/static/invite.js:636 through :643, :695 through :764 and :807
  through :824: link-code slots, bearer confirmation, close/abort and dialog
  lifecycle are unchanged. Copy completions retain captured local nodes rather
  than querying a later dialog; detached feedback cannot alter a new modal.
  The change adds no onboarding request, global status write or focus call.
- scripts/ui/invite.mjs:68 through :83: the actual regression saves own method
  descriptors, rejects navigator.clipboard.writeText, makes execCommand return
  false, counts both calls, and restores after partial override setup failure.
  Unsupported fixture capabilities explicitly fail with NEEDS_CONTEXT.
- scripts/ui/invite.mjs:84 through :103: clicks the actual Copy prompt button;
  requires exact guidance in an in-dialog status/alert, checks visible bounds
  and hit testing, requires one call to each clipboard mechanism and no Copied
  action label, and checks unchanged, focusable, selectable prompt text.
  finally restores original own descriptors or removes temporary own methods
  to reveal inherited originals, then disposes the handle even on assertions.
- scripts/ui/invite.mjs:104 through :107: after restoration, retry requires
  Copied and actual clipboard equality with the same displayed prompt, then
  waits for the original button label. Desktop/mobile calls are at :295/:422.
  The supplied diff preserves the existing journey assertions and screenshots.

Remaining limits:
- Source review only: no imports, application/test execution, browsers, network
  or other agents. Parent-reported RED was not independently executed here.
- Current GREEN, full UI/Go/screenshots, actual desktop/mobile geometry and
  clipping, focus under native permission prompts, and manual assistive
  technology announcements remain parent-owned and unverified by this review.
- Source and regression inspection do not prove real ChatGPT task execution,
  subscription acceptance or native Codex acceptance. Existing asynchronous
  clipboard completion timing is not established by this source-only pass.

=== DONE ===
