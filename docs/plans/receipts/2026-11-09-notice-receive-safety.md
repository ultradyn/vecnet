# Received hub notice safety — component evidence

Batch1 introduces shared strict URL validation and whole-list/page rejection at the operator-client boundary. Tail ordering and unrelated error/cause behavior remain intact; relocation errors carry safe fixed messages and only validated recovery details.

| Parent x-boss check | Result |
| --- | --- |
| Genuine original RED | 399 failing leaves, 76 preservation controls; all 3 roots failed, actual/bb exit 1 |
| Focused GREEN | 478 test passes, no failures or skipped tests, actual/bb exit 0 |
| First full clients | Failed only the inherited rich notice decoding fixture (generic non-URL fields); preserved raw and disposition |
| Corrected current full opclient + hubnotice | 662 test passes including all 478 focused checks, no failures or skipped tests; helper package has no standalone test files; actual/bb exit 0 |
| Fresh independent explicit Sol source review | Production contract PASS; I1 inherited-fixture conflict accepted |
| Same independent Sol fixture re-review | Spec PASS, quality PASS; I1 addressed, no new findings |

All 2,157 source entries matched before/after/current execution; actual raw hashes and two empty owned-process drains were verified. Synthetic homes and physical owned temporary paths were used with one job. Current full execution covers the unchanged focused production/oracles plus the URL-field-only inherited fixture correction. Complete generated fixture metadata and exact decoding/request assertions remain intact.

Raw SHA-256:

- Original RED: `2bc5a4f12b867679108d7cc1c7bcbc3db85366aa314f4b10f1893f0b08e3dc1a`.
- Focused GREEN: `e1564b1bc2ded4a686b0dc0d0b71a268709a00cc48e0f2dcfb1c48d377fea302`.
- Preserved full failure: `cc1cc9c3708526441bfb388cf104a7e4ba545c4a83f1f57a9c7cf8410d74a0ac`.
- Corrected current full: `ee565f3c05f4fceb2db86e1de8f886a2b84b595ad8ded3c070c5c7c7b804c8b9`.

The earlier producer's NEEDS_CONTEXT/model-provenance report remains preserved; it made no production edits. The replacement producer and independent reviewer model headers were verified by the parent as gpt-6.1-sol/high. Reports distinguish source review from execution evidence.

Remaining issue47 CLI/TUI/notice receive/relocation consumers, combined release gates/screenshots and installed behavior are pending. No master merge, installation, deployment, destination I/O, credential or migration change is established by this source component.
