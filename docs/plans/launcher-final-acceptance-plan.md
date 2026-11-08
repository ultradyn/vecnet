# Final launcher candidate acceptance plan

Controller handoff, 2026-11-08. Execution remains held until the scoped native/UI/setup/ACP reviews pass. This is an exact owned-resource plan, not an acceptance receipt. Main owns one sequential CX executable slot; the ACP producer's slot remains separate until explicitly drained.

## Source and candidate gates

Drain all writers, integrate the sole ACP producer's test-only fixture correction, and publish the immutable combined feature HEAD to GitHub and Forgejo. Record the full tracked-source and production-source inventories before and after the following sequential gates:

1. Whole launcher/hub-client Node unit suites, test concurrency 1, plus full launcher TypeScript.
2. `GOMAXPROCS=1 node scripts/npm/build.mjs`; npm pack with scripts disabled; real installed-package tests with Node test concurrency 1. The manifest intentionally remains **0.1.1**. Record source revision, runtime inventories, native binary digests and tarball SHA-256. This is a source candidate, not the final release artifact.
3. Build the local demo hub from the same source with `GOMAXPROCS=1 go build -p 1`. CoS owns final combined Go/protocol/whole-branch review and final merged-source screenshots; do not compete for its reserved slot or duplicate its gates.

CoS retains the authorized **0.2.0** clean-master version/tag/release and release-CI rebuild/integrity checks. Main never publishes or tags.

## Local web and actual provider checks

Use `scripts/native-launcher-live-smoke.mjs` from the combined freeze. It creates a new loopback demo hub, randomly named `smoke-<hex>` profile and launcher peer, private `/tmp/vecnet-native-launcher-*` config, and exact profile-scoped systemd unit returned by the installation plan. Existing profiles, units and sessions are never substituted.

Common environment: `VECNET_NATIVE_LAUNCHER_SMOKE=1`, `VECNET_BIN` pointing to the candidate native x64 executable, `VECNET_UI_DEMO_BIN` pointing to the current-source demo, the existing installed Chromium executable, and a distinct private `SHOTS_DIR` for each scenario. Every run is sequential:

- Native `VECNET_SMOKE_BACKEND=codex-app-server`, `VECNET_SMOKE_WORKSPACE=worktree`, then `project-root`, then `temp`. Use the existing `/home/xertrov/.codex` authenticated background daemon. Permission mode is read-only. Verify the effective model, actual daemon cwd and selected workspace; exact retry yields one job/thread; obtain initial and follow-up markers without tools; restart only the run-owned launcher unit and verify the same native thread, no replayed initial reply and a consumed follow-up receipt.
- ACP `VECNET_SMOKE_BACKEND=acp`, `VECNET_SMOKE_WORKSPACE=temp`, `VECNET_ACP_COMMAND=/work/package-cache/xertrov/fleet/vecnet-acp/claude-agent-acp-0.101.0/bin/claude-agent-acp`. Use existing service-account authentication, ask/default mode, no tool or file request. Verify actual initial/follow-up model markers, exact retry, same live stdio owner and consumed receipt. Do not restart the launcher or claim ACP resume: it advertises none.

Each run cancels only its known job, observes the launcher terminal response, verifies the exact installed unit bytes, stops it, verifies inactive/MainPID 0, removes only that unit, and reloads systemd. Only then may its private credentials/config be removed. Unresolved requests or failed teardown preserve recovery material and the ownership metadata. Receipts and screenshots remain; log no credential or enrolment token. Native no-rollout/policy-refusal cleanup has separate synthetic launcher/restart/sweep evidence; historical zero-input daemon archive refusals are not silently treated as disappearance or a successful cleanup.

## Existing x-boss installation and disposable production hub check

After candidate gates and scoped PASS, back up and atomically replace `/home/xertrov/.local/bin/vecnet` and the owned hub binary with current candidate bytes. Install the current immutable launcher runtime snapshot for profile `cx-on-xboss`, config `/home/xertrov/.config/vecnet/config.toml`, using the feature checkout as the explicit source. Preview and inspect the exact unit before writing. The launcher config is `/home/xertrov/.config/vecnet/launcher/p-cf807762dddb58f6/config.json`; its generated temporary entry was verified against repo_root `/home/xertrov/.local/share/vecnet-launcher` and base_ref HEAD, backed up, and migrated to explicit scratch metadata while stopped.

Recheck that there are no active jobs, then start only `vecnet-launcher-p-cf807762dddb58f6.service`, existing launcher peer `peer_01M6WG7VWA3M2CYPWPN49TNZTQ`. Check actual native daemon health and advertised capabilities. The coordinator uses profile `cx-xboss-coordinator`, config `/home/xertrov/.config/vecnet/coordinator/config.toml`, peer `peer_01M6WHV85PPF9W3C3184R5YBJZ`, Codex thread `01a1b903-7e2d-76b3-81cc-3d12105522be` in tmux `main:8`.

The existing persistent launcher may be installed/restored for ordinary use after GREEN; it is **not** the recipient of production acceptance tests. R-L11 records Max's direct Fly acceptance contract: use disposable peers created through the authorized `fly-operator` and disable/verify them afterward. Once source reviews pass, CoS can issue a fresh disposable launcher invitation and a separate disposable sender invitation into a private file; no credential values travel in messages. Enrol into a new private 0600 config, disable automatic conversational delivery for these test identities, and use only their newly created exact profile-scoped unit, job and thread. Their concrete ids/paths are recorded before activation.

Against `https://vecnet.xk.io`, the disposable sender submits a fresh, durably recorded request key to that disposable launcher, with a `vecnet` project mapped to the authorized local repository, default preset, native read-only worktree, 24-hour deadline and no-tool marker prompt. Retry that exact body/key; require one accepted/running job, a model reply, follow-up consumed receipt, then cancellation and native settlement of only that owned job. Verify unit bytes and stop/inactive/MainPID 0 before removal of its unit/private recovery files. CoS's operator disables both test peers and verifies the disabled state after teardown; preserve private ownership/recovery material on uncertainty. No persistent-peer acceptance recipient, foreign thread loads or blanket cleanup. Any TUI acceptance uses only these explicitly owned disposable threads and remains controller-coordinated.

The distinct automatic-delivery owner is `vecnet-delivery@6c7bae100706c46624555c38874d38209a59615c1ae7a1536db5e8f43d09ce02.service`. Keep its durable registry/ledger/shown state and one-owner semantics through the update. The old `vecnet-codex.service` stays disabled; the original launcher profile stays opted out of conversational delivery. Recheck actual delivery-status after installing the binary.

CoS owns production web/deploy/Fly/xsm and final release. Main performs no xsm real-account model activation; its pending direct Max approval is separate. Publish final acceptance as a new report bound to the tested candidate source, with historical receipts explicitly distinguished.
