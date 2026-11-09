# Bounded thumbnail transport — Task 1 component evidence

Status: Task 1 complete on isolated fix/webui-thread-memory based on 3d06953f. No caller activated, master merge, deployment, browser measurement or whole-issue #60 closure.

Parent-owned x-boss / cos-wm3 runs use one job, private synthetic HOME/XDG/CODEX_HOME and an identity-checked physical 0700 TMPDIR. Each listed final run has actual exit 0, bb exit 0, unchanged complete source manifests, zero failures/skips/cancellations and two empty exact-owned process drains.

| Gate | Result | Raw SHA256 |
| --- | --- | --- |
| Focused actual createApi tests | 50 passed | 3c89471af2da713a8f573118421c0b1190f060f1b555eafdb1756fec240b6f2e |
| Full UI JavaScript suite | 504 passed | 3410e34c8f14ef9fbca86bc3f940325e677cfdbcfde38b6b648962035d051edc |
| Relevant Go UI embedding/CSP | 10 passed | 72316083e3d154e65bb5eadb4f0eb7c05041ee13b1460bbb36a7f45af2e969a9 |

TDD: 23 new missing-method regressions failed before implementation while 24 existing controls passed (raw f2c7c8e2f08aa5842d8fc058fbdf965fc949eaf53aaccdc79f29998d63477480). Independent review found I1 valid-envelope normalization and I2 unsupported-body cancellation defects. New fix1 tests reproduced both failing roots with 48 passing controls, including abort-during-cleanup coverage (raw c3a7d36081ed45d437930dfaf9f795946a4b02526c3bc0db4ad3797109492d4f). The failing I1 root stopped at its first thumbnail assertion; not all loop scenarios are claimed individually RED.

Independent GPT-6.1-Sol source review initially failed C0/I2/M1, then the same saved reviewer returned SpecPASS/QualityPASS C0/I0/M0 with each finding ADDRESSED. It also reviewed the authorized 16 KiB asset-cap increase and preserved all-files guard. An earlier real embedding size-cap failure is retained in scratch; it was not a passing gate.

Current executable pins:
- internal/ui/static/api.js: `8e1d451beaf5d41762c13b68bb9ba10985859de29a56d781e44206a9c0e264d4`
- internal/ui/jstest/api.test.mjs: `a3aeb4c66aab845001cfd221b02e694163d082d09edeaf56c96998eb5fe75167`
- internal/httpapi/ui_test.go: `9743f436f71099d251214b8ab060654f8ef1e9587caa8d2511ba9a0b1a2ff41f`

Raw requests, receipts, full source manifests, failed runs, review packages and process drains are retained in .superpowers/sdd/2026-11-09-webui-thread-memory. Logical transport bounds do not establish native browser RAM or cancellation time bounds. Tasks 2–3 still own admission/cache/DOM activation and browser evidence.
