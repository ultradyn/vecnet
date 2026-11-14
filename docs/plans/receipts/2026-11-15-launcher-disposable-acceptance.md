# PR55 disposable native/ACP acceptance

Actual acceptance ran locally on Linux using build-box-produced binaries, with no local compilation. Source HEAD remained `b509a62aef982f233cb9a2e653eafca6d6c75033` throughout the matrix; it contains the reviewed runner fixes through `e90ce9867805b6407cb405a3ed902c680128d975`. Each scenario used a fresh private `/var/tmp` HOME, CODEX_HOME, XDG directories, profile, repository, backlog and generated launcher unit. Real user HOME, profiles and services were not used as deployment targets.

Binary identities:

- CLI: `6922e3a3df907241cb5b19b9e2b1b6ea63284f59bc038c5b3e0baac4ba21183c`
- Demo: `c64071e75f787cbbd3727801d4924c97e0b207f28d51f8c24a6898a7c2cda141`

Both were copied from `.scratch/merge55/final-artifacts/` into each fresh 0700 run directory, with executable copies mode 0700. Manifest hashes were checked against the source/runtime inventory before launch. The native binary was the pinned cached Codex 0.166.0 direct app-server; no daemon manager or updater was invoked.

## Results

| Scenario | Actual result | Private root |
|---|---|---|
| Native new worktree | PASS, process exit 0 | `/var/tmp/vecnet-launcher-accept-fQJU0l` |
| Native existing project root | PASS, process exit 0 | `/var/tmp/vecnet-launcher-accept-ip7ijW` |
| Native temporary workspace | PASS, process exit 0 | `/var/tmp/vecnet-launcher-accept-u4U2dT` |
| ACP temporary workspace | Provider-seed preflight unavailable, exit 1; no initialize/model dispatch | `/var/tmp/vecnet-launcher-accept-m32ho7` |

All three native receipts record `pass: true`, accepted/running status, real initial and follow-up model replies, exact-request retry, owned launcher restart with native-session continuity, cancellation state `cancelled`, and complete unit teardown. Each controller verified its matching receipt before stopping its exact direct listener; all three controller journals record complete and native phase stopped. The reported effective route was model `ocx/gpt-6.1-sol`, provider `opencodex`, permission mode `read-only`. The source project's tracked and untracked dirty sentinels, branch and HEAD were unchanged in every workspace mode. Temporary workspaces were under private persistent launcher data in `/var/tmp`, outside `/tmp`.

Private receipt files and SHA-256:

| Mode | Receipt file | SHA-256 |
|---|---|---|
| Worktree | `/var/tmp/vecnet-launcher-accept-fQJU0l/tmp/vecnet-native-launcher-iHp9Zc/receipt.json` | `fff35ca4479bf9015300724bb2b08738e4540ba28526d0ea0c701b6bd49cf6d3` |
| Project root | `/var/tmp/vecnet-launcher-accept-ip7ijW/tmp/vecnet-native-launcher-VRfneX/receipt.json` | `13499f3973e00ad2b110fb425beeaaf88bcd286b42c1a37da83480e8330283a9` |
| Temp | `/var/tmp/vecnet-launcher-accept-u4U2dT/tmp/vecnet-native-launcher-OTnISH/receipt.json` | `224fe78c6a442c2cdc1497bf3511e8a752b1e2364e8f2b22ddd81ed3793df0e0` |

The ACP executable/package is available, but the owned mode-0600 settings file has neither `ANTHROPIC_API_KEY` nor `ANTHROPIC_AUTH_TOKEN` in its environment settings. The controller cannot derive the required self-contained private provider seed under the accepted contract. It dispatched no ACP initialize or model process and did not search/copy broader OAuth/account state or fall back to real HOME. ACP model execution, temp-workspace behavior and model teardown therefore remain unverified by this live matrix. Existing synthetic/protocol checks do not substitute for that missing model acceptance.

## Host selection and preliminary failures

The local Playwright module defaults to Chromium revision 1228, which is absent; revision 1248 is installed. Successful native runs used the existing supported override:

```sh
PLAYWRIGHT_EXECUTABLE_PATH=/home/xertrov/.cache/ms-playwright/chromium-1248/chrome-linux64/chrome
```

No browser download, source change or binary rebuild was needed for this host selection. Run commands and isolation setup are in [the disposable acceptance plan](../2026-11-15-launcher-disposable-acceptance.md).

Three earlier roots record concrete pre-model failures rather than acceptance:

- `1WPlWl`: readiness incorrectly required a direct socket where Codex published an owned symlink. Fixed by reviewed resolved-socket owner/private-parent validation (`de1ded54`).
- `8F23Jt`: Codex initialized its private rendezvous directory before nonrecursive mkdir. Fixed by reviewed validated rendezvous preparation before listener spawn (`e90ce986`).
- `4ViW1G`: approval, native initialize, repository and demo startup succeeded; browser startup could not use the missing default Chromium revision. No enrollment/install/job was dispatched. Its private demo recovery authority is retained; no exited demo PID/PGID was reused or signalled during closeout.

The readiness and rendezvous regressions were observed RED, then the full synthetic runner/adapter suite passed 21/21. Prior scoped runner review fixes confined inherited ACP logging/configuration and proved bounded controller exit after injected identity/journal failure while retaining the child and starting intent. Source reviews passed before actual retries. None of the preliminary failures was counted as a model or workspace PASS.

## Directed listener closeout

After all three native scenarios completed, MAIN explicitly directed closeout of only the three earlier pre-model listeners. For each listener, the controller journal and live PID/start-time/executable/exact argv/UID/private HOME were revalidated before sending SIGTERM to that leader only. Documented failure control flow and scenario journals showed no launch dispatch; missing projections alone were not used as authority. The original roots and journals were preserved.

| Root suffix | Verified PID / start stamp | Closeout |
|---|---|---|
| `1WPlWl` | `2355501` / `55119763` | Leader stopped; no live process-group descendants |
| `8F23Jt` | `3531786` / `55138400` | Leader stopped; no live process-group descendants |
| `4ViW1G` | `764817` / `55160966` | Leader stopped; no live process-group descendants |

Each root contains a separate fsynced mode-0600 `owner-closeout.json`. No broad group signal, SIGKILL, service operation, root deletion or user-state change was used.

## Proof boundary

These results execute the actual generated launcher unit command through the reviewed private process adapter, use real native model turns, and verify workspace/retry/restart/follow-up/cancellation/teardown behavior. They do not establish real user-systemd activation, systemd resource/restart enforcement, persistent deployment, or ACP provider availability. Private credentials, provider values, recovery bearer authority and full command logs remain in owner-private run roots and are not included in this public receipt.
