# Web UI memory: bounded dashboard retention and measured diagnosis

**Goal:** reduce the unnecessary retained memory Max reports across multiple pages on vecnet.xk.io, while preserving history, live updates, focus and attachments. Track WEB-MEM-1 / issue #60.

**Base:** `fix/webui-memory` at `3d06953f21299b268d84649ea3a488b62807ddad`. Source diagnosis: `.superpowers/sdd/2026-11-09-webui-memory/diagnosis.md`. Synthetic retained-JavaScript and DOM observations are recorded in the component receipt; renderer RSS/PSS and production incident attribution remain open.

## Global Constraints

- Work in this isolated branch. Keep the frozen native release candidate unchanged.
- Use explicit GPT-6.1-Sol workers and independent review. The parent owns all execution and publication.
- Run tests and browser profiling on x-boss, within the two-job aggregate cap. Use synthetic fixtures and a private browser; never attach to a user's browser or inspect private sessions/configuration.
- Establish genuine RED before production changes, then GREEN on the final source. Keep source hashes and actual command exits.
- Preserve access to older history, UI functionality and failure recovery. Do not impose an arbitrary RAM target, silently truncate history, disable features, or change the API/schema.
- Native acceptance permission remains a separate dependency. Component checks do not establish integration or deployment.

## Review Focus

Bound live audit retention while preserving explicit history navigation. Reject stale pagination completions. Prune retired peer chart/row owners while retaining active focus and expanded tables. Preserve last successful data on errors. Measure heap, DOM and process memory after comparable repeated work.

## Task 1: Dashboard regression tests only

**Files:** create `internal/ui/jstest/dash-memory.test.mjs`, at most 400 lines. Reuse existing real dashboard mount, store and activity DOM fixture without modifying those fixtures.

- [x] Prove that 100 disjoint 50-entry head refreshes retain only the current 50-entry live view; identical heads do not duplicate rows, and errors preserve the last successful page.
- [x] Preserve access through Older/Newer/Latest while new heads arrive. Delayed replies must not replace a newer selection or redraw an unmounted view.
- [x] Exercise peer churn and archived visibility. Removed charts must stop receiving updates; active peers retain focus, keyboard position and expanded minute tables.
- [x] Report exact tests, expected failures, controls and omissions. The parent runs the draft against unchanged production before granting Task 2.

## Task 2: Bound dashboard ownership

**Files:** modify `internal/ui/static/dash.js`; optionally create `internal/ui/static/dash-audit-model.js`. Keep Task 1 tests frozen unless a parent ruling explicitly corrects a demonstrated fixture error.

**Produces:** one current audit page, bounded live-head state and scalar cursor navigation; peer caches owned only by the successfully shown directory.

- [x] Replace the permanent audit union with a 50-entry head/current-page model and explicit Older/Newer/Latest controls.
- [x] Guard stale async completions after selection changes or unmount.
- [x] Prune `peerRows` and `peerCharts` after successful directory rendering. Preserve same-ID nodes and the last good directory on errors.
- [x] Obtain parent GREEN and independent review. Avoid unrelated thread/store changes or a global cache framework.

## Task 3: Measure the retention and remaining hypotheses

**Files:** a bounded standalone profiling harness and result artifacts under this task workspace; no production changes in this task.

- [ ] Use private Chromium on x-boss, real UI modules and deterministic synthetic API/SSE fixtures. Pin source, browser, workload and fixture dimensions.
- [ ] Compare fixed-data controls, changing audit heads, peer churn, idle updates and navigation. Capture pre-GC peaks and stable post-GC heap, DOM and owned renderer/process memory; keep harness/browser totals separate.
- [ ] Keep the harness itself bounded: no retained Playwright request lists, DOM globals or remote handles.
- [ ] Compare identical workloads before and after Task 2. Treat the event-pump abort promise and thread/image allocation policies as separate hypotheses until measured. Unit retention assertions alone do not establish that the multi-GB incident is solved.

## Task 4: Review, gate and integrate

- [x] Run focused UI tests, the full UI suite and relevant Go embedding checks on x-boss.
- [x] Complete independent component review, eight dashboard browser screenshots and parent representative visual inspection. Full integration screenshots remain part of the later merged-source gate.
- [ ] Integrate serially after release dependencies, push both remotes, install/deploy under existing authority, and verify the running result. Do not claim deployment from component gates.
- [ ] Give any further thumbnail/history or event-pump fix its own bounded plan after measurement.

--- SUMMARY ---

Fix the confirmed dashboard retention with bounded page ownership and explicit history navigation. Prune obsolete peer chart owners while preserving active UI state. Prove the behavior test-first, review it independently and measure comparable browser memory on x-boss. Keep shared event-pump and thread-image attribution open until the measurements establish their contribution.
