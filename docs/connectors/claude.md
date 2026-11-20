# Claude passive status and the delivery ledger

Claude Code conversations stay on the legacy hook path by default. What exists
today is passive by construction: an exact local session root can be
registered, the shared delivery daemon reports a passive status for one
addressed recipient, and the daemon records durable disposition claims for any
native delivery it attempts. No command here acknowledges the hub, and the
legacy hook path is never removed.

## Linux prerequisites

Build-version gating is currently disabled. Registration never inspects or
hashes the Claude executable, and the runtime snapshot's admission reports
`build_check: "disabled"` (with `version_qualified: true`) so nothing claims a
build was verified. The kernel-attested build-digest check is implemented but
parked for a later release. A session registers only for a profile with a
pinned hub identity (its `hub_pubkey`, or the pin its first ordinary command
records); otherwise registration stays pending with the reason `claude
registration needs a pinned hub identity; run a vecnet command for this
profile once`.

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

The output is passive only:

```json
{"version":1,"owner":"registered","mode":"legacy","generation":"…","pending_count":0}
```

- `owner` is `registered` only when the shared delivery daemon holds its
  singleton lease, its heartbeat is fresh (within three 10-second reload
  intervals), its PID/start identity is exactly the live writer, and the
  addressed binding is present at the current generation. Anything else is
  `none` with a `reason`.
- `mode` stays `legacy`, or becomes `opted_out` after a durable disable.
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

`claude resolve` settles exactly one claim by delivery id. `--keep-inbox`
quarantines it inbox-only (native promotion stops, the delivery stays
readable, nothing is acknowledged). `--retry` re-arms one fresh native
attempt, only after the explicit duplicate-risk confirmation and a fresh
admission check; it refuses held or hold-priority claims (hold is inbox-only,
full stop) and claims still owing a native disposition (`reserved` or
`attempting`). Nothing in Go retires a claim tombstone.

## Native delivery canary (experimental, default off)

The daemon's native pointer-delivery adapter exists only behind an explicit
experimental opt-in: `VECNET_CLAUDE_DELIVERY_CANARY=1` in the delivery
daemon's environment. Without it nothing changes: no native claim is made and
every message flows through the legacy hook path exactly as before. With it,
the daemon stages bounded wake candidates per admitted scope, records the
durable claim lifecycle above, and invokes the single-shot `claude deliver`
helper; it never acknowledges the hub (read/accepted/processed cursors stay
untouched), `hold` and `wake_idle` never enter a native batch, and the
daemon's inbox read uses `vecnet inbox --no-display-proof` so it never
records legacy hook display state — a later UserPromptSubmit hook still shows
what the daemon merely observed.

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
