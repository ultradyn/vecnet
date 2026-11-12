# Messaging ergonomics source receipt — 2026-11-10

**Source and recorded gates are ready for review. Merge, installation, deployment and live acceptance remain UNRUN.** This receipt records parent-owned execution and independent source review; its author performed public read-only text/Git/hash checks only and did not rerun gates.

Intended installation: `docs/plans/receipts/2026-11-10-messaging-ergonomics.md`.
Evidence root (repository relative): `.superpowers/sdd/2026-11-09-messaging-master/` (called `E` below; ignored local evidence, not guaranteed to accompany a published checkout).

## Reviewed source

- Branch: `ship/messaging-master`; master/base: `8d2394d7896edacd4eadd437ca616a2243728fe3`.
- Candidate HEAD: `5c37a2877994b1c6f068cad84c02bf14105f630a`, plus unstaged `CHANGELOG.md` and `docs/PROTOCOL.md`; no staged changes.
- Dirty two-document binary diff SHA256: `2e59db701bb3d43fce77b1efb11e87e814639fe354712cf3276b7eb5c2f01924`.
- Full base-to-working-tree binary diff SHA256: `4f95806d2fedc93ac58e2276a8f0fa6a18823d3dfa25dfb48de8e0810c252dd7`.
- Independently reviewed scope: exactly 23 planned paths, +1339/-53 (1392 changed lines); frozen gate inventory: 2209 source entries.
- Linear parent edges: `5c37a287 -> 7042540a -> aaf7a7ce -> 8d2394d7`. This is an independent master port with no imported native branch ancestry or merge.
- Actual PROTOCOL SHA256: `b47ac3d796993531085b7c61d887cb260559bda5cc1067db168268b029559138`; CHANGELOG: `4b4275893d929b4d4182a167b35d263487138651a1a4cb8d2000f7312cf8ed3f`. Both match the earlier ignored Slice D afterimages.

## User-visible contract and upgrade requirement

Send/reply responses add optional `recipient_name` snapshots within the delivery transaction for every actual target, including group fan-out and current reply participants. CLI text quotes/sanitizes the receipt name beside the **full canonical recipient ID**. Missing/empty old-hub metadata or old idempotency rows display `"name unavailable"` without a directory lookup or resend. Replays retain their stored snapshot; fresh sends can reflect renames. Names remain display labels; alias input is deferred.

CLI JSON inbox and MCP inbox tool/resource views are compact by default: only delivery `message.loop.sender_chain` is removed. CLI `--include-sender-chain` and MCP boolean `include_sender_chain=true` restore ancestry; the MCP field defaults to false. CLI text opt-in appends the ordered JSON array only on delivery rows. Resource consumers needing ancestry must migrate to the tool with the corresponding peer/merged-principal scope; resources gain no query argument. MCP blob URL stripping applies in both tool modes.

Canonical storage, HTTP/SSE/push, MCP Events, threads/envelopes and loop controls retain their contracts. Canonical-schema validators and client loop breakers consuming compact views must opt in or use canonical transport. Master retains ordinary canonical message/thread cache placement, buffers a page, then performs one error/count-checked output write before explicit acknowledgement. Render errors and failed/short writes do not ack; cache effects or partial output bytes can already exist.

**Both hub and CLI upgrades are needed:** hub changes supply receipt metadata and MCP projection; CLI changes supply name rendering, local projection and the flag. Updating one side alone does not establish the other side's behavior. These source results do not prove either installed binary or a live endpoint.

## Parent-owned final gates

All runs below target candidate HEAD plus the two dirty docs on `x-boss`. Successful final receipts record actual exit 0, `bb: exit=0`, unchanged 2209-entry source pins, and parent verification records two empty owned-process drains. Jobs are per recorded request, with at most two aggregate workers. Commands are recorded here as evidence, not instructions executed by this author.

| Run | Lane | Jobs | Actual command | Recorded result |
| --- | --- | ---: | --- | --- |
| `final-go-r1` | `cos-msg-final-go-r1` | 1 | `go test -json -p 1 -timeout 40m ./... -count=1` | 5944 passing named test nodes; 0 failures; 3 skip records below |
| `final-race` | `cos-msg-final-race` | 1 | `go test -race -json -p 1 -parallel 1 -count=1 -timeout 45m ./internal/inboxview ./internal/hub ./internal/cli ./internal/mcpserver` | 3170 passing named test nodes + 4 package passes (3174 pass events); 0 failures/skips |
| `final-ui` | `cos-msg-final-ui` | 2 | `just test-ui` | 521 tests; 0 failures/skips/cancellations |
| `final-protocol-r1` | `cos-msg-final-protocol-r1` | 1 | `just validate-protocol` | PASS; no actual skip; schema/negative examples, R2/blob and CLI dependency checks succeed |
| `final-aux` | `cos-msg-final-aux` | 1 | `python3 -c` with the stage payload recorded below | All 4 stages exit 0; empty formatting output; ISA checks pass; 12 manifest entries OK |
| `final-shots` | `cos-msg-final-shots` | 2 | `just ui-shots` | 8 runner tests pass; exactly 413 indexed PNGs |

Full Go skips are package-level `cmd/vecnet` and `internal/testhub` records (neither has a Test field), and `internal/tui/TestWriteShots`. Named counts include subtests and are not independent top-level case counts. The full Go raw log has 5983 pass events including package events; race's 3174 total must not be reported as 3174 named tests.

