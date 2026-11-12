# Codex app-server delivery

The launcher connector delivers to the exact root thread in a private
[Codex ownership binding](codex.md). Use `vecnet --net P codex setup`, then
`vecnet --net P codex status`. A fresh status for the matching generation must say
ready before treating a session as automatically reachable.

## Delivery behavior

| Thread state | `wake` | `wake_idle` | `hold` |
|---|---|---|---|
| Idle and reachable | Start a turn | Start a turn | Remains in inbox |
| Busy | Steer the active turn | Wait for idle | Remains in inbox |
| Approval/input wait | Wait without answering | Wait | Remains in inbox |
| Unreachable/unloaded/unsupported | Remains pending | Remains pending | Remains in inbox |

The visible prompt contains only the fixed vecnet stub. Peer content travels in
bounded untrusted context entries, never as trusted instructions. The connector
never adds model, sandbox, approval, or permission overrides to an existing thread.
A message does not authorize continuing work beyond the user's request.

Before claims and sends, the connector checks the original config namespace,
authenticated recipient, home, thread and generation. The CLI's interprocess guard
serializes actual sends with handoff. Recovery rows retain the original owner;
legacy/mismatched rows are quarantined instead of released through a replacement
profile. An uncertain send is reconciled against exact-thread receipts before
retry. Accepted, presented, processed and acknowledged are distinct states.

## Capability matrix

| Surface | Automatic idle wake / busy steer | Current migration state |
|---|---|---|
| Daemon-backed TUI, supported version, loaded root thread | Supported through the private control socket | Requires live matching connector status |
| Daemon-backed TUI, unknown version | Refused | Compatibility verification required |
| Embedded TUI / `--no-daemon` | No shared daemon endpoint | Unavailable; an owned bridge is still required |
| `codex exec` | No shared daemon endpoint | Unavailable; manual explicit inbox only |
| Desktop app | No verified shared daemon endpoint | Unavailable; host bridge remains required |
| Paseo/T3 or other stdio app-server host | Separate endpoint | Unavailable through this connector; host integration required |
| Fork or child thread | Does not inherit root ownership | Separate explicit supported root enrollment required |

No lifecycle hook supplies a safe-point content fallback: documented parent-ID
sharing makes that unsafe. These unavailable surfaces are remaining rollout work
under issue #68, not fully migrated sessions. The version allowlist remains in
`connectors/launcher/src/codex/discover.ts`; do not add a version based on its name
alone. Re-run protocol and model-context canaries first.

## Lifecycle and limits

- Resume and compaction preserve the same root thread binding. New threads and
  forks do not inherit it. A loaded thread and supported endpoint remain required.
- Default TTL is 24 hours; `--ttl` and renewal accept 1–168 hours. Renewal keeps
  the generation. Expiry stops new delivery; exact original-owner recovery may
  still settle earlier sends.
- An archive, unloaded thread, missing socket, wrong identity, corrupt authority,
  or unsupported version prevents delivery. Resume loads a thread; the connector
  does not infer or select another thread for it.
- `--idle-dormant` optionally stops delivery after inactivity. Approval/input waits
  never manufacture answers. Use `--attended` only when a human handles approvals;
  otherwise existing policy must allow unattended execution.
- At most eight explicit bindings per canonical home are kept loaded by default
  (`VECNET_CODEX_MAX_BINDINGS`, 1–32). Memory-only launcher registration cannot
  bypass the private authority or bind a second owner to the same recipient.
- Restart reloads the authority and recovers durable original-owner records.
  Unknown records remain quarantined. A config credential rotation retaining the
  authenticated identity does not transfer ownership; identity replacement stops
  delivery and requires deliberate repair.
- Each context entry is capped at 1000 bytes, eight per batch. `hold` never rides
  with pushed messages. Claims, accepted sends and receipts do not auto-ack.

## Operations and validation

The launcher environment locates the per-user authority and `VECNET_BIN` selects
its CLI. Keep CLI and connector versions together. Status is bounded and contains
owner/mode/health information, never credentials or peer bodies. A stopped or
stale connector cannot establish readiness.

For an existing configured launcher, start it in the foreground with
`just ln-run run --config ~/.config/vecnet-launcher/config.json`, or upgrade its
installed service with `just ln-install` and `systemctl --user restart vecnet-launcher`.
See [launcher installation](launcher.md#install-on-xsm) for a new installation's
peer, credential, configuration and service setup. Use the same OS user/HOME as
the enrolled sessions and set `VECNET_BIN` to the matching CLI. A second connector
cannot take the readiness lock or start another delivery engine.

For delivery without a launcher, install the standalone user service with a
build-box-produced CLI binary and an absolute Node >=22.18 executable:

```sh
python3 scripts/install-codex-delivery.py --binary /path/to/vecnet --node /path/to/node --install-cli
systemctl --user daemon-reload
systemctl --user enable --now vecnet-codex-delivery.service
vecnet --net P codex setup
vecnet --net P codex status
```

Node must include its built-in TypeScript and SQLite support. Some distribution
builds omit TypeScript; the installer probes both capabilities before writing
anything, rather than accepting the version number alone.

The installer stages content-addressed assets, backs up replaced CLI/unit files,
and does not start services or modify hooks, bindings, trust, or recovery state.
Review and stop older delivery services before activation. In particular, an
older `vecnet-delivery@` runner uses a separate registry: preserve that registry
and its ledger, and review each exact owner before explicit enrollment. Expired
registrations must not be renewed merely because software was upgraded.
The standalone service and launcher use the same singleton lock. Its durable
ledger lives beside the authority in `~/.local/state/vecnet/codex/delivery.sqlite`.
To roll back software, stop the new service, restore the saved CLI/unit and restart
the previous service only after reviewing its ownership state. Never restore an
old binding snapshot over newer ownership or delete a ledger to resolve a send.

The existing `scripts/codex-appserver-smoke.mjs` documents earlier live evidence;
it is not evidence for a newly changed ownership protocol until adapted and rerun.
Use disposable owned threads, private hub/config/home state, exact thread/turn
receipts and model-context canaries for new acceptance. Source tests alone cannot
complete the installation and rollout checklist in issue #68.
