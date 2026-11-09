# Event-pump lifetime component receipt

2026-11-09. WEB-MEM-2, follow-up to issue #60, based on `3d06953f21299b268d84649ea3a488b62807ddad`. Component ready for review; not merged or deployed.

The event pump previously created one pending abort promise per session and attached another losing Promise.race reaction on every resource-settlement wait. Normal disabled/auth termination also left its abort listener installed. The fix creates wait-local state, removes the listener on settlement/rejection/abort and bypasses it without a signal. Existing reload, backoff, cursor-reset, accepted-once-shown acknowledgement and abort behavior remain covered.

All execution ran on x-boss with private synthetic fixtures, one job per lane and an aggregate cap of two. The controller verified raw hashes, actual command and bb exits, source inventories and two empty owned process scans after every gate.

| Gate | Actual result | Raw SHA-256 |
| --- | --- | --- |
| Tests against unchanged store | Ten expected lifetime failures, three gated abort/order controls pass; actual exit 1 / bb 1 | eb180f9dcbeb88d91e334b8d066d0778d5054f3fda26a6a3e5e678582a393c9e |
| Focused candidate | Thirteen pass, zero failures/skips/cancellations; actual exit 0 / bb 0 | 21fa9025c2a4a497ff486273db8e99eefb9b9ffa140c0f669750383a7c6158a4 |
| Full UI suite | 491 pass, zero failures/skips/cancellations; actual exit 0 / bb 0 | e297827f06816aeb3f765e6096a77bf80613eeb4f9d764381858c65a8503af6c |
| Go embedding/CSP/size | Ten pass, zero test skips; actual exit 0 / bb 0 | c0f6b2824b13be21227a578e0a44d2dd45bcf79ef234987585c016e26154506c |

Current production SHA-256: `dc33e44454baff721e0cc8c0709b2d7407c247ff203d792164dc68f132351539`. New regression SHA-256: `e32f9f5c29b5343164769c8d8799169b8b438c833d721d8b99c44a90107e0acc`. Original store tests remain unchanged at `d0784c1cd11978c428b1e7c49f7457d71778e7a42d0da9039c2236cf249c6717`. Parent workspace-ignore metadata was excluded after RED; this did not change production or tests. Later closing docs/changelog edits do not change the executable source exercised by these gates.

Independent GPT-6.1-Sol source review passed Spec and Quality, C0/I0/M0. It traced wait lifetime, abort/rejection races, no-signal/normal terminal behavior and actual app/API callers; all thirteen regression cases call real createStore with real AbortSignals. The reviewer ran no execution gates. No-signal, already-aborted, listener-registration race and defensive settlement-rejection branches were source-traced rather than directly added to this regression file.

The tests establish observable abort-listener lifetime and preserve ack/abort order. They do not measure retained Promise reactions, JavaScript heap slopes, renderer/process memory, long-running cross-page behavior or the reported multi-GB incident. Shared outstanding loader work may remain pending until its existing resource load finishes; successful waits no longer accumulate on a shared unresolved promise. Dashboard measurements remain separately attributed.

Full merged-source Go and browser/screenshot gates, final integration review, installation/deployment and production reproduction remain open. Keep issue #60 open. Evidence: `.superpowers/sdd/2026-11-09-webui-pump-lifetime/` raw logs, pin inventories, review and process-drain records.
