# Messaging ergonomics ledger — 2026-11-10

**Source and recorded gates are ready for review; merge, installation, deployment and live acceptance are pending.** This ledger copies recorded task state and rulings; historical component results are not substituted for final integrated gates.

Intended installation: `docs/plans/ledgers/2026-11-10-messaging-ergonomics.md`.
Full evidence and hashes: [messaging source receipt](../receipts/2026-11-10-messaging-ergonomics.md).
Local ignored evidence root: `.superpowers/sdd/2026-11-09-messaging-master/` (`E` below).

## Source identity

- Branch `ship/messaging-master`; master/base `8d2394d7896edacd4eadd437ca616a2243728fe3`.
- Candidate `5c37a2877994b1c6f068cad84c02bf14105f630a` plus dirty `CHANGELOG.md` and `docs/PROTOCOL.md`; index unchanged/unstaged.
- Full base-to-working-tree binary diff SHA256 `4f95806d2fedc93ac58e2276a8f0fa6a18823d3dfa25dfb48de8e0810c252dd7`; dirty two-doc diff `2e59db701bb3d43fce77b1efb11e87e814639fe354712cf3276b7eb5c2f01924`.
- Independent final review: exactly 23 planned paths, +1339/-53 = 1392 changed lines; 2209 frozen gate inputs.
- Linear ancestry `5c37a287 -> 7042540a -> aaf7a7ce -> 8d2394d7`; no native feature ancestry/merge imported.

## Actual task state

| Task | Recorded state | Evidence and limits |
| --- | --- | --- |
| A — recipient names | COMPONENT complete at `aaf7a7ce1df2ae61f87369b75c5f31c5b7b30ff8`; published to both recorded remotes by parent | 5 paths; genuine receipt RED preceded production; focused 12 and affected 3126 PASS, no failures/skips; independent Spec/Quality PASS C0/I0/M0 |
| B — CLI compact inbox | COMPONENT complete at `7042540aea8086f07ef88126372994a884f666f1`; published to both recorded remotes by parent | 4 paths; behavior RED accepted before production; focused 28 and affected 1590 PASS, no failures/skips; independent Spec/Quality PASS C0/I0/M0 |
| C — MCP compact inbox/fixtures | COMPONENT complete at `5c37a2877994b1c6f068cad84c02bf14105f630a` | 16 paths; focused 16, guard 5, affected 2290 PASS, no failures/skips; canonical guard RED preceded fixture authorization; independent Spec/Quality PASS C0/I0/M0 |
| D — bounded protocol/changelog | Earlier ignored afterimages installed by parent into the two dirty docs; closing receipt/ledger candidates prepared | Actual docs match prior candidates; closing candidates still require parent installation; no release/deployment acceptance |
| Final integrated gates | Parent records all required source gates PASS, terminal/drained | Full Go/race/UI/protocol/ISA/format/checksums/screens below; initial environment failures retained |
| Independent whole-branch review | Spec PASS; Quality PASS; C0/I0/M0 | `E/final-code-review-report.md`; source-only verdict, separate from execution receipts |
| Master/install/live | UNRUN/pending parent | No merge, installation, deployment or native approval follows from this ledger |

A/B/C counts are historical component receipts. Historical B affected 1590 includes 11 pure projection tests; C reconciles CLI 1579→1582 and MCP 695→708. These are not totals to add to the final suite. Per-component source pins and two empty owned drains were parent verified, as recorded in `E/progress.md` and component parent verifications.

## Contract rulings retained

