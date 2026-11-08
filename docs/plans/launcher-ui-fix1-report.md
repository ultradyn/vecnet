# Launcher UI review fix1

Baseline: `f09fad6bd959c47c5e035c61c3d7f1634cd8d65c`, reviewed ancestry ed88/a5. The permission-catalog correction is in `af41a4f7`; the UI/cleanup source commit is `138611a36b3fb52d8f87ef0d3c5758590081ac2d`. This is scoped source/test/browser evidence, not production deployment or final release.

## Findings addressed

- I1: exact submitted launch body, idempotency key, deadline and safe result ids persist in tab-scoped recovery. Scope includes origin, pinned public key, authenticated caller and launcher. Reload/navigation, held responses and late success preserve the same operation. Malformed records/pin changes refuse replacement; storage denial retains memory state and guards leaving; discard explicitly warns that a job can still run. Automatic retry stops at the conservative 24-hour dedupe bound. Submitted prompt storage is disclosed; no credentials, API client or unsent drafts are stored.
- I2: smoke recovery material is unsafe to remove by default. Partial installation or unresolved accepted send retains credentials. Cleanup settles a resolved job, verifies exact unit bytes, stops it, verifies inactive/MainPID 0, removes that unit and reloads the manager before deleting private files. Nine injected failure/order regressions preserve private material. The exact outbound request is saved privately before waiting for a launch response.
- I3: per-backend mode tags now intersect configured permission policies, just like the global catalog. Actual daemon integration proves a read-only-only configuration emits no ask/auto/full-access choices and admits its read-only selection. Host admission remains authoritative.
- I4: onboarding and unavailable-state guidance name a supported running Codex background app-server, the same CODEX_HOME, explicit daemon start, launcher check and launcher status. Waiting alone is not presented as recovery.
- Scratch compatibility: only explicit scratch:ALIAS intersected with advertised project aliases grants a temp choice. An ordinary project named temporary stays selectable as a repository; temp sends the advertised scratch alias.

## Unit checks and chronology

The original I1/I4 regressions were observed before the UI production fix: `/tmp/vecnet-launcher-recovery-red.log`, 2 expected failures. Focused UI `/tmp/vecnet-launcher-ui-recovery-green-focused.log`: exit 0, 18/18. Full UI `/tmp/vecnet-launcher-ui-recovery-green-full.log`: exit 0, 453/453. Both use Node --test --test-concurrency=1; the focused path is internal/ui/jstest/launcher.test.mjs and full path internal/ui/jstest/.

I2 synthetic legacy-ordering RED: `/tmp/vecnet-cleanup-red.log`, 9 expected failures, exit 1; covering `/tmp/vecnet-cleanup-green.log`, 9/9, exit 0. Commands execute scripts/native-launcher-cleanup.test.mjs with Node --test --test-concurrency=1. This is sensitivity against the old cleanup ordering, not an original feature-wide TDD claim. Fresh real model/service cleanup after the fix remains part of the final native smoke gate.

I3 first fixture run had an async race and was corrected; the meaningful catalog RED is `/tmp/vecnet-catalog-red.log`, proving unwanted scoped modes before the source fix. `/tmp/vecnet-catalog-green.log`: daemon/presence 22/22 pass, exit 0. Later shared config/policy/presence/daemon check `/tmp/vecnet-native-fix1-shared-green.log`: 1,251/1,251 pass.

Product JS and unit-test hashes matched the 18/453 GREEN freeze during the later browser run. Only the browser harness changed: it now waits for Dashboard to mount before pressing Back, and captures complete form/action geometry. Initial browser timing failures remain in `/tmp/vecnet-launcher-ui-browser-first-failure.log` and diagnostics, not accepted evidence. No product fix was hidden by the harness correction.

## Fresh browser and visual verification

Fresh local demo build: GOMAXPROCS=1 go build -p 1 -o /tmp/vecnet-launcher-ui-browser/demo ./internal/opseed/demo, exit 0. Browser: GOMAXPROCS=1 VECNET_UI_DEMO_BIN=/tmp/vecnet-launcher-ui-browser/demo SHOTS_DIR=/tmp/vecnet-launcher-ui-recovery-shots PLAYWRIGHT_EXECUTABLE_PATH=/home/xertrov/.cache/ms-playwright/chromium_headless_shell-1248/chrome-headless-shell-linux64/chrome-headless-shell node scripts/ui/launcher.mjs, exit 0. Raw `/tmp/vecnet-launcher-ui-browser-journey.log`.

The real local hub journey verifies host setup, actual enrolment, one-time token absence from browser storage/page after close, unavailable host actions, native default discovery, 24-hour deadline, mobile overflow, held accepted response across Dashboard/back, lost-response reload and repeated retry. All three sends retain identical body/key/deadline and resolve the same message/job/thread. Presence is simulated; no model is run by this browser test.

Nine fresh screenshots were inspected under `/tmp/vecnet-launcher-ui-recovery-shots/`: unavailable, host setup, setup top, token-redacted installation commands, desktop ready, full ready form, mobile, retry recovery, resulting thread. Forms/dialog intentionally scroll vertically; desktop/mobile actions remain reachable. Only redacted install screenshots are accepted. Manifest `/tmp/vecnet-launcher-ui-browser-shots.sha256`. Product/test/browser source manifest `/tmp/vecnet-launcher-ui-browser-final-source.sha256`.

The frozen shipped assets total 664,284 bytes. Separate commit `d920cf10` raises the unchanged total-size guard from 640 to the smallest sufficient 656 KiB step, with dated exact-byte comment and CHANGELOG entry. HTTP TestUIUnderSizeCap passed: `/tmp/vecnet-launcher-ui-size-green.log`, exit 0. No readability cuts or disabled assertion.

Browser/demo/Chromium and UI test processes drained; the worker explicitly released its single sequential slot. Main separately inspected the fresh complete desktop form. No source publication grants intake, merge or rollout approval. CoS owns same-reviewer scoped re-review and final combined acceptance.
