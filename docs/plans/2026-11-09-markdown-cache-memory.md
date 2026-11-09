# Markdown cache retained-input budget

**Goal:** stop the document-wide parsed-body memo retaining up to 1000 arbitrarily large message bodies while preserving full rendering. This is WEB-MEM-4 under issue #60.
**Base:** 3d06953f, isolated fix/webui-markdown-memory. **Status:** implementation and parent focused/full-UI/embedding gates pass; fresh final independent source review PASS C0/I0/M0; ready for source publication. No master merge/deployment or incident closure.

## Global constraints

Keep the existing 1000-entry FIFO limit and add 256 × 1024 UTF-16 units of aggregate String-normalized cache keys. Charge raw keys before parser line-ending normalization. Hits retain FIFO position without charging twice. Oversized inputs render fully and bypass cache without evicting existing entries. Preserve the parser, safe links, inert HTML, full plain rest, reusable parsed data and fresh DOM. This bounds logical retained input, not AST/native/tab memory. No TTL, LRU, configuration, dependencies or cache introspection API.

Tests/builds run only on x-boss, aggregate two jobs, private synthetic HOME/XDG/CODEX_HOME and physical temporary paths. Source workers use explicit GPT-6.1-Sol and no executors/private state.

## Review focus

- One admission requiring multiple FIFO evictions must remove every required victim before re-admission can mask it. Pinned by WEB-MEM-4 one admission can evict multiple old keys and reclaim their charges; parent proved the corrected oracle rejects single-eviction and double-subtraction mutants.
- Budget bypass must render the complete oversized input safely while preserving earlier cached keys. Pinned by the oversized normalized-input regression and existing parser/DOM security controls.

## Task 1: Bound retained memo keys

Files: internal/ui/static/md.js and new internal/ui/jstest/md-cache-lifetime.test.mjs.

- [x] Write ten behavioral regression/control cases around actual markdown(), real parse and real h(); instrumentation changes only import specifiers and observes parse calls.
- [x] Parent observe genuine retention RED: seven new failures, sixteen controls passed.
- [x] Add only retained-unit accounting, oversize bypass and repeated FIFO eviction. Preserve original parser and rendering.
- [x] Parent GREEN23/full UI488/Go embedding10 pass at final executable hashes, zero skips/cancellations; raw/source manifests and two empty process drains retained per run.
- [x] Independent source review: production SpecPASS, initial oracle QualityFAIL I1; same scoped reviewer accepts corrected oracle C0/I0/M0 after actual six-case old/new mutation proof.
- [x] Fresh final component source review PASS C0/I0/M0; targeted source publication and integration handoff authorized. Full merged-tree release gates remain outside this source component.

R-M41: use 256 Ki logical UTF-16 key units plus the original entry ceiling; no whole-memory claim. R-M42: parent appended a missing standard report sentinel with before/after hashes, no product effect. The review I1 was a test-oracle defect, not a second production defect. Initial mutation fixture run failed missing transitive imports; raw failure is retained, and the corrected named run adds only format.js and peer-model.js to scratch fixtures.

--- SUMMARY ---

Bound document-wide memo input retention and skip oversized cache admissions while keeping complete message rendering. Parent gates and independent review establish this component; issue #60 still needs integration, thumbnail ownership work and browser/native-memory evidence.
