# Ship the ChatGPT Dot copy prompt

Goal: after connecting Vecnet in ChatGPT, show a short prompt and a familiar Copy prompt button so users know what to send to the Dot.

Source: previously independently reviewed component `2b703eeb`, copied exactly onto current master `fbbf664f`. Existing helper styling, text rendering, clipboard feedback and manual-selection fallback are reused. No credentials are included in the setup prompt.

R-DP1 ruling: ship this completed UI component independently of the native Codex release. Its exact six-line UI change has no native runtime dependency. The frozen native candidate and changed-actor-policy approval remain untouched. Cost if wrong: revise this isolated UI patch; no new session, binding or live credential action follows.

Constraints: all application tests/builds/browsers on x-boss; global two jobs, physical owned temporary directory and private synthetic homes. One screenshot run at a time. Explicit GPT-6.1-Sol independent source review. Preserve existing onboarding, OAuth and recovery flows, and current master dashboard/memory fixes. No new permanent tests for the reversible copy action; rerun the existing invite browser journey with its retained disposable clipboard/layout assertions.

Review focus: the copied text matches the current guide and contains no invitation, token or URL; Copy prompt is keyboard-accessible and uses the existing failure feedback; desktop/mobile prompt and actions remain readable and within the dialog.

Task 1 — integrate the existing component and validate current master:
- Files: internal/ui/static/invite.js; CHANGELOG.md; this plan and its final receipt; generated required screenshots/index.
- [x] Copy the reviewed six-line product patch without importing native runtime changes.
- [ ] Fresh independent source review passes; fix any concrete issue under the normal review loop.
- [ ] Parent canonical UI, full Go and full screenshot gates pass on the exact candidate; compare actual/build-box exits and source hashes, retain drains.
- [ ] Existing invite journey plus retained disposable prompt/clipboard/layout checks passes; inspect representative mobile capture.
- [ ] Commit receipts/screenshots, mark ready for review, merge/push both remotes, install/restart dev hub and deploy/smoke Fly under existing CoS authority.

Try it after deployment: open Add agent, choose ChatGPT Dot, complete plugin connection, then Copy prompt and paste it into that ChatGPT conversation. Full ChatGPT task execution is outside the isolated browser fixture and is not certified by clipboard checks.
