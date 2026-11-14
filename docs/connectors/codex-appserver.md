# Codex app-server delivery

The delivery connector delivers to the exact root thread in a private
[Codex ownership binding](codex.md). Run the invitation's `vecnet enrol` command
inside that root: supported hosts automatically configure hooks, bind the owner,
activate the managed runtime and verify readiness. Check `vecnet --net P codex status`.
A fresh status for the matching generation must say ready before treating a
session as automatically reachable. Credentials saved with setup pending is a
partial result; fix the reported requirement and run `vecnet repair` to resume
without redeeming the invitation again.

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

`hook --release` reports both `released` and `remaining`. An accepted claim with
`remaining > 0` retains its recovery rows; `released: 0` alone does not prove
cleanup. `remaining: 0` confirms cleanup even after a crash between the release
and local row deletion, so retrying that cleanup is idempotent.

## Capability matrix

| Surface | Automatic idle wake / busy steer | Current migration state |
|---|---|---|
| Daemon-backed TUI, recorded verified version, loaded root thread | Supported through the private control socket | Requires live matching connector status; evidence remains scoped to exercised canaries |
| Daemon-backed TUI, newer version at or above the compatibility minimum, loaded root thread | Tentatively compatible through the existing protocol | Requires the same runtime checks and live matching connector status; fresh canaries establish evidence |
| Daemon-backed TUI, version below the minimum or malformed | Refused (`refused_version`) | Upgrade or correct version reporting |
| Embedded TUI / `--no-daemon` | No shared daemon endpoint | Unavailable; an owned bridge is still required |
| `codex exec` | No shared daemon endpoint | Unavailable; manual explicit inbox only |
| Desktop app | No verified shared daemon endpoint | Unavailable; host bridge remains required |
| Paseo/T3 or other stdio app-server host | Separate endpoint | Unavailable through this connector; host integration required |
| Fork or child thread | Does not inherit root ownership | Separate explicit supported root enrollment required |

No lifecycle hook supplies a safe-point content fallback: documented parent-ID
sharing makes that unsafe. These unavailable surfaces are remaining rollout work
under issue #68, not fully migrated sessions.

### Version compatibility and evidence

The shared policy in `connectors/launcher/src/backends/codex-app-server/compatibility.ts`, reexported
through discovery, admits valid semver versions at or above
`CODEX_MIN_APPSERVER_VERSION` (`0.166.0`). This includes future major versions and
prereleases whose semver precedence is above the floor. Older or malformed
versions return `refused_version`.

New versions are admitted provisionally through the existing protocol. The
`CODEX_VERIFIED_APPSERVER_VERSIONS` list (`0.166.0`, `0.167.0`, `0.167.2`) records
verified stable versions; it is not an exact-version admission gate. Version
admission does not bypass protocol, exact-root ownership, policy, receipt or
private-socket checks, and does not establish readiness by itself.

Verification evidence is bounded to each canary's runtime version, surface and
behaviors exercised. Re-run protocol and model-context canaries for a new
runtime, retaining exact-thread receipts and negative isolation evidence. Passing
one canary does not verify every launcher or delivery path. In particular,
`0.169.x` remains tentatively compatible; this change does not establish full
verification for it.

On 2026-11-15, read-only probes of Codex `0.169.0` passed initialization,
exact-root reads and summary-history reads through both adapters. A disposable
root also passed resume-policy validation, untrusted model-context delivery,
turn completion and exact client-message receipt recovery. These checks do not
cover every launcher mode or the full enrollment path. Full history remains
bounded by the existing 4 MiB transport limit: one existing root's latest full
turn exceeded 27 MB and could not be read through that limit.

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

### Opt-out and display proof

`vecnet --net P codex unbind` records a durable opt-out for the canonical config,
profile and `--as` identity, including when there is no current binding. Saved
enrollment repair checks it before runtime activation and again under the binding
lock before enrollment can commit. Explicit addressed `codex bind` or `codex setup`
reenables only that target. Repair and ordinary use never clear opt-out, acquire
an ordinary existing profile, transfer ownership or renew expiry implicitly.
An unresolved send still blocks unbind until exact-owner recovery finishes.

Shared state distinguishes reservations (`shown` and `claims`) from successful
display (`displayed`). Complete hook/monitor output or an exact owner-bound
receipt can publish display proof. RPC acceptance alone cannot. Receipt settlement
validates the original client key and complete delivery batch, retires only its
matching claims/replay, and commits proof before deleting recovery evidence.

