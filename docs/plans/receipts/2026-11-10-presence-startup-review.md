# Independent final combined presence/startup review — SOURCEONLY

Spec PASS
Quality PASS
C0 / I0 / M0

Concrete file:line / Scenario / Effect / Fix findings: none identified within this bounded source review. These verdicts assess the combined source contract; they do not establish execution, installation or release readiness.

Sole current code target: `/home/xertrov/src/vecnet4/.worktrees/presence-startup-release`. Read the combined final brief FIRST, the entire 2,078-line / 20-path review package in passes, components.json and progress.md, then traced the current startup, preference, policy and Settings integration seams. No pending unrelated code was reviewed.

**Review composition and attribution**

The earlier independent 18-path TTL + Settings source review is retained as attributed work: `/home/xertrov/src/vecnet4/.worktrees/grok-presence-ttl/.superpowers/sdd/2026-11-09-grok-presence-ttl/ttl-settings-whole-final-review-report.md` (Spec/Quality PASS, C0/I0/M0). Its detailed storage, consumer, confirmation, Settings and test audit remains applicable to the retained bytes: independently hashed all 20 current changed files, and all 17 retained TTL paths other than CHANGELOG match that report exactly. Reread the whole combined diff and current critical seams rather than treating the earlier verdict alone as evidence. The current CHANGELOG contains both component entries; the new startup scope is main.go and the 198-line URL flag test.

Read the startup production brief and independent review report under `/home/xertrov/src/vecnet4/.worktrees/issue47-startup-master/.superpowers/sdd/2026-11-09-issue47-startup-master/` as claims, plus the selected S1 master-port plan/addendum contract. Independently inspected the actual current main.go delta, the new test and existing URL/origin/bootstrap controls. Old worktrees were artifact references only. The current main.go and new test hashes match the startup review's recorded hashes; their correctness was also assessed directly here.

components.json identifies base `5fb55edc8c83f1f8f6defd1693d8a139a2377a8c`, TTL component `d5aa1080022ff72b3a3a34c0005880d8c357fe84`, startup component `ab914c83cb5c49b708210ecde11d53a7bb8282e7`, and candidate `7b5659e666747a6c61e707aa2d723d8eb5565538`, with native ancestry excluded. These identities and ancestry statements are parent artifact attribution, not fresh ref/index/ancestry verification. Current source identity is recorded below by independently computed hashes.

**Checked and fine — startup diagnostics, selection and validation**

- cmd/vecnet-hub/main.go:82,100 registers public-url and relocated-to with literal empty defaults. Their environment values no longer enter flag default metadata before parsing; help/unknown-option diagnostics return at :104–106 before the environment fallback at :110–117. Usage retains the option and environment names. This closes the selected two-URL metadata leak for valid prefixes/IPv6 as well as invalid userinfo/query/fragment/authority payloads.
- main.go:110–117 uses Visit membership, not value equality, to select fallback. Safe explicit URLs override invalid environment URLs. Both `--public-url=` / `--relocated-to=` and separate empty arguments are explicit visits and override the environment: empty public URL selects the listener-derived URL; empty relocation selects ordinary mode. Repeated flags retain normal parser final-value behavior. The map reads flag metadata without calling Set or changing the parser's visited set.
- main.go:118–135 retains init/identity/unknown-command dispatch. Storage/retry/preference/blob/origin/legacy/OAuth validation order at :136–172 is preserved. The selected values and resolved Bootstrap reach serve at :173–177; neither environment fallback nor URL selection replaces the preference bootstrap map.
- main.go:359–367 wraps hubid.CheckHubURL with fixed contextual errors. internal/hubid/hubid.go:68–171 retains safe sentinel errors, URL normalization and the credential/query/fragment/authority checks. main.go:399–426 validates configured URLs and relocation self-targeting before listening or opening the store; :428–447 validates the listener-derived public URL and self-targeting before opening the store, with listener cleanup on refusal. Intentional public HTTP support is preserved.
- main.go:449–461 copies the preference bootstrap into openHub; :313–330 preserves it into hub.New. Handler selection at :487–500 still separates relocation from ordinary operation. Cleanup and ordinary-mode worker startup at :505–514, checked URL announcements at :518–522 and shutdown at :529–545 are unchanged. No new TTL worker, early store open or unchecked URL announcement is introduced by S1.
- cmd/vecnet-hub/huburl_flag_diagnostics_test.go:19–93 defines 24 diagnostic leaves across both environment keys, six invalid/valid URL forms and help/unknown-option. It captures actual stderr with restoration, checks ordinary parser results/usage/environment names, excludes raw/quoted values and canaries on stderr/stdout/error, and asserts no data directory was created. This is source evidence of intended assertions, not an execution result.
- The same test at :95–198 defines ten override leaves: environment path/IPv6 selection, safe flag over invalid environment, equal-form empty over invalid environment, and separate-empty over valid environment for both flags. It checks selected identity and relocation 410/hub_moved versus ordinary 401. The HTTP client contacts only the boot's loopback listener, has no proxy configured and refuses redirects. smoke_test.go:96–135 registers cancel/join cleanup immediately; the test also stops each hub. Existing smoke_test.go:310–355 and main_test.go:938–999 retain environment/default/override, unsafe refusal-before-store and checked IPv6 controls.