- Receipt `recipient_name` snapshots describe actual transactional delivery targets; CLI quotes/sanitizes names beside full canonical IDs, including group/reply targets. Old/empty metadata uses `"name unavailable"`, without directory lookup or resend; replay preserves stored names.
- Names are labels, not a new addressing namespace. Alias input remains deferred.
- Compact CLI JSON and MCP tool/resource inbox views remove only delivery `message.loop.sender_chain`; CLI `--include-sender-chain` and MCP boolean `include_sender_chain=true` opt in to full ancestry.
- Resource ancestry consumers migrate to the scoped tool with the same peer/merged-principal scope; no resource query parameter. Blob URL stripping remains active in both MCP tool modes.
- Canonical storage/HTTP/SSE/push/MCP Events/thread/envelope/loop controls retain their contracts. Canonical-schema validators and client loop breakers must opt in or use canonical transport.
- Master keeps ordinary canonical message/thread cache placement. One buffered, error/count-checked output write precedes explicit ack; failed rendering/output or a short write cannot ack. Cache effects and partial written bytes remain possible.
- Both hub and CLI upgrades are required for the complete behavior; neither installed side has been accepted here.
- Canonical fixture updates remove only the authorized nested ancestry field: parent semantic evidence records 80 omissions with other semantic values/numeric lexemes preserved; both manifests retain 12 total checks. This author did not rerun a parser.
- Reference catalogue size pins/ceilings and canonical validation controls remain unchanged; final executable gates are attributed to the parent.

## Final recorded checks

All six successful final runs are on `x-boss`, actual exit 0 / bb 0, with 2209 unchanged source pins and two empty owned-process drains. Exact commands, lanes, worker counts and all eight raw hashes are in the companion receipt and `E/final-*/request.json`/`receipt.json`.

| Run | Recorded outcome |
| --- | --- |
| `final-go-r1` | 5944 named passing test nodes; 0 failures; package skips `cmd/vecnet`, `internal/testhub`, plus `internal/tui/TestWriteShots` |
| `final-race` | 3170 named passing test nodes + 4 package passes = 3174 pass events; 0 failures/skips |
| `final-ui` | 521 tests; 0 failures/skips/cancellations |
| `final-protocol-r1` | PASS with no actual skip; parent's literal-skip substring misclassification corrected without source change/rerun |
| `final-aux` | ISA, empty gofmt output, and both checksum manifests PASS (12 entries total); all four stages exit 0 |
| `final-shots` | 8 runner tests PASS; 413 PNGs indexed exactly once; parent checked one desktop image visually |

Retained failures: `final-go` exit 1/bb 1, 35 failing named CLI records, parent status `FAIL_ENVIRONMENT_ENOSPC_RETAINED`; `final-protocol` exit 23 during rsync ENOSPC transfer, no bb exit and no verified remote after-source. Corrected named runs are `final-go-r1` and `final-protocol-r1`; original failures remain failed.

Screenshot index: `docs/plans/shots/oui/README.md`, SHA256 `1e10677603709bcd7af8fddc292ee636d18422bd1c29fead386a16dafb5904e0`. Parent visual check of `cli-label-thread-desktop.png` found readable names, intact three-column layout, literal hostile title and no obvious clipping in that state. The other images have suite/index/hash coverage; they were not individually visually accepted.

Independent whole-branch source review SHA256: `b871849080ed32d74f5f91e97667229c78561ef359e7ac12b31d39ac9f3031ce`. Parent screenshot verification SHA256: `4309ef4604989c88b1e728e2e003e82c7dfab635cfc311b590f80718f56ba556`.

## Deferrals and release boundary

- Parent installation of this receipt/ledger, master merge, hub/CLI installation and live acceptance remain open. Source readiness grants no runtime/native authority.
- Carry forward public production baseline **Fly v55 / `8c93b0bf`** from the route-statistics receipt; no fresh production inspection occurred here.
- Pre-migration-35 deployment remains held for a fresh consistent external private backup, separate isolated restore with typed verification, and attachment/recovery coverage. A synthetic rehearsal is not production backup/restore acceptance.
- Native display-proof/session/claim/replay machinery, native completion/approval and alias input remain outside this independent port. No memory-size claim or new deployment result is recorded.
- This closing author used public text/Git/hash evidence only: no gate rerun, build, parser, runtime/browser/network/private-state access, communications or delegates. Existing product/docs/index/refs/screens and earlier candidates remain frozen.