`watch --for-monitor` serializes complete output and proof with other shared-state
writers. A pending claim, busy/corrupt state or unverified identity closes the
transport at that event and retains one bounded envelope. Local retries use
1/2/4/8/15-second delays without releasing claims or passing the blocked cursor.
Receipt proof completes silently; a safe unsent release permits one print.
Stored evidence without both its hub and authenticated caller stays unchanged
and deferred; only empty legacy state can bootstrap a monitor identity. Generic
hooks retain their poll predecessor while any legacy reservation lacks positive
proof, including reservations outside the next page. Fresh verified hook output
records its proof before it can authorize cumulative acknowledgment.
Explicit new `--since` replay remains deliberate. Failed/short output, or failure
to persist proof after output, stops the watch before acknowledgment or retry.
All watch modes acknowledge only completely handled events; plain/JSON watch
does not publish monitor proof. Output and filesystem writes cannot form one
crash-atomic transaction, so a crash between them can still cause replay.

The private opt-out sidecar shares the ownership lock and leaves the strict
cross-language ownership schema unchanged. Codex lifecycle hooks remain
signal-only; none of these guarantees reintroduces lifecycle peer content.

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

For delivery without a launcher, enrollment automatically stages and activates
`vecnet-codex-delivery.service` using embedded connector assets and a private copy
of the matching CLI. It requires Linux, working user systemd and Node ≥22.18 in
the same OS user/HOME as the session. `VECNET_NODE` can select the Node executable.
Node must include working built-in TypeScript and SQLite: capabilities are probed,
not inferred from the version number. Missing capabilities, custom/conflicting
services and unavailable host bridges leave setup pending with a specific reason.
Routine enrollment does not require a source checkout or a separate installer. Managed activation currently requires `XDG_CONFIG_HOME` unset or equal to `$HOME/.config`; another user systemd configuration directory is reported unsupported before installation.

First ordinary use after an npm update or direct binary replacement schedules
background maintenance. Ordinary commands check local version metadata; complete integrity checks run in the background, with another check due after ten minutes of managed use. `vecnet repair --json` explicitly reconciles all
registered managed artifacts, the managed runtime/private CLI and pending
enrollments. `vecnet repair --status --json` reads the last maintenance result without changing files. `vecnet projects list --json` shows installation inventory and
pending/deferred project work; it is not a live owner-health check. Inspect
`vecnet --net P codex status` for that. Updates preserve ownership, expiry,
recovery and custom content; they cannot implicitly move a recipient.

The source-tree `scripts/install-codex-delivery.py` remains an advanced manual
staging tool. Unlike automatic enrollment, it does not start services or change
hooks, bindings or trust:

```sh
python3 scripts/install-codex-delivery.py --binary /path/to/vecnet --node /path/to/node --install-cli
systemctl --user daemon-reload
systemctl --user enable --now vecnet-codex-delivery.service
vecnet --net P codex setup
vecnet --net P codex status
```

It stages content-addressed assets and backs up replaced CLI/unit files. Review
and stop older delivery services before manual activation. An older
`vecnet-delivery@` runner uses a separate registry: preserve it and its ledger and
review each exact owner. Verified obsolete sources, including empty legacy binding
files, can be retired automatically only when their old writers are proven stopped;
unresolved active ownership remains a conflict. Never delete state to bypass it.
Expired registrations must not be renewed merely because software was upgraded.
The standalone service and launcher use the same singleton lock and recovery
ledger, beside the authority at `~/.local/state/vecnet/codex/delivery.sqlite`.
The connector validates private ownership, modes and paths for this database and
its SQLite sidecars in either mode. Only the singleton lock holder imports rows
or starts engines; stopping closes the recovery ledger before releasing the lock.

On its first upgraded start, each launcher imports its existing `codex` namespace
into the shared ledger in one transaction. It preserves original-owner rows and
leaves the launcher's other namespaces unchanged. The launcher namespace remains
a compatibility mirror; the shared ledger is authoritative. A durable source ID
and shared import marker prevent retained offline rows from replaying after the
other mode settles them. Different values for the same recovery key refuse the
whole import and start no engines (`codex_recovery_refused`); retain both ledgers
for deliberate owner-specific repair.

Before switching from an older launcher to standalone, start the upgraded launcher
once with its original state directory so its rows are imported. An upgrade cannot
discover a launcher ledger in an arbitrary state directory. Preserve all old
ledgers. If an old launcher cannot be upgraded first, switching requires its
recovery to be fully drained: no claiming, inflight or releasing rows and no
unsettled CLI ambiguity fence. Do not run an older launcher against the retained
compatibility mirror after switching modes; drain shared recovery and review
original ownership before rolling back.

To roll back software, stop the new service, restore the saved CLI/unit and restart
the previous service only after reviewing its ownership state. Never restore an
old binding snapshot over newer ownership or delete a ledger to resolve a send.

The existing `scripts/codex-appserver-smoke.mjs` documents earlier live evidence;
it is not evidence for a new runtime version or changed ownership protocol until
adapted as needed and rerun.
Use disposable owned threads, private hub/config/home state, exact thread/turn
receipts and model-context canaries for new acceptance. Source tests alone cannot
complete the installation and rollout checklist in issue #68.
