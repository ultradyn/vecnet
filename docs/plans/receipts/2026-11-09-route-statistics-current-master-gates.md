# Current-master route statistics gates — 2026-11-09

Candidate `2f3813170693814e30f55c2e868c7c0deb199002` integrates master `8c93b0bfdb0b4db4210f9ffde1c61e9ec39d28ae`. This is the independently reviewed replacement for PR #3, published as PR #65. It imports the selected route-statistics feature without the historical native-launcher branch.

The 35 selected source/test/documentation paths and three earlier public receipts were tested as one frozen inventory of 2,203 inputs. All 34 deployed migration files retain their bytes. Migration `0035_route_attempts.sql` is additive; fresh/upgrade/old-reader compatibility passed. Component and whole-feature GPT-6.1-Sol reviews passed with no open Critical or Important findings. The controller accepted exactly 87 additional test lines above the initial aggregate allowance; the selected source delta is +6,587/-18, excluding receipts and screenshots.

| Gate on x-boss | Observed result |
| --- | --- |
| Full Go suite | PASS, 5,888 passing named test events and no failures; one intentional `TestWriteShots` skip and two package-level skip events |
| Full UI tests | PASS, 521 tests |
| Canonical launcher strict TypeScript and units | PASS, 1,828 tests; includes hub-client inputs |
| Launcher E2E | PASS, 16 tests against the real candidate hub and fake backends |
| npm build/pack/check | PASS, extracted runtime package coverage and six release checks; no publish |
| Protocol, vet and CLI dependency check | PASS; CLI does not depend on SQLite |
| Candidate static binaries | PASS; source/version pins recorded |
| Actual old/new-binary compatibility | PASS; four offline phases preserve old state and additive migration compatibility |
| Canonical screenshots | PASS, 413 PNGs indexed exactly once; all hashes retained |
| Operator backup-helper rehearsal | PASS, actual static helper, live WAL sentinel, separate exact restore, 14 calls including ten refusals and one retained incomplete resource-failure artifact |

The successful final wrappers retained actual child status, `bb: exit=0`, before/after source hashes and two empty owned-process drains. The full UI child passed 521 tests with child exit 0 inside the initial combined Node wrapper, whose later launcher fixture failure produced `bb: exit=1`; that retained UI result is the explicit wrapper-status exception. All execution ran on x-boss with at most two aggregate workers and private physical homes/temp roots. The normal Fly remote-only deployment build remains the separately recorded rollout exception.

The first combined Node runner failed one credential-mode fixture because its inherited umask 077 changed the fixture's requested 0644 to 0600. Its raw failure is preserved (`c0545d6478ed3282bcc6fe58131fbb2ae9bf64f78982b1f09e7471ad6f26a8cb`). The corrected runner retained private 0700 roots and used umask 022 inside synthetic launcher/npm tests. No product or test source changed; the corrected strict/unit/E2E/npm sequence passed. The original UI 521 PASS remains attributed to its original run.

Eight current images were visually inspected: routes, loaded dashboard, thread, launcher dashboard, phone notices, isolated activity error, and Dot onboarding at desktop/narrow widths. Other PNGs received suite/index/hash coverage and were not individually inspected. The retained images accompany this receipt.

The backup helper's build SHA256 is `cb4b955ed11e2c2f0f95e991b4f9da1e69850258940a5a3437d64ee86255eaaf`. Its source and harness passed independent review and actual synthetic WAL/restore/refusal tests. This is a rehearsal, **not** a production backup or restore receipt.

## Rollout status

Source gates are ready for review. Production remains on Fly v55 / `8c93b0bf` at this receipt. The production migration/deployment is held until a fresh consistent external private backup, separate isolated restore and attachment/recovery coverage are verified. Fly Machines API reads work; SSH/SFTP transport authentication is under diagnosis. No production migration, install, changed storage policy or native actor approval is inferred from these gates. Original PR #3 remains open until replacement delivery is verified or a separate delivery disposition is recorded.

## Retained raw gate hashes

- `final-go`: `a2abff3ba03e672e13752e3e03902c1d5508637cf2aa7c5639ef42621babdbe0`
- `final-shots`: `742baef795b22e30f3cf3ce038d60bb8a6e4c6fe95bc95d52df50ac3fe478ad8`
- `final-build`: `d77d74d9840e0afaf742800683bc168b37daf8fcae22bd4b73064c02d26396f1`
- `final-g10`: `6f1c6746932997b21b829425b62a7f1dc61c87734d62c98d4b81578c895655e1`
- `final-node-r1`: `ce3c8d03d0e96c4cf141f4494151f78762174ef69afe906f9b2c520ad79c979f`
- `final-checks`: `5078d175c88f3d72a666ff0016afc376072d39e3b609cb84de6b55bf55505a85`
- `final-backup-proof`: `6d574de9857dfd8422f838879f09b95ec8b38460bbc60ba52d0731fad9bf156b`