**Checked and fine — MCP origin and TTL bootstrap seams**

- main.go:83 retains existing MCP environment-default registration and ordinary explicit flag precedence; :161–164 validates the selected origin list before serve/store startup. An explicit empty flag resets to the existing default extra-origin behavior, while `none` selects own-origin-only. main_test.go:546–614 preserves invalid environment/flag refusal and default/none/custom/override/empty-reset controls.
- internal/mcpserver/origin.go:17–49 accepts exact HTTP(S) origins and rejects credentials, paths, queries/fragments, wildcards and malformed hosts/ports. :54–99 keeps the checked public origin allowed, nil extras as the default ChatGPT extra origin, nonnil empty extras as own-only, and fixed error text for invalid configuration. internal/httpapi/mcp.go:18–20 passes the checked public URL and selected list. Public URL path/IPv6 support does not turn that URL into a new extra-origin entry.
- The S1 diagnostic claim is bounded to public-url and relocated-to. It is not a claim that all startup environment-backed flags have sanitized metadata: MCP origins and other pre-existing environment defaults remain registered by their existing paths. The selected S1 plan does not require a universal startup flag rewrite.
- main.go:548–589 registers preference flags with safe factory metadata; :594–605 independently reads actual Visit membership, applies environment fallback, then explicit flag precedence. The earlier URL Visit does not consume or alter those visits. :641–642 preserves SourceFlag and the actual flag/environment origin even for factory-equal values, and :653–654 preserves Config.PresenceTTL.
- Consequently explicit `--presence-ttl=1h` remains explicit D3600; with no explicit G, Grok inherits that D3600. With neither explicit, Grok remains factory600. No new G command-line flag is introduced; generic explicit G bootstrap through hub.Config remains supported. main_test.go:785–833 checks equal-default environment/flag provenance and safe flags overriding invalid environment; :835–875 checks preference diagnostic exclusion; :876–936 checks invalid-before-store and every enabled preference flag/environment binding. These controls were read as source, not run.

**Checked and fine — retained TTL policy, persistence and consumers**

- internal/prefs/registry.go:40,48–55,77–80 retains live/editable G600, seconds, bounds60..86400 and presence_shortened metadata, with session/launcher900 and other-default3600. Registry version2 still enters the generation digest through internal/hub/preferences.go:185–198, invalidating older-generation previews/ETags.
- internal/hub/preferences_runtime.go:57–78 resolves explicit stored/flag G first, otherwise explicit D when G is factory-sourced, otherwise G600. It selects provenance rather than inferring intent from numeric equality and retains controller/source/origin. Session and launcher use their own keys. preferences.go:109–171,259–319 retains factory/bootstrap/stored overlay and restart behavior; RESET exposes the underlying bootstrap. :388–413 preserves the exact D/G factory-equal stored-intent exception while retaining identical stored SET and absent-row RESET no-ops.
- preferences_runtime.go:81–150 retains four-class risk comparison independent of peer population, deterministic duration/kind messages and both candidate dependencies for Grok. The earlier I1 case remains fixed: stored G7200/D3600 followed by dual RESET resolves7200→600; restoring either old G or old D raises the resolved Grok TTL, so both reset keys qualify. The literal four-case oracle remains in preferences_test.go:1209–1314. There are no invented affected-peer counts.
- internal/hub/preferences_preview.go:144–186,230–259,270–322 retains the shared risk resolver for preview/PATCH and binds confirmation to caller, exact proposal, risk, ETag, generation and expiry. Raw-equal G600 replacing inherited D7200 still needs real confirmation. preferences.go:464–541 retains authorization, strong preconditions, serialized storage/audit/revision and commit-before-publication/notification. Existing commit-failure handling returns before Current changes; presence-only changes do not acquire retention work through the production adapter.
- preferences_runtime.go:153–161 and presence.go:54–66 preserve unknown for missing/invalid reports and strict age > resolved TTL, so age600 is fresh under TTL600. Policy uses the actual peer kind/report and does not renew timestamps. Fresh sends, directory, Peers and own-peer Inbox retain the shared resolver and operation/batch snapshot rules documented in the earlier audit: send.go:243–248,663–670,738–751; directory.go:74,137; opview/dash.go:19,134; opview/inbox.go:19–34.
- internal/hub/tickers.go:36–81,160–200 retains committed transactional reevaluation on startup, preference notification and ticks. It reselects committed preferences/current reports, closes selection before writes, and releases only queued held rows. held/state predicates preserve idempotence; no delivery, receipt, attempt or presence update is created. S1 ordinary startup still reaches these workers, while relocation mode remains without workers. Existing dispatch eligibility and leases remain in deliveries.go:88–170. This does not prove universal remote duplicate prevention.

