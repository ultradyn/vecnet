# Claude passive status

Claude Code conversations stay on the legacy hook path. What exists today is
passive: an exact local session root can be registered, and the shared
delivery daemon reports a passive status for one addressed recipient. No hub
request, no Claude-side output, and no native delivery claim is ever made.

## Linux prerequisites

Session registration is qualified for Linux with Claude Code 2.1.322 or newer.
Registration proves, through the kernel and Claude's own private session
registry, that the registering hook is a child of the exact live Claude
process (`/proc` start identity, boot id, OS-user ownership, the canonical
config root, and a private messaging socket of the right type). Other
platforms, missing evidence and unsupported builds stay on the legacy path
and report `unsupported`; nothing is guessed.

Enrollment is the single ordinary setup action, exactly as for Codex:

```sh
vecnet enrol --hub https://YOUR-HUB --token - --profile P
```

Run it inside the intended Claude conversation. The session's own SessionStart
hook registers the exact root passively; the hook itself emits nothing and
performs no network request.

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
- `pending_count` is always `0`. There is no `deliveries` or `resolve`
  subcommand; native delivery does not exist yet, and status never claims it.

The daemon publishes one private sidecar,
`$HOME/.local/state/vecnet/codex/bindings.json.claude-status.json`, next to
the existing Codex v1 status. It carries only opaque binding keys,
generations, passive states and the host's own PID/start/boot identity —
never a socket path, config content or credential. A crashed daemon leaves a
stale file behind; stale heartbeats, dead or reused PIDs, mismatched boot or
start identities, wrong generations, or a released lease never count as a
live owner. Checking the lease never unlinks it.

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
