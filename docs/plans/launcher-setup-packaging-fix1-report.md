# Launcher setup / packaging fix1 — candidate pack gate PASS

This is candidate validation at `6c5263f851c5ff46ad61cfc2f473ec1abce5d8cb`, not a release/deployment gate. ACP fixes pending on the producer branch were not integrated into this candidate; main must freeze and rebuild after integrating them. No production change was needed during this gate.

The tested setup/packaging production bytes were subsequently committed as `e97b0023`; `sha256sum -c candidate-owned-source.sha256` verified all nine owned files still match. ACP CLI selection is in `11ea5fa2` and scratch compatibility in `af41a4f7`. The pending ACP producer fixes and final rebuild remain separate.

## Scope / source pins

- Worktree: `/home/xertrov/src/vecnet-launcher-live`; initial fix1 BASE `f09fad6bd959c47c5e035c61c3d7f1634cd8d65c`.
- Candidate HEAD before = after = BUILD_INFO revision: `6c5263f851c5ff46ad61cfc2f473ec1abce5d8cb`.
- Owned changed paths: `internal/cli/launcher_install.go`, `internal/cli/launcher_install_unix.go`, `internal/cli/launcher_install_other.go`, `internal/cli/launcher_test.go`, `scripts/npm/build.mjs`, `scripts/npm/notices.mjs`, `scripts/npm/package.test.mjs`, `npm/README.md`, `docs/connectors/launcher.md`.
- Ownership outside these paths remains with main/UI/native/ACP workers. No commits or branch publication by this worker; the controller publishes the integrated source.
- Frozen `a9d4ee3` delivery implementation untouched. Existing delivery assets, execution and wrapper signal behavior remain tested.
- Full repository source observation (nonignored files, excluding worker state/generated screenshots/receipt evidence): 1718 files, before SHA256 = after SHA256 `d9e00726d10faf32245627a22675c09bfc89566477ffe21b61ad2d688fb537f8`. No path changes during this window.
- Production input inventory (Go sources/module pins, embedded resources, runtime/modules/schemas/templates, packaging/wrappers/manifest/README): 451 files; SHA256 `e69b74d00589efb7bee84e301df7bbb01cbb4039973481b5f6efe79cdcf044b9`. No production input changes during this window.
- Full inventories are in `candidate-source-before.json`, `candidate-full-source.sha256`, `candidate-full-source-after.sha256`, `candidate-production.sha256`; owned-source hashes are in `candidate-owned-source.sha256`.

## Raw commands / exits / budgets

Each command ran sequentially with ONE compiler/test worker. Node test concurrency was 1; notices generation obeyed the same GOMAXPROCS/-p budget. No build/test ran on xsm.

```sh
GOMAXPROCS=1 node scripts/npm/build.mjs
# exit 0; raw candidate-build.log
npm pack ./dist/npm --pack-destination ./dist --ignore-scripts
# exit 0; raw candidate-pack.log
GOMAXPROCS=1 node --test --test-concurrency=1 scripts/npm/package.test.mjs
# exit 0; 6 tests / 6 pass / 0 fail / 0 skip; raw candidate-package-tests.log
```

The first command built native CLI+hub binaries for x64 and arm64 sequentially with `-p 1`. The builder staged 45 existing delivery runtime modules before the preserved ESM-manifest entries (47 delivery digest inventory entries total), and 65 complete launcher runtime assets including all four dynamic builtins: acp, codex-app-server, paseo, t3.

## Actual packed runtime proof

All six tests passed without weakening existing assertions:

1. Packed-file allowlist, complete native binaries, binary digests, notices and npm manifest.
2. Actual `npm install --global --prefix` of the fresh tarball in a private HOME/XDG, outside the source checkout; both installed commands, temporary private hub messaging; enrolled launcher setup; install preview and explicit write/enable/start against a fake manager ONLY; Node itself was REAL. Installed executable discovered its own packaged runtime without `--source`. Real Node imported every builtin (ACP/native Codex/Paseo/T3) from the private copied snapshot. Actual installed `launcher check` returned the expected native unhealthy-backend report for a deliberately absent private Codex socket, with configured default preset/modes; this is not a fake Node success or a live-provider smoke. Changed packaged module bytes were refused before unit/manager changes.
3. Wrapper preserved failure after a native SIGPIPE.
4. Existing automatic-delivery asset digests and exact source agreement.
5. Complete launcher runtime/main/plugins/native/dynamic builtin inventory, exact source bytes, schemas/manifests and exact profile template; every packed runtime file accounted for by provenance metadata.
6. Existing extracted delivery entry executed, created private fixture state/status, and drained.

## Artifacts / hashes

- Candidate tarball: `/home/xertrov/src/vecnet-launcher-live/dist/vecnet-0.1.1.tgz`; 76 packed files.
- Tarball SHA256: `cc0577b00d5d995755881e1ebddd8fd43b7de6b6f1ce77ce1f622f5b9cddc31f`.
- BUILD_INFO SHA256: `f2a78da5174cb09db75dc9415ee9ca02b632c7369ae5fb1391c0e5bf4fd2b35f`.
- Installed-snapshot content hash from actual staged bytes: `dc2ad9ff4621689abe5739e0b6a497bac1199389e85687816fe5c25c11bcc9cc`.
- All 65 launcher asset digests match the frozen source observation (using the preserved generated minimal ESM package manifests); mismatch inventory empty.

Native binary SHA256:
- `native/linux-arm64/vecnet`: `9a2f7e98f70d0a52086917bce53016c7c908d752288ddef0a7ea604b28fdcbe7`
- `native/linux-arm64/vecnet-hub`: `03084fe5885d8c2cc07e7895a2b33ac8421164638fee42a51946804a3b090faf`
- `native/linux-x64/vecnet`: `f7abf061320c589f03e4ff7d22a7a97e31f9ead0395e0bc8518f8e3673233440`
- `native/linux-x64/vecnet-hub`: `aec9bf025607c67e27e938e524a1dc46f679b06c88da8d15e1b26d46de9818a4`

## S-I1/S-I2 status and limits

S-I1 focused actual RED -> GREEN evidence is unchanged: original trust RED accepted 11 unsafe cases; final focused suite after the manifest-publication fix passed 9 top-level tests and 13 unsafe/publication cases, with 1 foreign-owner case skipped because no privileges were available. Reused private snapshots pass. Existing targets are prevalidated before either inode is republished. `red-manifest-publication.log` proves the additional ordering regression before the fix; `green-go-publication.log` records GREEN. No chmod/chown repair of real paths.

S-I2 has now passed actual fresh packed installation, discovery, copied graph and native check boundary. The package kept the prior delivery tests and contains complete runtime/dynamic plugins/template. Source override remains supported. All candidate raw commands and before/after hashes are attached; package tests used private fixtures and never a real profile, daemon, credential or service manager.

## Drain / remaining main gates

All worker build/test children exited and private npm/hub/delivery fixtures drained. A post-suite process check found no remaining build/test or `/tmp/vecnet-npm-*` fixture processes. Slot was explicitly released to main before reporting/upload. No real profile or service changed; no deployment, publish, push or commit occurred.

This candidate is NOT the final release. Main still owns ACP fix integration, a new frozen-source final rebuild/package gate, combined Go/TS/UI/browser/live acceptance gates, and independent scoped review of fix1 plus Q2/ACP setup deltas. A later artifact cannot inherit these candidate hashes/results without its own source agreement and rebuild. No additional execution is authorized by this report.
