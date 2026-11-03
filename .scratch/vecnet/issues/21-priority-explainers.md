# 21: Hover explainers for wake, wake_idle and hold

Raised by Max over vecnet, 2026-11-03: "in the UI, we should have explainer things that show up on hover for wake, wake_idle, and hold."

## Where
The priority buttons in the new-message form (`internal/ui/static/compose.js`, the `['wake','wake_idle','hold']` row) and the thread reply box (`thread.js`, same row). Do it after the attachments branch merges: Tasks 3 and 4 of that build edit both files.

## Copy (from PROTOCOL §5.2; keep it truthful)
- **wake:** Interrupt now. Pushed to the recipient straight away (push routes, Grok Bot routines, dots); pull clients see it at once.
- **wake_idle:** Interrupt only if the recipient is idle or its presence is unknown. Otherwise it waits, and is pushed once when the recipient goes idle.
- **hold:** Never pushed. It waits in the recipient's inbox until it next checks; pull clients still see it as it arrives.

## Shape
One `PRIORITY_HELP` map in `format.js` used by both rows; a styled tooltip from a CSS file (shows on hover and on keyboard focus, dark palette, no inline style, CSP unchanged), plus `aria-describedby` so screen readers get the same text. Optional: the same lines in the TUI help for its priority toggle.

## Done (2026-11-03, branch `md`)
`internal/ui/static/prio.js` builds the priority picker for both composers (`compose.js`, `thread.js`). Each button's `aria-describedby` names a hidden element holding `PRIORITY_HELP[p]`; a visible, `aria-hidden` tip (priority name in its colour, then the same words) shows on hover and on keyboard focus through CSS `:has()` in `base.css`, and Escape hides it until focus or the pointer moves (WCAG 1.4.13). No `title` on the buttons, no inline style, CSP unchanged. The new-message tip opens leftward because the picker ends its row. Checked by `scripts/ui/md-thread.mjs` (hover, Tab focus, Escape, on-screen at 390 px); shots `md-tip-*.png`. TUI help lines not done (optional).
