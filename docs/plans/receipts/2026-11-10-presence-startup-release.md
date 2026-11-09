# Presence TTL Settings and startup URL diagnostics — release evidence

Ready for review. This integration combines the local Grok presence preference/Settings component d5aa1080 and startup URL diagnostic component ab914c83 on master 5fb55edc. Candidate 7b5659e6 keeps both independently reviewed component contracts.

Grok TTL can store an override even when its raw value equals the inherited value, and can reset to inherit again. Confirmation describes the resolved presence change. The List and Policy editors distinguish committed policy from draft values and preserve the narrow flag override rules. Startup help/parser diagnostics omit environment URL values; normal startup and explicit empty flag overrides retain their intended behavior.

| Parent-owned x-boss gate | Observed result |
| --- | --- |
| Full Go, go test -json -p 2 -timeout 40m ./... -count=1 | PASS: 5228 test pass events; actual exit 0, bb exit 0. See raw for nested-event counts and skip attribution. |
| UI unit suite | PASS: 521 tests, zero failures/skips/cancellations; actual exit 0, bb exit 0. |
| Protocol validation | PASS: schema/example positive and negative fixtures plus dependency checks; no skipped jsonschema. |
| Canonical full screenshot suite | PASS: all journeys and exact 413-name index; actual exit 0, bb exit 0. |
| Supplemental Settings capture | PASS: canonical Settings assertions retained, 98 actual frames including 24 relevant focused frames, then 100 frames adding two ready mobile Save-bar views; separate helper/source attribution. |
| Fresh independent Sol source review | Spec PASS, Quality PASS, C0/I0/M0 across the complete 20-path combined scope. |
| Independent final raster review | See visual review for exact acceptance and static-image limits; parent inspected actual relevant images. |

All gates verify the same 2,126 source entries before and after, compare actual subprocess status with the bb footer, preserve raw SHA-256, and finish with two empty owned source/TMP process censuses. The final Go suite exclusively reserved the global two-job x-boss cap. Other suites used one job/private synthetic HOME/XDG/CODEX/physical TMP.

Raw SHA-256: Go `e6030377b5d0a2b68d50b34d205bdbff45cc0337220077191d354a84fbf20e70`; UI `545333a805f128d838165b1a4d872b3b8163fe7476a76243c129b54881d6b63d`; protocol `ca5a30f72f819cccc3cec382ec2d1b69ec1ab81e1721a8e59791f9369f2e50a9`; screenshots `fa019016864492e398fb61f5da21be6c84988eae1793763fd35584c9e0243490`; supplemental `93d3ca68a1b15a7ba205d75081ab9b4833751f31e72615c4813f6915ba0ab4f8`; final two-action capture `dd97c04845959c33de1b70d5fe6344a6878287c5961967e295f4e61a701060bc`. Machine receipts and full source maps remain in the plan-scoped ignored workspace.

Historical failures remain preserved: the component screenshot suite completed all 413 images but failed the strict index because 20 new names were absent. Only those index links were added, and the combined canonical repeat passed. The first independent raster review found missing framing evidence without identifying a product defect. A reviewed supplemental helper scrolled real controls into view and added four default-TTL flag frames plus two separate ready phone Save-bar frames; no production code, DOM/CSS content, assertions, expected values or canonical screenshot was altered by that helper.

Only closing documents and PNGs change after these gates. Executable source equivalence is checked before merge. Clean-master build, atomic CLI/hub install, dev-hub restart and serial Fly deployment follow, with live commit and changed Settings asset checks. Final operational results are retained in the controller workspace and subsequent deployment receipt.

[Source review](2026-11-10-presence-startup-review.md) · [Visual review](2026-11-10-presence-startup-visual-review.md) · [Ledger](2026-11-10-presence-startup-ledger.md) · [Canonical screenshots](../shots/oui/README.md) · [Focused frames](../shots/presence-ttl-review/README.md).
