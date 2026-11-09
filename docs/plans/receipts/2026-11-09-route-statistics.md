# Route statistics port validation, 2026-11-09

The PR #3 port adds operator route statistics while preserving its original proposal branch. Gated source `002a419e1d4a65e036b935917fb87bc4bcfbce69` includes the signed relocation fix and MCP metadata/selector documentation corrections. It depends on the unmerged Codex release candidate. This receipt establishes no master merge, deployment, release or live native acceptance.

All gates ran on x-boss with at most two concurrent jobs, isolated homes and synthetic fixtures. Parent verified raw hashes, all 2168 before/after/current source hashes, and two empty owned process-drain scans after completed gates. Genuine relocation RED before the fix had19 failing events and54 passing controls; the original failure is retained.

| Check | Result, actual/build-box exit0 | Raw SHA256 |
| --- | --- | --- |
| Focused relocation GREEN | 73 passes, no failures or skips | `3bdcf0074f03eb89d274a65822433f3d9da0081608d82e726d82eea3fccf92a7` |
| Full CLI | 1765 pass events, one existing skip | `74eba5c6a0aa9b019ff9b5444e287fe674d0a313ee0e9150269025e0bb75909e` |
| Full Go | 6040 pass events /2219 top-level passes, zero failures, two existing skips | `db8cb6c3ef68051ae14db6b927cb852c042c6fede1ebb1c201700987a431f123` |
| Vet/build/launcher/npm | vet + fresh binaries;44 E2E +9 npm tests;build/pack/extracted delivery entry passed | `622eb1558ca8f04ee5b69e3f838caa78045edd9b000d89958fd8316014b58adb` |
| Full screenshots | canonical ui-shots;362 PNGs indexed exactly once | `2923eb846c98bfc980b07ea415d57c335e2743acaee119ac1c611cac08844437` |

The full Go skips remain `TestLauncherInstallRejectsUnsafeSnapshotReuse/foreign_runtime_owner` and `TestWriteShots`. The count exceeds the earlier803a8d74 baseline by exactly25 passing events/four roots, matching relocation regression coverage. CLI dependency closure has266 entries and no SQLite.

Independent GPT-6.1-Sol scoped re-review803a8d74..002a419e passed with C0/I0/M0: I1/M1/M2 addressed, no regressions found. Its complete report is copied beside this receipt. Parent inspected `inv-12-dot.png`, `12-dash-success.png` and `launcher-3-mobile.png`, finding no new issue in those representative states. All362 PNG hashes/index entries were checked; this does not claim individual visual inspection of every image.

Retained UI unit/protocol validation at803a8d74, strict TypeScript +2153 launcher unit tests from release3d, and old/new hub DB compatibility remain valid for unchanged inputs. All154 launcher Git blob hashes match this tree. The final three-path fix changes docs/PROTOCOL.md, CLI route statistics handling and its regression test, with no overlap in UI, launcher, schema/examples, hub/store/migration or validator inputs. Compatibility checked69 old tables/rows, replay and route budget across old/new migration use and removed its private fixture. These are retained checks at their stated sources; fresh E2E/npm and builds above use the fixed Go source.

This closing commit contains only validation/review documentation and generated screenshots; executable source stays byte-identical to gated/reviewed002a419e. Original PR #3 disposition and integration follow the release dependency. No production-state write is attributed to these gates.
