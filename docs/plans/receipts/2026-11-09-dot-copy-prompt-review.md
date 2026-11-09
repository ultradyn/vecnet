Spec FAIL
Quality FAIL

Scope: fresh independent source review of fbbf664f..c1e82eba as supplied in
source-review.diff, the dated plan, and the current relevant source. No prior
component review or historical execution result was used as acceptance evidence.

Findings (C/I/M = critical/important/minor):

I1 — Clipboard failure guidance is outside the active modal.
- Location: internal/ui/static/invite.js:684 (new Copy prompt call), through
  internal/ui/static/invite.js:73 and internal/ui/static/dom.js:58.
- Scenario: Clipboard API access and the execCommand fallback both fail while
  the Dot onboarding dialog is open, including a denied clipboard environment.
- Evidence: copier only calls toast on failure. toast updates #toast, which is
  a body-level sibling at internal/ui/static/index.html:76. The onboarding
  dialog is opened with showModal at internal/ui/static/invite.js:818. Modal
  dialogs put the rest of the document into the inert background; that external
  live region is outside the modal's accessible content. Its z-index:70 at
  internal/ui/static/base.css:59 cannot place it above the dialog's top layer
  or backdrop (internal/ui/static/invite.css:4). On a narrow, tall panel the
  dialog can also cover the toast's fixed top:18px position.
- Effect: the button retains "Copy prompt" without an in-dialog failure
  indication, and a screen-reader user does not receive the intended manual
  selection guidance. The new action inherits an existing shared-helper defect;
  clipboard failure feedback is an explicit criterion of this review.
- Minimum fix: show the existing failure message in a live status/error region
  inside the active dialog, either through a dialog-local copier failure target
  or a shared toast implementation that uses the active modal. Raising z-index
  alone cannot fix modal inertness or top-layer ordering.

Checked:
- internal/ui/static/invite.js:672: short, static setup prompt contains no
  invitation, link code, token, URL, peer id, or interpolated user/server data.
- internal/ui/static/invite.js:681: explicitly tells the user to paste the
  prompt into the Dot after connecting the plugin.
- internal/mcpserver/guide.go:23 and :38 through :42: current recommended task
  instruction, identity check, Trigger vecnet, Event vecnet.message, no Settings,
  Same chat for every run, and client-owned subscription setup. The new prompt
  delegates to that guide rather than duplicating its full task instruction.
- internal/mcpserver/guide.go:117 through :142: default start guide includes
  the Dot setup steps for the authenticated OAuth identity.
- internal/onboarding/prompts.go:29 through :34 and docs/connectors/dots.md:44
  through :64: prompt agrees with current local onboarding/task requirements.
- internal/ui/static/dom.js:10 through :24: h renders text via textContent;
  internal/ui/static/invite.js:684 copies that same pre element's textContent
  through copier at :69. Visible text and supplied clipboard text are identical.
- Copy prompt is a native type=button with its visible accessible name and
  native keyboard activation; the labeled prompt is focusable/selectable.
- internal/ui/static/invite.css:3, :22, :43, :44 and :52: existing bounded
  dialog, scrolling body, wrapping/selectable prompt, sticky actions and focus
  outline support narrow layouts by source inspection.
- internal/ui/static/dom.js:100 through :116: secure Clipboard API, textarea
  fallback, Boolean failure result, cleanup and focus restoration were traced.
- internal/ui/static/invite.js:527 through :534, :632 through :639, :689
  through :758 and :801 through :818: added nodes do not change submission,
  reconciliation, link-code refresh, bearer confirmation, OAuth or close logic.
- Supplied diff adds no production dependency or native launcher/session change.

Remaining unknowns:
- No imports, tests, builds, browser runs, screenshots, network calls, or other
  agents were used. Actual clipboard permissions/fallback success, rendered
  mobile layout, keyboard/assistive-technology behavior, fresh UI/full Go/full
  screenshot gates and disposable browser checks remain with the parent.
- Local guide consistency does not prove current external ChatGPT UI behavior,
  real task execution, subscription acceptance or native Codex acceptance.

=== DONE ===