**Checked and fine — retained Settings, HTTP, tests and documentation**

- internal/ui/static/settings-model.js:153–214 unlocks flags only for exact editable/live D/G and retains deliberate equal SET intent for default/flag D/G; identical stored SET remains clean. The same predicate reaches editing, adoption, dirty count and source labels at :388–442,521–545. Rendering/load cannot store intent; RESET remains distinct and generic flags stay locked.
- settings-model.js:172–178 and internal/ui/static/settings.js:138–144 derive current Grok policy from committed rows. Draft SET/RESET cannot relabel inherited D7200 as current G600. settings.js:125–183,339–406 retains reset/source/origin explanations, Store override with separate Save, the G policy token/drawer/error focus, and the committed duration/controller/source/origin paragraph in both views. The displayed raw G600 editor survives inherited D7200.
- settings-model.js:576–724 retains real authenticated preview, If-Match, server risk/token redemption, cancel-with-draft preservation, fault preservation and explicit 412 reconciliation followed by a fresh preview. settings.js:521–560 shows the actual server risk and retains apply_existing only for all-retention proposals. Equal-intent drafts still participate in sidebar and leave/beforeunload guards at :662–696; app.js:181–185,360–374 restores the route before unmount on Stay. Browser wait scripts/ui/prefs.mjs:545 only observes that restoration and preserves the prompt/hash assertion. Cleanup remains at settings.js:700–714.
- internal/httpapi/prefs.go:24–44,88–122 and static/api.js:165–178,390–392 retain no-store, authenticated transport, conditional ETags and operator/as-peer boundaries. No new HTTP wire shape or endpoint is introduced. prefs_test.go:80–244 preserves sorted population22, literal G metadata, independent refresh fixture, 304/no-store, authority and precondition controls. Registry/value/TUI count adjustments retain existing assertions and generic descriptor consumption.
- Retained backend tests cover literal boundaries, actual restart, risk ordering, rejected/token/commit-failure durability and Current/audit/notification state. Runtime fixtures retain sampled-time margins, independent startup synchronization, hold priority/attempt/receipt controls and cancellation/join cleanup. settings-presence.test.mjs retains independent lock/equal-intent/committed-resolver/preview/cancel/RESET/fault/412 controls; prefs.mjs adds real-hub inherited G600 and explicit boot cases in both views and viewport sizes. These are coverage observations from source only.
- docs/PROTOCOL.md:840–846 retains local inheritance, stored intent/reset, strict staleness, confirmation and historical limits. CHANGELOG.md retains the startup diagnostic entry above the TTL/Settings entry. Their claims are consistent with the bounded selected changes. Neither combined documentation nor this review closes remote Issue4 or native acceptance.

**Current source identity**

Review-package SHA-256: `d78174a7584762577adb83be23c16182405f7e034ee014e3b2995c79818f4ccd`.

All 20 current changed paths were hashed again before writing this report:

| Path | SHA-256 |
| --- | --- |
| CHANGELOG.md | 3d3f2ca094b94185cc6700f658746da9abb8eed42576fa5a0975a7d399b3078f |
| docs/PROTOCOL.md | a1a08f79c85e781275f8d9a5c05164be97115d20e8c3dd227cdb06591f39928b |
| cmd/vecnet-hub/main.go | 3480220b59e068e9af1996ad5cb430ea8493e56ec103fdb7d9c6d63f43a4dea5 |
| cmd/vecnet-hub/huburl_flag_diagnostics_test.go | 4550ad85a329b1c03cf9c435ff5f4aa33276104d561d08bee5f19fc94be2c041 |
| internal/httpapi/prefs_test.go | 116621b423ae6eea3dde92e81deb91cf954667461215bab38b376a82836a6803 |
| internal/hub/preferences.go | 1c24a5c8833935262c6dcab011106120ed3af8cc538b4fd0e315ef0d4b56dd8e |
| internal/hub/preferences_preview_test.go | d1afebc23d6861fb6dbf923ccee033f0bfabe3b62164c5ba715469d676482925 |
| internal/hub/preferences_runtime.go | 1e9f9ec301a2ac561b7992fc7f4e3dfdd94982fda895d89e02a4c4bce1b1312b |
| internal/hub/preferences_runtime_test.go | 1cecba13ce0d7156bf4abf5c51d3b4f156290aaf05303fd5a67855b3e2047d47 |
| internal/hub/preferences_test.go | 35a3435c048a43f97a993eb0dfa429bfe399634209eabba1004a0cc593d328aa |
| internal/hub/presence.go | 79b8b340cbfac9c62cb2da3d707385c575326aea834c41309549616f1bda673e |
| internal/hub/presence_test.go | 5da157336fb604022f528f63786407669dabb1a4a5ccfde5a91f009838099295 |
| internal/prefs/registry.go | f86080d2317546ddaa0d1e22917bcdea05993c3a5f50dcdba892a94ea356a82d |
| internal/prefs/registry_test.go | 3abd45779fd2f45adbb65936d3fed2a5b49f0535964d890f593ad7d0e3711e85 |
| internal/prefs/value_test.go | 9ef7ca3c9ef3ccec97547192e51654e4d316a83dc5cc25e3de16a25dcd1f94fd |
| internal/tui/screen_prefs_test.go | 87ab65608b95bfa5514a26041e679bbee096749a32a7e5929defe5f7ea7ae3d3 |
| internal/ui/jstest/settings-presence.test.mjs | b9893ff203696d762a24f240012d94b951b2141499016102629b887f2fc39217 |
| internal/ui/static/settings-model.js | e4d9ef55d15cdd9a4b97b3ae8ff479d7136a060d43670751e1e32c9cea181c05 |
| internal/ui/static/settings.js | e922607bcf43a6b2d37f9673bbc8dc96df44de674a71d59de08749af7e0d2312 |
| scripts/ui/prefs.mjs | 6ab8139bd664a629196fb75fbe465cb31a2aafd5c4438a033e623003c976a2ad |

**Cannot verify / explicit boundaries**

- No executable imports/parsers, Go/Node/SQL, tests, builds, application, network, SSH or browser were run. Compilation/module loading, actual reached assertions, race behavior, keyboard/dialog timing, responsive layout, accessibility in use and screenshots remain unverified by this reviewer. Source wiring does not establish the contents or identity of a built/running binary.
- S1 focused36/fullcmd199 PASS with independent review, and TTL Go5192/UI521/protocol/Settings29/browser94captures PASS, are supplied component evidence only. They are not fresh executions by this reviewer and are not upgraded into combined-source runtime evidence.
- The ledger also records a TTL component screenshot index failure and a planned index-only repair. That attribution is preserved: this review neither validates the proposed repair nor treats the failed gate as passing. Extra TTL gates and NEW combined exact-source full Go/screens/UI/protocol/visual checks remain parent-owned. No concurrent gate was queried or assumed to finish successfully.
- Master installation and Fly deployment remain parent gates. Remote Issue4 discovery/drain/read/process/receipt/cursor readiness and native acceptance remain OPEN/UNPROVEN. No deployment, native ancestry or remote duplicate-run closure is asserted here.
- Only this NEW report was written, LAST. Product source, tests, documentation and old artifacts were preserved. No refs/index/commits/push, delegates, communications/c2c, attn, private-home/configuration/credentials or foreign sessions were accessed.

=== DONE ===
