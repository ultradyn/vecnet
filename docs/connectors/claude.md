# Claude passive status and the delivery ledger

Claude Code conversations stay on the legacy hook path by default. What exists
today is passive by construction: an exact local session root can be
registered, the shared delivery daemon reports a passive status for one
addressed recipient, and the daemon records durable disposition claims for any
native delivery it attempts. No command here acknowledges the hub, and the
legacy hook path is never removed.

## Linux prerequisites

Build checking is currently disabled: any Claude Code build is admitted.
Registration never inspects or hashes the Claude executable, and status and
the runtime snapshot report `build_check: "disabled"` (the snapshot keeps
`version_qualified: true`) so nothing claims a build was verified. The
build-digest check is implemented but parked for a later re-enable.

A session registers only for a profile with a pinned hub identity: its
`hub_pubkey`, or the key its first hub-contacting command records on first
use. That pinned key, not the URL, is the hub identity the binding is keyed
by, so one hub reached through two URL aliases is one recipient. Until a pin
exists, registration stays pending with the reason `claude registration
needs a pinned hub identity; run a hub-contacting vecnet command for this
profile once (for example vecnet --net PROFILE inbox); claude status and
claude disable do not pin`. The `claude` subcommands read local state only
and never contact the hub, so none of them records a pin.

Registration proves, through the kernel and Claude's own private session
registry, that the registering hook is a child of the exact live Claude
process (`/proc` start identity, boot id, OS-user ownership, the canonical
config root, and a private messaging socket of the right type). Other
platforms stay on the legacy path and report `unsupported`, and missing or
mismatched evidence leaves the root `blocked` or unregistered; nothing is
guessed. The Claude build itself is not checked while build gating is
parked. Registration requires the
session host to supply `CLAUDE_PID` and `CLAUDE_CODE_SESSION_ID` in the hook's
environment, and a missing or malformed value leaves registration on the
legacy path. `CLAUDE_CODE_VERSION` is ignored.

Enrollment is the single ordinary setup action, exactly as for Codex:

```sh
vecnet enrol --hub https://YOUR-HUB --token - --profile P
```

Run it inside the intended Claude conversation when you want the session root
registered. When `vecnet enrol` runs where no live Claude host identity is
present (no `CLAUDE_PID`/`CLAUDE_CODE_SESSION_ID`), it
prints `credentials saved; automatic setup unsupported: …` — the profile is
enrolled, but session registration stays on the legacy hook path until a
session hook registers the root. Inside a session, the silent SessionStart
hook performs the passive registration (no hub request, no output); the
first verified user prompt can also register or renew it before the ordinary
legacy delivery path, byte-identically.

## Status

```sh
vecnet --net P claude status
vecnet --net P claude status --json
```

```json
{"version":1,"owner":"registered","mode":"legacy","generation":"…","pending_count":0,"blocked_pending_limit":false,"canary_opt_in":false,"build_check":"disabled"}
```

- `owner` is `registered` only when the shared delivery daemon holds its
  singleton lease, its heartbeat is fresh (within three 10-second reload
  intervals), its PID/start identity is exactly the live writer, and the
  addressed binding is present at the current generation. Anything else is
  `none` with a `reason`.
- `mode` is `legacy` by default and `opted_out` after a durable disable. It
  is `canary` only when both halves hold: this recipient's durable canary
  record (below) and the live host's report that its delivery adapter
  admitted this exact binding at the current generation. Either half alone
  stays `legacy` and the `reason` names the missing one. Status never
  reports any other native mode.
- `canary_opt_in` reports the durable canary record alone, whether or not a
  host has admitted it.
- `build_check` is `disabled` while build checking is parked.
- `pending_count` is factual, not fixed: it counts the addressed scope's
  unresolved native delivery claims (`reserved`, `attempting` or `uncertain`)
  in the durable ledger, and `blocked_pending_limit` reports whether the
  scope's staging window is latched on the 128-claim pending cap. Both come
  from the ledger and are reported even when no host is live.

The daemon publishes one private sidecar,
`$HOME/.local/state/vecnet/codex/bindings.json.claude-status.json`, next to
the existing Codex v1 status. It carries only opaque binding keys,
generations, passive states and the host's own PID/start/boot identity —
never a socket path, config content or credential. A crashed daemon leaves a
stale file behind; stale heartbeats, dead or reused PIDs, mismatched boot or
start identities, wrong generations, or a released lease never count as a
live owner. Checking the lease never unlinks it.

## Deliveries and resolve