The initial parent protocol substring check matched a literal recipe skip branch; `final-protocol-r1/parent-verification.json` corrects that interpretation without a source change or gate rerun. No executed skip occurred in that successful run.

Auxiliary argv and exact payload are retained in `E/final-aux/request.json`, SHA256 `d9d6b89b72837163f9e763ae714e4c05983ccc3580eb4b1784e650f0e1a94df4`. Its `python3 -c` payload runs these commands in order and fails on any nonzero stage or nonempty formatting stdout:

```text
just isa-check
gofmt -l internal/cli/commands.go internal/cli/messaging_ergonomics_test.go internal/hub/messaging_ergonomics_test.go internal/hub/send.go internal/inboxview/project.go internal/inboxview/project_test.go internal/mcpserver/descriptions.go internal/mcpserver/guide.go internal/mcpserver/messaging_ergonomics_test.go internal/mcpserver/reference_guard_test.go internal/mcpserver/resources.go internal/mcpserver/sender_info_test.go internal/mcpserver/tools.go
sha256sum -c SHA256SUMS  [cwd: internal/mcpserver/testdata/reference_guard]
sha256sum -c SHA256SUMS  [cwd: internal/mcpserver/testdata/reference_guard/resources-read]
```

## Retained failed runs and corrected runs

`final-go`, lane `cos-msg-final-go`, jobs 1, used the same full-Go argv as `final-go-r1`: actual exit 1 / bb 1, 5909 passing named test nodes, 35 failing named records in `internal/cli` plus its package failure, and the same three skips. The parent classified it `FAIL_ENVIRONMENT_ENOSPC_RETAINED`; its original receipt remains failed. `final-go-r1` is the separately named corrected full run.

`final-protocol`, lane `cos-msg-final-protocol`, jobs 1, used `just validate-protocol`: actual exit 23, no bb exit recorded, remote-source-after verification false, local source unchanged. Its raw log records rsync receiver ENOSPC and transfer code 23; it is not a schema-validation failure or a completed validation gate. `final-protocol-r1` is the separately named corrected run. Neither initial failure is erased or relabeled PASS.

| Raw log under E | SHA256 |
| --- | --- |
| `final-go/raw` (retained FAIL) | `a5de382decc8af4f20556db0f103cef050fa0f63b4aeee1a034ce8409137fc8b` |
| `final-go-r1/raw` | `8a2c9f0071a37b07b07f6343bf89ce7113284596c17f58988487d665a13e6a9b` |
| `final-race/raw` | `d38e328898f8cdff4aa6a06ee732345c857646017581b1e056bc460716e3dc93` |
| `final-ui/raw` | `01cb0431ddea3c7050e8f2e44c4cd862b65780215247dd2c60613340acf09d5f` |
| `final-protocol/raw` (retained FAIL) | `76e747ea5f2ed84fb1866bc6a51135ad41a45719ddd42526a38f0896c7e9284a` |
| `final-protocol-r1/raw` | `8ae81208974d76d23df1fd475f0ec487fb5a3977d5c34adf943803d2ab7ecf2d` |
| `final-aux/raw` | `3fefda83c78312266c8332f5d69a7a0f03069f831801b61c171cf609b97e9f54` |
| `final-shots/raw` | `27d39074ee7eb8162799ccd8df5e03796ccb4789c0c01a8ba8d4b163a288e659` |

Each run retains `request.json`, `receipt.json` and raw output; successful runs also have parent verification, with screenshots verified by `E/final-shots-parent-verification.json`.

## Independent review and screenshots

`E/final-code-review-report.md`: **Spec PASS; Quality PASS; Critical 0 / Important 0 / Minor 0**, SHA256 `b871849080ed32d74f5f91e97667229c78561ef359e7ac12b31d39ac9f3031ce`. The independent whole-branch source trace covers receipts/replay, projection, cache/render/ack, MCP scope/blob/discovery, docs and fixture provenance. It certifies source review only; execution is supported by the separate parent receipts above.

`docs/plans/shots/oui/README.md` indexes all **413** generated PNGs exactly once; index SHA256 `1e10677603709bcd7af8fddc292ee636d18422bd1c29fead386a16dafb5904e0`. The corresponding retained images are in `E/final-shots/shots/`. Parent verification SHA256: `4309ef4604989c88b1e728e2e003e82c7dfab635cfc311b590f80718f56ba556`.

The parent visually inspected **`cli-label-thread-desktop.png`**: readable destination names, intact three-column thread layout, hostile title rendered literally, and no obvious clipping in that captured desktop state. This is one visual check, not an exhaustive inspection of all 413 images; this author performed no additional visual or runtime check.

## Rollout boundary and hold

The public [route-statistics receipt](2026-11-09-route-statistics-current-master-gates.md) records production on **Fly v55 / `8c93b0bf`**. That remains the carried-forward production baseline; no fresh production inspection was performed for this receipt. The pre-migration-35 deployment hold remains: a fresh consistent external private backup, a separate isolated restore with typed verification, and attachment/recovery coverage must be verified before migration/deployment.

Source and gates are ready for review only. Master merge, receipt/ledger installation, hub/CLI installation, deployment and live acceptance remain pending parent action. Native actor completion/approval, native display-proof/claim/replay machinery, alias input and production backup/restore acceptance are outside this source receipt. No memory-size or native runtime claim is made.
