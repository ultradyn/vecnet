# Final candidate launcher runtime acceptance, 2026-11-08

**Local native/ACP and disposable Fly acceptance: PASS for source `a7b46a6c4c46c05c531cb348f0c8ac05ae2eae44`.** This report records completed candidate measurements. It does not declare the combined master, xsm automatic delivery, persistent installation, deployment or release complete.

This is the new source-bound report required by [the final acceptance plan](launcher-final-acceptance-plan.md). The earlier [native acceptance report](launcher-native-acceptance-2026-11-08.md) and automatic-delivery receipt remain historical evidence for their own revisions. Later CLI changes for [issue #56](https://github.com/ultradyn/vecnet/issues/56) require separate review and affected gates; they are not covered by these runtime receipts.

## Source and artifact identity

| Measurement | SHA-256 |
| --- | --- |
| Complete tracked candidate source inventory | `c3a76e9a594067a69c4c77a546804837da20df410339b8c86017e947bbc7e20a` |
| Approved public runtime manifest | `94b35ab7fda3ee60109c78cd2316971f22129dc0f5a8011c5b15ac623dab28c2` |
| Launcher/hub-client runtime snapshot | `6650e5e3a4c5757101c56c77112ea419921360f14e331198eb545a1306b43ca0` |
| CLI | `7267a2a6efbd0bac45d28b676de8603eadd06cdcf14e2476103b46e479f79db7` |
| Demo hub | `7c72eaabf0c3ce2fe8dacb1c8f195f3c141f9b1d95eecd9d30f5f32eefb96305` |
| Candidate npm tarball, version 0.1.1 | `0a103e69c908a4de48c59f1ef062882849f60432f4d76265d92bf6f3421b81e9` |

The candidate's scoped source reviews and nonlive gates completed before dispatch: 2,135 launcher/hub-client Node tests, strict TypeScript, npm build/pack and installed-package checks, all 44 launcher E2E cases with a distinct B CLI, full Go, UI and protocol checks. The screenshot closeout is a separate documentation-only change. Raw command receipts and inventories remain in the controller's candidate evidence bundle; this report does not relabel older interrupted or failed gate attempts as passing.

## Actual local measurements

Every command exited normally with status 0 and retained the exact candidate source inventory before and after execution. [The summary](receipts/launcher-final-a7b46a6c-2026-11-08/local-runtime-summary.json) contains run times, accepted job/thread/session identifiers, cancellation acknowledgement identifiers and receipt/log hashes.

| Scenario | UTC interval | Result and public receipt |
| --- | --- | --- |
| Native worktree | 07:04:21–07:04:30 | [PASS](receipts/launcher-final-a7b46a6c-2026-11-08/native-worktree.public-receipt.json) |
| Native project root | 07:06:00–07:06:08 | [PASS](receipts/launcher-final-a7b46a6c-2026-11-08/native-project-root.public-receipt.json) |
| Native temporary workspace | 07:06:49–07:06:58 | [PASS](receipts/launcher-final-a7b46a6c-2026-11-08/native-temp.public-receipt.json) |
| ACP temporary workspace | 07:07:26–07:07:34 | [PASS](receipts/launcher-final-a7b46a6c-2026-11-08/acp-temp.public-receipt.json) |

All four runs observed actual initial and follow-up model turns, exact request retry without another job/thread, consumed follow-up receipt, parent branch/HEAD/dirty/untracked preservation, exact launcher-authored cancellation for the saved job/thread, and teardown of only the owned service and authority. The three native runs restarted only their owned launcher unit and retained the same native session without replaying the initial reply. Native temporary work used an independent Git repository; ACP temporary work had no Git state.

Native preflight bound the running executable bytes to the approved daemon inventory, then measured RPC version **0.166.0** and the session-reported route **opencodex / ocx/gpt-6.1-sol / high**. The native jobs used read-only permission mode.

ACP verified the approved wrapper, package/entry and Node bytes, then measured adapter **0.101.0**, protocol **1**, and a single live stdio session owner across the two turns. Effective provider, model and effort are explicitly **unknown/null**. Successful turns establish availability during this run; they do not attest a particular ACP provider/model. No ACP resume or restart was tested or claimed.

## Disposable Fly measurement and final cleanup

The actual Fly native command exited normally with status 0 at **07:11:56–07:12:05 UTC**. It used a new disposable launcher and sender, a run-owned worktree, and the same approved candidate artifacts. Initial/follow-up turns, exact retry, effective native route, owned-unit restart with the same native session/no initial replay, workspace preservation and exact cancellation all passed.

The [original runtime receipt](receipts/launcher-final-a7b46a6c-2026-11-08/fly-runtime.public-receipt.json) deliberately records only `runtime_pass:true`: operator disablement and private-authority retirement were still pending at that point. It remains unchanged.

The operator subsequently disabled and independently verified **both** exact disposable peers. [The public operator proof](receipts/launcher-final-a7b46a6c-2026-11-08/operator-disabled-verified.json) has SHA-256 `2d602f407daa53c244e00f749a0c35a607c7edef9499d3228e1003e87969f6a6`. Only after that verification did the owner recheck the saved identity/job/cancellation and exact service absence, then remove the run-owned private config/recovery/launcher authority and invitation file. Public receipts were preserved.

[Final Fly acceptance](receipts/launcher-final-a7b46a6c-2026-11-08/fly-final-acceptance.json), completed at **07:25:15 UTC**, joins these proofs and records `pass:true`, both peers disabled, the exact unit absent/inactive/MainPID 0 before and after retirement, and private authority retired. Its SHA-256 is `b70ff36338d8736883cfb30e2bfb8a81854400f620452edf4610e56d3df9f445`.

## Evidence and remaining integration

The adjacent [manifest](receipts/launcher-final-a7b46a6c-2026-11-08/manifest.json) binds eight public JSON artifacts copied byte-for-byte from the measured evidence. No credentials, bearer material, invitation token or private recovery journal is included. Raw logs, source inventories and screenshots remain in the controller's public integration evidence copies under `launcher-final-candidate-a7b46a6c-evidence`, `launcher-local-live-a7b46a6c-evidence` and `launcher-fly-live-a7b46a6c-evidence`.

Still separate: actual xsm automatic-delivery acceptance and cleanup, integration of subsequent source fixes with their affected checks, final source/evidence review, combined-master merge, both remotes, persistent CLI/hub/profile **and separate delivery-runtime** installation, devhub/Fly deployment and health, owned persistent-peer dogfood, and the controller's 0.2.0 release/CI integrity work. These candidate receipts cannot stand in for those outcomes.