```sh
vecnet --net P claude deliveries            # bounded disposition list
vecnet --net P claude deliveries --json
vecnet --net P claude resolve <delivery-id> --expect-generation G --keep-inbox
vecnet --net P claude resolve <delivery-id> --expect-generation G --retry --confirm-duplicate-risk
```

`claude deliveries` reads only the durable claim ledger the delivery host
owns (`delivery.sqlite` beside the bindings authority): opaque ids, states,
dispositions and reasons for the addressed scope — never message bodies,
credentials or scope coordinates. At most 256 rows are shown; only the oldest
settled rows are hidden and the truncation is noted.

The ledger is a live SQLite database the delivery daemon writes to. Opening
`delivery.sqlite` read-write while the daemon runs — for example a plain
`sqlite3 delivery.sqlite`, whose default journal handling can checkpoint or
recreate the WAL — can orphan the daemon's WAL and lose or corrupt recent
claims. Inspect a running daemon's ledger with `sqlite3 -readonly
delivery.sqlite` (and read-only queries), or stop the daemon first.

`claude resolve` settles exactly one claim by delivery id. `--keep-inbox`
quarantines it inbox-only (native promotion stops, the delivery stays
readable, nothing is acknowledged). `--retry` re-arms one fresh native
attempt, only after the explicit duplicate-risk confirmation and a fresh
admission check (including the canary record and the stored config
witness); it refuses held or hold-priority claims (hold is inbox-only, full
stop) and claims still owing a native disposition (`reserved` or
`attempting`). Nothing in Go retires a claim tombstone; only the delivery
host does (below). A claim marked by the host's ack observation shows
`hub_ack_observed: true` in JSON and `hub_ack_observed=true` in text.

## Native delivery canary (experimental, default off)

```sh
vecnet --net P claude canary enable    # this profile's own peer inbox only
vecnet --net P claude canary disable
```

The per-profile canary record is the gate on native pointer delivery into a
live Claude session. Without it nothing changes: no native claim is made and
every message flows through the legacy hook path exactly as before. The
record is local and neither verb contacts the hub. It fails closed:

- at most one record per user, for one exact peer scope: a principal
  credential or an `--as` delegation cannot hold it, and a second recipient,
  or the same recipient through another profile or hub URL alias, is refused
  until the first is disabled;
- `claude enable` alone never turns native delivery on, and canary enable
  never clears a durable opt-out (global or addressed); an opt-out still
  withdraws delivery while the record exists.

`VECNET_CLAUDE_DELIVERY_CANARY=0` (exactly `0`) in the delivery daemon's
environment is the host kill switch: no delivery adapter is constructed at
all. It is never an enable. Empty or `1` changes nothing; any other value is
logged once as a warning (`claude_delivery_canary_env_unrecognized`) and does
not disable anything.

What an admitted scope does:

- **Admission.** The host admits a scope only from the runtime snapshot:
  registered and live, policy-enabled, canary-opted, the caller's own peer
  inbox, and still witnessed by the config at the binding's own stored path
  (the profile, hub, pinned identity and caller witness). More than one
  qualifying row admits none.
- **Pinned children.** Every child the host runs for that scope (the inbox
  read, the watch and the ack-window read) runs with `VECNET_CONFIG` set to
  the binding's own config, never the daemon's default. A binding whose
  config path is missing or not absolute is never admitted.
- **New arrivals only.** Every admission baselines at head. A scope's first
  admission, every re-admission after the canary or policy was withdrawn and
  returned (including a config-witness lapse), and the first admitted pass
  after every daemon start, restart or kill-switch recovery reads
  `inbox --since head`, stores that cursor and stages nothing; the same
  happens after a generation change or with an empty cursor. On that pass a
  `reserved` claim left from before the gap (a stop, crash or outage
  mid-batch) is released with `fenced_scope_not_admitted`, never sent; an
  `attempting` one becomes `uncertain` as always. Deliveries
  already waiting before admission stay on the legacy hook and inbox path:
  wakes that arrive while the scope is not admitted, or while the daemon is
  down, get no native pointer — they stay in the inbox on the legacy path. A
  page that arrives with events on that head read is retried, never
  baselined past. If the hub refuses the stored cursor itself (purged below
  retention, from another epoch after a log rebuild, or malformed), the
  scope rebaselines at the head the same way; any other read failure keeps
  the cursor and retries with backoff.
- **Staging.** Bounded wake candidates, in cursor order, become durable
  claims (`reserved`, then `attempting` before any byte can leave). `hold`
  and `wake_idle` never enter a native batch. At most 128 unresolved claims
  per scope; the cap blocks further native sends only.
- **No auto-ack.** The host never acknowledges the hub and never posts a
  receipt: read/accepted/processed cursors stay untouched. Its inbox reads
  use `vecnet inbox --no-display-proof`, so they record no legacy hook
  display state and a later UserPromptSubmit hook still shows what the host
  merely observed.

### The `claude deliver` helper

`claude deliver` is the host's private single-shot transport child, not a
user command. It takes only `--binding-key`, `--expect-generation` and the
three message ids; there is no explicit socket, session or owner mode. It
resolves the endpoint and expected owner from the live binding registry and
holds the `claude-bindings` lock from resolution through the end of the
write, so no registration can commit a new generation mid-attempt. After
the kernel peer check on the one connection, and before the first byte, it
rechecks admission (canary, opt-outs, generation, liveness), the stored
config witness, and Claude's own session row for that process. It writes one
pointer-only frame of at most 4 KiB within a one-second connect/write
window. Exit codes:

| Exit | Meaning | Recorded disposition |
| ---- | ------- | -------------------- |
| 0 | Full write and EOF flush. Never proof the session showed it. | `released` / `attempted` |
| 3 | Connect failed; zero bytes. | `refused` / `connect_refused_or_timeout` |
| 5 | Refused before any write: resolution, admission, generation, config witness, lock wait (one second), socket type, frame size, peer identity, the pre-write recheck, or the one-second window expiring before the write began; zero bytes. | `refused` / `refused_before_write` |
| 4 | Write failure, partial write, or a recovered internal fault: bytes may have left. | `uncertain` / `write_failed_or_outcome_unknown` |
| 2 | Flag parsing only. | as 4 |

The host also treats a signal, a timeout or any unmapped code as `uncertain`
(4), and a child that never spawned as a definite refusal (5). Older claims
may still carry the earlier reason string `write_failed_or_partial`; it
means the same as `write_failed_or_outcome_unknown`. An `uncertain` or
`attempting` claim is never resent automatically.

### Ack observation, tombstones and disable drain

- **Ack observation.** For an admitted scope with unretired claims, at most
  once a minute, the host reads up to 200 events from the hub's acked
  cursor (an ordinary `inbox` read with no `--since`, never `--ack`). When
  that window is complete (fewer than 200 events), every settled claim of
  that scope whose delivery is absent from it is marked `hub_ack_observed`.
  The flag is inferred from absence, so it also covers a delivery the hub
  expired or purged unacked; it is not a confirmation the hub sent. A
  truncated or failed read marks nothing. Live (`reserved`/`attempting`)
  claims are never marked.
- **Retirement.** In the claim's own scope only, a marked claim in a settled
  state (`released`, `refused`, `held` or `expired`) is deleted, and only if
  its stored bytes are unchanged since the host read them, so an operator
  resolution that lands meanwhile always wins. `uncertain` claims keep the
  mark and are never retired; an operator resolves them.
- **Disable drain.** Every pass with a readable snapshot fences the claims of
  every scope that is no longer admitted (disabled, opted out, no longer
  canary-opted, unwitnessed or gone), even when nothing is admitted: a
  `reserved` claim becomes `released` with the reason
  `fenced_scope_not_admitted`, and an `attempting` one becomes `uncertain`
  (`restart_may_have_written`). A pass whose snapshot cannot be read changes
  nothing, and it does not end an admission: if the scope is admitted again
  on the next readable pass, the host resumes its stored cursor rather than
  rebaselining. A withdrawal hidden entirely behind unreadable passes can
  therefore still be promoted when it ends; any pass that proves the scope
  withdrawn drains it and forces a rebaseline.

## Disable and enable

```sh
vecnet claude disable            # global: persists with the versioned Claude settings
vecnet --net P claude disable    # addressed: one recipient's durable opt-out
vecnet --net P claude enable     # clears only the addressed opt-out
vecnet claude enable             # clears the global disable
```

Global disable wins over profile enable: while it is set, an addressed enable
cannot restore registration. Neither action removes the legacy hook path,
deletes a stored binding row, or starts any native promotion. An opt-out
blocks registration and keeps the stored row, so an explicit enable resumes
idempotently with the original generation.

## Repair, hooks and Monitor behavior

`vecnet repair` retries saved Claude enrollment intent for the original root
without redeeming another invitation. The hook and Monitor behavior is
unchanged: lifecycle hooks stay signal-only, delivery still flows through the
legacy hook path, and `vecnet --net P inbox` remains the explicit manual
operation. `vecnet --net P claude status` is read-only and performs no hub
request, so it is always safe to run for diagnosis.
