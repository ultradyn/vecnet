# Fresh whole-change reviews — pending thumbnail cancellation

Source `27529818041bf5f589ea24797ab2de180aab8517`; baseline `c5f6429dfc12927670e18c13ce149f5284070751`. Two fresh independent GPT-6.1-Sol/high reviewers, separate contexts.

Spec PASS — C:0 / I:0 / M:0

Independent SPEC verdict for the bounded GitHub60 Task1 component. Inspected actual `git diff c5f6429dfc12927670e18c13ce149f5284070751...HEAD` and corresponding `git log BASE..HEAD`: one commit, `27529818041bf5f589ea24797ab2de180aab8517`, four files, +134/-2. Current tracked source exactly matches that commit; no superseding fixes. No other final-axis report read.

Findings: none; no missing, wrong, partial or unasked requirements within Task1. No accepted minors remain; finding ID/location/Scenario–Effect/evidence/fix-check entries are therefore inapplicable.

Checked and fine:

- **Ownership/contracts** (`brief-task1.md:6–8`). Actual route→thread→thumbs teardown reaches cache release (`app.js:254`, `thread.js:770`, `thumbs.js:131`). `thumbs-model.js:40,55,92–97` owns cancellation independently, marks dead before abort, revokes URLs and releases idempotently. `api.js:165–183,335–362,482` preserves legacy signatures and R2 verification/security while forwarding the signal through actual body consumption. Late loader/SVG guards, eligibility and callbacks remain intact.
- **Regression** (`issue.json:1`, “fails before the bounded fix”; brief:8). `thumbs.test.mjs:293–379` exercises cache→API→native streamed HTTP, with no server timer. It requires client closure/native rejection before cleanup, preserves a same-SHA live owner, and checks deduplication/release behavior. Archived RED product bytes match BASE; tests match HEAD: four behavioral failures/40 controls. GREEN records 44/44; canonical UI 650/650; corrected Go gate two named tests. Raw log hashes match; setup failure remains distinct.
- **Browser binding** (brief:12–14). Independently hashed old3/new3 seven-helper archives; policies match, actual 2,006-file manifests differ only in the two product paths. All 80 served pins agree. Minimized candidate replay matches original lifetime-06 helper bytes. Final records show seven known PID identities absent, three refused listeners and drained proxy/directories.
- **Measured criterion/functional preservation.** Raw old3 ends held3/+663 nodes/+99 listeners; new3 has held0/node+0/listener+0 after every unmount, client closes before teardown. Numeric criterion and separate heap/native figures match `receipt-draft.md:29–47`; native highwaters increased slightly. Parent functional log/source pins and five screenshot hashes match: PNG/SVG, viewer/focus, retry/draft controls pass. Functional retaining observers supply no memory proof.

Limits/cannot verify: historical execution independently replayed nowhere here; no new visual inspection, live R2 partial-body trial, heap retaining roots, native/GB reduction, unlimited plateau or installed/deployed acceptance. Old8 failed quiescence; not completed evidence. Wider `issue.json` acceptance remains explicitly OPEN (`receipt-draft.md:79–81`), correctly separate from component acceptance. No broad WIP import or closure justified. Read-only analysis except this report; no runtime execution or children.


Quality PASS — C0 / I0 / M0

Independent standards/quality verdict for the bounded GitHub #60 component. Findings: none; no accepted minors or other findings await fixes. No known critical/important issue found in this pass.

Source: HEAD is exactly `27529818041bf5f589ea24797ab2de180aab8517`; clean working tree at intake. Inspected actual `git diff c5f6429dfc12927670e18c13ce149f5284070751...HEAD` and corresponding `git log ... ..HEAD`: one commit, four files, +134/-2. No superseding task fixes. Read all requested task/controller documents and receipt draft; did not read the other final-axis report.

Checked and fine:

- Standards: GLOSSARY.md, ADR 0001, PROTOCOL attachment/security §§2.7,11,14 and actual module conventions. Scoped filename searches found no CODING_STANDARDS, CONTRIBUTING, AGENTS.md or CLAUDE.md. No documented violation or actionable baseline smell; smell assessments are judgment calls. Native browser cancellation and shared test-fixture logic add no dependency or speculative abstraction.
- Lifecycle: `thumbs-model.js:40,55,92-99` owns one controller per cache, marks dead before abort, releases idempotently, revokes URLs and clears entries. Existing late-loader/SVG guards prevent callbacks/URLs after disposal. Traced app unmount → thread → thumbs → cache; independent owners remain independent.
- Compatibility/security: `api.js:165-183,335-364,482` forwards signals through native body consumption and existing R2 discovery/grant/provider reads. Existing signatures, aborted APIError classification, authorization, media/size/hash checks and credential/origin protections remain intact.
- Tests: `thumbs.test.mjs:285-380` uses real partial HTTP bodies without server release timers; observes native readers, requires closure before cleanup, checks deduplication and independent-owner completion. Deadlines bound polling; finally releases caches, destroys owned sockets and awaits listener shutdown. API wrapper cases protect both dispatch branches.
- Evidence: independently matched archived RED product to base and tests to current bytes; verified nine log hashes/exits/stable manifests. Raw RED is four behavioral failures; GREEN 44/44, canonical UI 650/650, corrected narrow Go two named passes. Matched old/new helpers, 2,006 remote hashes and 80 served assets agree; only two product files differ. Three candidate cycles retain zero obsolete bodies/additional nodes/listeners; recorded drains complete. Parent functional source/log/screenshot hashes agree.

Limits/cannot verify: no runtime gates repeated; historical observations are archived evidence, not independent runtime attestation. No fresh visual inspection, real-provider partial-body R2 gate, exhaustive failure/crash matrix, unlimited plateau, heap-root/GB attribution or native-memory reduction established. Full incident remains OPEN. Only this report was written.
