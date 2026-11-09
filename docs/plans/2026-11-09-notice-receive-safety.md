# Hub notice receive safety — Batch 1

Goal: reject malformed notice URL fields before operator clients return or apply them, and provide safe recovery details for hub relocation errors.

Base: isolated `8439354f`, which includes the separately reviewed HTTP diagnostic boundary. This is Batch 1 of issue #47's wider received-URL work. Native release state and other CLI/TUI/notice consumers remain separate integration work.

The internal hubnotice helper validates the required Hub and each nonempty NewURL through the shared validator. Operator adapters explicitly retain their existing allowance for plain HTTP. Complete notice lists and tail pages are validated before return/restart/apply/ack/accepted/cursor effects. Only `410/hub_moved` envelopes receive a fixed safe message and validated allowlisted recovery fields; valid signed notice values survive unchanged. Arbitrary notice prose and cryptographic authenticity remain governed by their existing contracts.

Constraints: explicit GPT-6.1-Sol producers/reviewers, parent-owned x-boss execution with global two-job cap, private synthetic HOME/XDG/CODEX roots and identity-checked physical TMPDIR. Test-first frozen regressions, current-source raw/manifest/drain verification and independent review are required. Real configurations, credentials, destinations and live services stay outside these component checks.

Review focus: malformed structural fields and URLs in cleared/nonmoving notices; whole-page atomicity before any effects; exact valid HTTPS/HTTP, prefixes/IPv6 and signed payload preservation; safe relocation recovery fields without changing unrelated typed errors or transport causes.

- [x] Parent genuine RED: 399 failing leaves and 76 preservation controls, all three new roots exercised.
- [x] Implement exact shared helper plus operator list/tail/error receive adapters.
- [x] Correct only URL fields in the inherited rich decoding fixture; preserve all generated metadata and every assertion.
- [x] Parent current full client gate passes, including all 478 focused checks, with source/raw/drain evidence.
- [x] Fresh independent production review and same-reviewer fixture supplement pass.
- [ ] Integrate all required issue #47 consumers, run combined full release gates/screenshots, and verify installed behavior.

R-I47N2: the original conductor producer stopped before source writes because its model could not be attested. Preserve that report and use a fresh native process whose actual Sol/high header the parent verifies. R-I47N3: the inherited rich fixture uses generic strings as URLs; replace only those fields with valid exact synthetic values. Both rulings preserve production validation and the frozen behavioral oracles.

[Component receipt](receipts/2026-11-09-notice-receive-safety.md). This component is ready for integration review; issue #47 remains open.

## Batch2 amendment — 2026-11-09

R-I47N4 permits the independently reviewed TUI formatter slice before remaining CLI policy/receive work. Both existing formatter boundaries now validate known structured notice fields before presentation, retaining accepted output and all unrelated fallback behavior.

- [x] Genuine behavior RED, current 348 focused and 906 full TUI passes (one existing opt-in screenshot skip), and independent Sol Spec/Quality PASS C0/I0/M0.
- [x] Canonical test formatting preserves the original fixtures and oracles under R-I47N5.
- [ ] Remaining CLI normal/JSON/hook/watch, construction-policy and follow/enrol/preferences boundaries, combined integration gates and installed behavior.

[Batch2 component receipt](receipts/2026-11-09-notice-tui-formatting.md). Issue #47 remains open.
