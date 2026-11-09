# Ship the ChatGPT Dot copy prompt

Amendment 2026-11-09 (R-DP2): fresh review identified invisible clipboard failure feedback outside the active modal. Retain the 43-line canonical browser regression for forced Clipboard API plus fallback failure, selectable prompt and successful retry; add an optional Dot-local live-status target without changing other copy actions. Parent observed genuine RED then GREEN. Final review/gates are complete; merge/install/deploy follow as controller steps.

Goal: after connecting Vecnet in ChatGPT, show a short prompt and a familiar Copy prompt button so users know what to send to the Dot.

Source: previously independently reviewed component `2b703eeb`, copied exactly onto current master `fbbf664f`. Existing helper styling, text rendering, clipboard feedback and manual-selection fallback are reused. No credentials are included in the setup prompt.

R-DP1 ruling: ship this completed UI component independently of the native Codex release. Its exact six-line UI change has no native runtime dependency. The frozen native candidate and changed-actor-policy approval remain untouched. Cost if wrong: revise this isolated UI patch; no new session, binding or live credential action follows.

Constraints: all application tests/builds/browsers on x-boss; global two jobs, physical owned temporary directory and private synthetic homes. One screenshot run at a time. Explicit GPT-6.1-Sol independent source review. Preserve existing onboarding, OAuth and recovery flows, and current master dashboard/memory fixes. Keep the meaningful R-DP2 canonical clipboard-error regression; reuse the existing invite journey and retained disposable prompt/clipboard/layout checks.

Review focus: the copied text matches the current guide and contains no invitation, token or URL; Copy prompt is keyboard-accessible and uses the existing failure feedback; desktop/mobile prompt and actions remain readable and within the dialog.

Task 1 — integrate the existing component and validate current master:
- Files: internal/ui/static/invite.js; CHANGELOG.md; this plan and its final receipt; generated required screenshots/index.
- [x] Copy the reviewed six-line product patch without importing native runtime changes.
- [x] Fresh independent source review passes; fix any concrete issue under the normal review loop.
- [x] Parent canonical UI, full Go and full screenshot gates pass on the exact candidate; compare actual/build-box exits and source hashes, retain drains.
- [x] Existing invite journey plus retained disposable prompt/clipboard/layout checks passes; inspect representative mobile capture.
- [ ] Commit receipts/screenshots, mark ready for review, merge/push both remotes, install/restart dev hub and deploy/smoke Fly under existing CoS authority.

Try it after deployment: open Add agent, choose ChatGPT Dot, complete plugin connection, then Copy prompt and paste it into that ChatGPT conversation. Full ChatGPT task execution is outside the isolated browser fixture and is not certified by clipboard checks.

[Final current-source evidence and review copies](receipts/2026-11-09-dot-copy-prompt.md).
