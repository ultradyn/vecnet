# Running a launcher

A launcher is a small daemon that starts coding-agent sessions for vecnet peers. A requester (another agent, or the operator) sends it a `launch` job naming a backend, a project alias, a preset and a permission mode. The launcher opens one session in that backend (native Codex app-server, T3 Code through `t3-bridge`, or Paseo through `paseo-bridge`), relays the job thread's messages into the agent as turns, and posts each finished turn back as a reply. The agent's questions and permission prompts go to the requester, who can answer or escalate to the operator. The launcher is a peer of kind `launcher` (PROTOCOL §18.1); the wire contract that requesters and launchers follow is [PROTOCOL §13.8](../PROTOCOL.md#138-launcher-issue-26). The code is in [`connectors/launcher/`](../../connectors/launcher/README.md); the design is [`docs/design/2026-11-03-launcher.md`](../design/2026-11-03-launcher.md) and the build plan [`docs/plans/2026-11-03-launcher.md`](../plans/2026-11-03-launcher.md).

**Accepted risk.** Sessions run as Max (D2: there is no dedicated user or sandbox for T3 sessions yet, on xsm or on the build boxes), and any requester may ask for `auto` or `full-access` (D1, the shipped `policies.default`). A `full-access` agent can therefore do anything Max's account can: read his credentials, push, delete files outside its worktree. The controls that remain are spawn-time policy (which peers may launch, which projects, which modes; see [Configuration](#configuration)), the caps, approvals for `ask` sessions, and the operator's ability to disable the launcher or a requester at once. Narrow `policies.default.permission_modes` before letting a peer you do not trust launch.

## How it works

- One `systemd --user` unit per launcher profile runs `connectors/launcher/src/main.ts run`. It reads its bearer from a 0600 file, opens the peer's event stream, and records every Delivery, job, session, turn and outgoing post in a SQLite ledger under `state_dir`, so a restart neither loses a reply nor launches a second session.
- Each `launch` job gets its own thread and exactly one session. Sessions are tagged `vl-<job_id>` and work on branch `launch/<job_id>`; the launcher's sweep only ever touches sessions whose tag starts `vl-`.
- Backends are plugins in `src/backends/<id>/`. They see neither the hub, the credential nor raw Deliveries; every agent environment is scrubbed of `VECNET_*` and other secrets, and every agent-originated text is redacted before it is posted.
- Status goes to the requester as `job_status` messages (`accepted` → `running` → `succeeded`/`failed`/`cancelled`); the job stays `running` for the session's whole life.

## Set up a native Codex launcher

Run these commands on the host that will run the jobs. Enrol a **dedicated** peer of kind `launcher`; a launcher is a service, so its invitation is a host setup workflow, not a prompt to paste into a conversational agent. Existing interactive profiles and delivery services are separate.

```sh
# Operator creates the launcher; redeem the returned token on its host:
vecnet peer add --kind launcher --name launcher-host
vecnet enrol --hub https://vecnet.xk.io --token - --profile launcher-host

# Preview, then write a private launcher config and credential:
vecnet --net launcher-host launcher setup --project my-repo --repo /absolute/repo --base-ref master
vecnet --net launcher-host launcher setup --project my-repo --repo /absolute/repo --base-ref master --write

# Preview the source snapshot, resolved runtime paths and complete service unit:
vecnet --net launcher-host launcher install --source /absolute/vecnet-checkout
vecnet --net launcher-host launcher install --source /absolute/vecnet-checkout --write
vecnet --net launcher-host launcher check

# Enable on user login and start (or restart) only when requested:
vecnet --net launcher-host launcher install --source /absolute/vecnet-checkout --write --enable --start
vecnet --net launcher-host launcher status
```

`setup` verifies the credential's authenticated peer and its directory kind before writing. It stores `config.json` and a separate `credential` with mode 0600 in a private directory beside the vecnet config: `<config dir>/launcher/p-<id>/`. The id also incorporates the config path, keeping profiles from separate config files isolated. Setup leaves the enrolled profile and Codex configuration unchanged. Existing setup files require `--force --write` to replace; edit the generated config to add more project aliases or narrower policies.

The generated backend is `codex-app-server`, module `./backends/codex-app-server/index.ts`, with preset `default`. Request launch jobs with that backend, preset and your project alias. `--codex-home /absolute/.codex` chooses an existing Codex home (default `$CODEX_HOME`, then `~/.codex`). Its running, authenticated Codex daemon supplies the existing route and model; setup never copies API keys or rewrites routes. Optional `--model MODEL` and `--effort EFFORT` override those values for the default preset only. The backend config has this shape:

```json
{
  "codex_home": "/absolute/.codex",
  "presets": { "default": {} }
}
```

Each preset may contain `model` and `effort`. Backend config also accepts optional `workspace_root` (absolute path) and `poll_ms` (100–60000). All four permission modes are supported: `read-only`, `ask`, `auto`, `full-access`. Generated policy permits only the configured project and native backend, with these four modes; narrow it as needed. Existing Codex threads are never claimed as launcher jobs.

The native adapter uses the existing host account and background Codex daemon. It verifies the rendezvous symlink, owner-private socket directory, socket owner and allowlisted daemon version (0.166.0 or 0.167.0). It does not provision the future dedicated-user/egress-isolated runtime described in the design. Each job gets a new named thread and an isolated Git worktree based on the allowlisted repository/ref. `running` reports the actual thread id, workspace, branch, model/provider and link; the job continues to speak through its authenticated launcher identity, rather than a synthetic per-session peer.

New native threads receive explicit spawn policy: `read-only` uses the read-only sandbox with approvals disabled; `ask` uses workspace-write with user approvals; `auto` uses workspace-write with the Codex auto reviewer; `full-access` uses danger-full-access with approvals disabled. The web form defaults to `ask` and a 24-hour deadline. These settings apply only to newly created job threads. Reattachment never changes a running thread's policy. To start a missing host daemon, use `CODEX_HOME=/absolute/.codex codex app-server daemon start` on that host before checking the launcher; the adapter reports an unavailable or unsupported daemon as unhealthy.

The opt-in `scripts/native-launcher-live-smoke.mjs` exercises web creation, host setup/install, a real native Codex worktree launch, exact-request retry, a model follow-up reply, and run-owned cancellation/service teardown against a local demo hub. Set `VECNET_NATIVE_LAUNCHER_SMOKE=1`, a built `VECNET_BIN` and `VECNET_UI_DEMO_BIN`; it uses the existing authenticated `CODEX_HOME` but only creates fresh run-owned threads. Unit/fake-backend results do not substitute for this live check.

`install` requires Linux with `systemd --user` and Node 22.18+ with default TypeScript support (23.6+ on Node 23). It resolves Node from the caller's `PATH` or `--node /absolute/node`, and resolves vecnet from `--vecnet-bin /absolute/vecnet`, `$VECNET_BIN`, or the running executable. The unit records absolute executables and the caller's `PATH`, `HOME` and vecnet config path; there is no `/usr/bin/node` assumption. `--source` may be omitted when running inside a vecnet checkout.

Runtime sources, package manifests and schemas are copied to `$XDG_DATA_HOME/vecnet-launcher/snapshots/<content hash>/` (default `~/.local/share/vecnet-launcher/`). Jobs use `$XDG_STATE_HOME/vecnet-launcher/p-<id>/`. The unit is `$XDG_CONFIG_HOME/systemd/user/vecnet-launcher-p-<id>.service` (default `~/.config/systemd/user/`). Installation records public runtime paths in `install.json` beside `config.json`. Changes to the checkout do not change an installed snapshot; installing changed sources selects a new snapshot. Old snapshots remain available, and a snapshot whose files were edited is refused on reuse.

Setup and installation print JSON plans unless `--write` is present, without exposing credentials. `install --write` writes assets and reloads the user manager. `--enable` enables the unit, and `--start` starts or restarts it; both require `--write`. Without those flags installation leaves the service's enabled/running state as it was. `status` reports configuration, installed paths and systemd state; `check` runs backend health and capability checks from the installed snapshot. Use the unit name shown by `status` with `journalctl --user -u UNIT` and `systemctl --user stop UNIT`. User services run after logout only if the account already has lingering enabled.

The launcher unit handles launch jobs only. Automatic delivery to existing Codex threads uses the separate hub-scoped [Codex delivery owner](codex-appserver.md), installed through its existing workflow; `launcher install` neither enables nor replaces it.

## Legacy T3 installation on xsm

These steps are for Max or the coordinator. A build task never runs them.

**1. Enrol the peer and store its credential.**

```sh
vecnet peer add --kind launcher --name launcher-xsm
vecnet enrol --hub https://vecnet.xk.io --token - --profile launcher-xsm
install -d -m 700 ~/.config/vecnet-launcher
install -m 600 /dev/null ~/.config/vecnet-launcher/credential
```

The first command prints the `peer_id` and a one-time enrolment token; paste the token into the second and finish with Ctrl-D. Copy the `secret` value from `[profiles.launcher-xsm]` in `~/.config/vecnet/config.toml` into `~/.config/vecnet-launcher/credential` (one line, nothing else). The daemon refuses to start if the file's mode is anything but `0600`. Substitute your hub's origin for `https://vecnet.xk.io`.

**2. Raise its send limits.** Each job that escalates to the operator opens an escalation thread, and a busy launcher posts many turn replies and `job_status` messages:

```sh
vecnet peer limits peer_<launcher-xsm id> --sends-per-min 120 --threads-per-hour 60
```

Use the `peer_id` that `peer add` printed.

**3. Write the config.** Copy [`connectors/launcher/config.example.json`](../../connectors/launcher/config.example.json) to `~/.config/vecnet-launcher/config.json` and edit it:

- `hub.url`: the hub's origin; `hub.credential_file`: the file from step 1 (absolute path).
- `projects`: every repository a session may use, as `alias → {repo_root, base_ref}`. Requesters name the alias, never a path. **The built-in default policy allows no project**: list the aliases you allow in `policies.default.projects` (the example lists `vecnet4`), or `"*"` for all.
- `backends[].config`: the example's `{}` is a placeholder and fails `check`. For T3:

```json
{ "id": "t3", "module": "./backends/t3/index.ts", "config": {
    "bridge_bin": "/home/xertrov/.local/bin/t3-bridge",
    "caller": "vecnet-launcher",
    "presets": {
      "sol": { "instance": "codex", "model": "ocx/gpt-6.1-sol", "options": [{ "id": "effort", "value": "high" }] }
    } } }
```

  A preset names a T3 provider instance, a model and its option values exactly as `t3-bridge models --json` lists them. `runtime_modes` (optional) narrows the modes it offers; `route` (optional, `{base_url, key_file}`) makes `health()` probe a model route. `projects` (optional, `alias → {project_id}`) pins T3 project ids; otherwise the plugin resolves the project by `repo_root`.

**4. Check, install and start.**

```sh
just ln-setup                     # once per checkout
just ln-run check --config ~/.config/vecnet-launcher/config.json
just ln-install
systemctl --user daemon-reload
systemctl --user enable --now vecnet-launcher
```

`check` prints each backend's health, version, live presets and permission modes as JSON, and exits 1 if any backend is not healthy (2 for a config error). `just ln-install` copies the current checkout to `~/.local/share/vecnet-launcher/<sha>`, points `current` at it and installs the unit; it never enables the unit. Logs go to the journal (`journalctl --user -u vecnet-launcher`); they never contain message bodies or secrets. To upgrade, pull, `just ln-install` again and `systemctl --user restart vecnet-launcher`: open sessions are reattached, not relaunched.

The launcher then shows in the directory with kind `launcher`, presence `idle`, and capability tags `launch:t3`, `preset:t3/sol`, `host:xsm` and `mode:<mode>`.

## T3 prerequisites

**Agents never pair, re-pair or write any t3-bridge token or config.** Only Max changes them.

- The existing auto token (`~/.config/t3-bridge/token`) is enough for `ask` and `auto` launches. Nothing changes. (The T3 plugin does not offer `read-only`.)
- **`full-access` is a manual step for Max:** add `vecnet-launcher` to `full_access.callers` in `~/.config/t3-bridge/config.json`. `token-full` already exists on xsm and is shared with `forge`, `xpp` and `xpp-eng-lead`; re-pairing full-access would replace it and break them, so pair full-access only on a box where `token-full` is missing. Caveat: once allowlisted, **every** launcher call goes through `token-full`, not only full-access ones.
- Until Max does this, T3 refuses full-access launches and the job fails `backend_error`. The launcher looks for anything the failed call created (by its `vl-<job_id>` tag) and settles it at once (`settled_orphan: true`), so no session runs unsupervised.
- The plugin uses only `t3-bridge` subcommands that exist today. It does not append Grok Bot wake instructions and registers no watcher wakes, so `t3-bridge outbox list` and `~/.local/state/t3-bridge/launches.json` never gain launcher entries.

## Model routing (D11)

Presets reuse the route the backend is already configured with, including xsm's OpenCodex route; the launcher never rewrites a working route. `check` lists only the presets T3 reports as live (an enabled, fresh provider instance with that model and option values). The LLMP harness profile is only for boxes without a working route; a preset's optional `route` lets `check` and the presence report show a dead route as `degraded`.

## Configuration

Durations take `"30m"`, `"2h"`, `"7d"` or a number of milliseconds. An unknown or malformed key fails startup and `check` with a JSON pointer to the field (exit 2).

| Key | Default | Meaning |
|---|---|---|
| `host` | (required) | Host name in presence tags and `accepted` output |
| `state_dir` | (required) | Ledger directory; absolute |
| `limits.max_sessions` / `max_open_sessions` | 6 / 24 | Running (unparked) and open sessions on this launcher |
| `limits.max_idle_wrapup_ms` | 8h | Largest `input.idle.wrapup_after` a requester may ask for |
| `limits.max_session_lifetime_ms` | 72h | Hard bound on a session, whatever `expires_at` says |
| `limits.workspace_retention_ms` | 7d | How long a settled workspace is kept before the sweep may reclaim it (only if clean and its branch exists) |
| `policies.default` | backends `t3`, `paseo`; every mode; **no projects**; 3 running, 12 open, 30 launches/hour | The ceiling for every requester without its own entry |
| `policies.<peer_…\|prn_…>` | inherits `default` per field | A ceiling for one peer, or for every peer a principal created (keyed on the hub-set `created_by`) |
| `approvals.parent_expires_ms` | 15m | How long the requester has to answer a permission prompt |
| `approvals.on_parent_expiry` | `deny` | `deny`, or `escalate` to the operator |
| `approvals.operator_expires_ms` | 60m | An operator prompt's expiry; it always denies |
| `approvals.to_operator` | presets `["none"]` | Prompts that skip the requester (D10): presets `outside-workspace`, `privileged`; `categories` (`exec`, `edit`, `network`, `mcp_tool`, `other`, `unverifiable`, …); `requesters` (peer or principal ids) |
| `relays.<name>` | none | Route a principal's (or listed peers') prompts through its own approval relay, with its own expiry and escalation switches |
| `questions.blocking_timeout_ms` | 30m | When a blocking question is answered "use your best judgement" |
| `idle.park_after_ms` | 30m | Park an idle session, where the backend can resume it |
| `idle.settle_opaque_ms` / `settle_observable_ms` | 2h / 24h | Settle an idle session (opaque background tasks / observable and parked) |
| `idle.deadline_margin_ms` | 5m | Settle this long before the session deadline, busy or not |
| `presence.heartbeat_ms` | 5m | Presence report interval (it also reports on change) |
| `wake_interrupts` | `false` | Reserved; no effect yet |

Policy keys on the authenticated sender (and the directory's `created_by` for `prn_` keys), never on names, content or tags.

## Requester guide

The normative rules are [PROTOCOL §13.8](../PROTOCOL.md#138-launcher-issue-26); this is the short version.

**Find a launcher** in the directory by kind `launcher` and its tags (`launch:t3`, `preset:t3/sol`, `host:xsm`, `mode:auto`). Tags are self-reported and authorise nothing; presence `blocked` means it is at its cap or every backend is down.

**Launch.** `POST /v1/messages` with your own credential (PROTOCOL §2.2):

```json
{
  "idempotency_key": "launch-2026-11-05-001",
  "kind": "message",
  "recipients": ["peer_<launcher>"],
  "priority": "wake",
  "locked_participants": true,
  "expires_at": "2026-11-08T00:00:00Z",
  "content": { "text": "launch: tidy the README" },
  "job_request": {
    "version": 1, "job_key": "tidy-readme-1", "operation": "launch",
    "instructions": "Tidy README.md, commit on your branch, and stop.",
    "input": { "backend": "t3", "project": "vecnet4", "preset": "sol", "permission_mode": "auto",
               "title": "tidy README", "reply_priority": "wake_idle", "idle": { "wrapup_after": "45m" } }
  }
}
```

- No `thread_id`: every launch starts its own thread, and a second `job_request` in it is refused `unsupported` ("one job per thread"). Exactly one recipient, the launcher.
- **Set `expires_at`** (at most now + 7 days, §18.3). It bounds the session: the deadline is `min(expires_at, accepted + max_session_lifetime)`, and the session settles 5 minutes before it, busy or not (`succeeded {ended: "deadline"}`). Without it the hub's default of now + 24 h applies. A launch whose remaining lifetime is shorter than its settle timer plus 10 minutes is refused `policy_denied` / `expires_too_soon` with `details {expires_at, needed_ms}`.
- `input` fields: `backend`, `project`, `preset`, `permission_mode` (`read-only`, `ask`, `auto`, `full-access`) are required; `base_ref`, `title` (≤ 200 characters; the launcher prefixes `vl-<job_id>`), `idle.wrapup_after`, `idle.wrapup_turn`, `relay_from` (extra peer ids the agent hears), `reply_priority` and `deliver.push_branch` are optional. Unknown keys are refused. `backend: "any"` and `continue_job` are reserved and refused.
- **Modes per backend.** The launcher's `mode:` presence tags list the modes it offers. T3 offers `ask`, `auto` and `full-access`; Paseo offers `full-access` only. Neither offers `read-only` today, so such a launch is refused `policy_denied` / `permission_mode`.
- `idle.wrapup_turn: true` asks for one wrap-up turn before an idle settle: the launcher prompts the agent to commit and summarise, posts that turn's reply, and settles when it ends, with the turn's (redacted) text as `succeeded.summary`. New input during it keeps the session open; the deadline settle never waits for it.

**Threads and replies.** The launcher replies **to your launch message** with every post (turn replies, notes, questions, approval requests and `job_status`), so depth stays at 1 and each post lives `min(launch expires_at, now + 24 h)`. Do the same: reply to the launch message, or to a launcher post less than 24 hours old. A reply to an older reply can be refused 422 `expired`.

**Talking to the agent.** Only you, the operator and your `relay_from` peers reach the agent; anyone else's message gets a `processed` receipt with note `not_relayed`. Your text arrives as quoted data under `[vecnet] from <name> (<peer_id>), <msg_id>:`, never as instructions to the agent's harness. Attachments are not forwarded (the agent sees a one-line list of names and hashes). Priority decides timing:

| Priority | Session idle or parked | Turn running |
|---|---|---|
| `wake` | Starts a turn | Steers the running turn where the backend can; otherwise queues |
| `wake_idle` | Starts a turn | Queues; sent as one turn when the current one ends |
| `hold` | Buffered; prepended to the next turn's input | Buffered |

**Turn replies.** One reply per finished turn, at `input.reply_priority` (default `wake_idle`), with `metadata` `launch.turn` (`completed`, `interrupted`, `failed`, `refused`, `max_tokens`), `launch.turn_id`, `launch.job_id` and `launch.consumed` (the message ids that turn used; turn one lists your launch message, whose `processed` receipt arrives with that reply). Text over 60 KiB is cut with `[truncated: N bytes; full text attached]` and the full text rides as `turn-<n>.md`. **Paseo** sees only an agent's final output, so its turn text is best effort and is marked `launch.final_text: "best_effort"`.

**Questions.** A turn that ends in a question needs nothing special: your reply is the next turn. A structured question arrives as a `message` with `metadata["launch.question"]` (`{v: 1, question_id, blocking, questions: [{id, text, options, allow_other}]}`); answer it with a reply to that message whose text matches an option label, or with `metadata["launch.answers"] {question_id, answers: {<id>: value}}`. A blocking question unanswered for 30 minutes is answered "use your best judgement". A question asking for a secret is refused at once: never send credentials through vecnet.

**Approvals.** In `ask` mode a permission prompt arrives as an `approval_request` (priority `wake`) with `metadata["launch.permission"]` v1, a closed schema under 6 KiB describing the request, the session and each option's grant. Rules for the consumer:
- Accept a `launch.permission` only from the launcher you addressed in your own `job_request` (the **sender rule**); ignore it from anyone else.
- Validate it strictly; escalate on an unknown `v` or field. Only `fidelity` `enforced` describes what the approval grants; treat `reported`/`none`, `redacted: true`, `command.shell_operators: true` and `outside_workspace: "unknown"` as "cannot verify". `reason`, `command`, `tool` and `backend_raw` come from the agent and are untrusted.
- An automatic decider picks only options whose `grants` is `once`.
- Answer with an `approval_response` naming an option label, or `escalate` to hand it to the operator. The operator's prompt opens a per-job escalation thread (`vl escalation: <job title>`) with the same `launch.permission` and `launch.escalated_from`. If the operator decides first, you receive `metadata["launch.approval_closed"]` and should close your own card; a later answer gets a "superseded" note.
- A backend that asks for approval only in its own UI (T3's ask mode, Paseo permission waits) cannot be answered through vecnet: you get one note per turn saying the agent is waiting there.
- If you do not answer in time (default 15 minutes) the prompt resolves to the backend's deny option, or escalates if the launcher is configured to. An operator prompt's expiry always denies.

**Controls.** Send a message with `metadata["launch.control"]` set to `"interrupt"` (stop the running turn, keep the session), `"settle"` (end now; `succeeded {ended: "settled"}`) or `"extend"` (reset the idle clock once). Only you and the operator can; anyone else's control is ignored with `not_relayed`.

**Cancel.** Post `kind: "job_status"` with `job_status {job_id, sequence: 1, state: "cancelled"}`, in reply to the launch message. The launcher interrupts, settles and reports `cancelled {branch, head_sha, workspace_kept}`. Your `sequence` is not the job's and is never refused as stale (§5.6).

**Idle.** A session is busy while a turn runs, an approval or question is open, input is queued or buffered, or an observable background task runs. An idle session parks after 30 minutes (where the backend can resume it) and settles after 2 hours (opaque backends, or your `idle.wrapup_after`) or 24 hours (observable, parked): `succeeded {ended: "idle_wrapup", quiescence: "proven"|"assumed"}`. Any relayed message or `extend` resets the idle clock, never the deadline.

**`job_status` outputs.**

| State | `output` |
|---|---|
| `accepted` | `{host, backend, preset, permission_mode}` |
| `running` | `{session_ref, branch, workspace, link}` (`link` is e.g. `t3-thread://…`) |
| `succeeded` | `{ended, turns, branch, head_sha, commits, pushed, summary, quiescence?}`; `ended` is `idle_wrapup`, `settled` or `deadline` |
| `failed` | `{error, reason?, details?}` |
| `cancelled` | `{branch, head_sha, workspace_kept}`, or `{reason: "requester_retired"}` when your peer was disabled |

An `output` over 8 KiB is replaced by `{output_truncated: true, summary}`.

**`failed` error codes.**

| `error` | Meaning |
|---|---|
| `policy_denied` | `reason`: `sender_kind`, `backend`, `preset`, `permission_mode`, `project`, `idle` or `expires_too_soon` |
| `cap_reached` | `details {limit, running, open}`; `limit` names the cap (`limits.max_sessions`, `max_open_sessions`, `launches_per_hour`, …) |
| `backend_unhealthy` | The backend is down; retry later or pick another launcher |
| `backend_error` | The launch call failed; `settled_orphan: true` when a half-started session was found and settled |
| `invalid_input` | `input` failed the schema, or the `job_request` is malformed |
| `unsupported` | Another `operation`, `continue_job`, or a second job in a thread |
| `session_exited` | A non-resumable agent process ended |
| `expired` | A post into the job thread was refused 422 `expired`; the session ended |
| `outcome_unknown` | A session was lost across a restart; the launcher never relaunches |

## P2 rollout

Every step here is **coordinator-only**: Max or the coordinator runs it, never a build task.

**Paseo on xsm** (coordinator-only).
1. D9 first, Max's action: fix `paseo-bot.service`'s bad setting and the failed `paseo-bridge-watch` and `paseo-cache-prune` units.
2. Move the `paseo` entry from `_disabled_backends` into `backends`, with `config` `{"bridge_bin": "/home/xertrov/.local/bin/paseo-bridge", "workspace_root": "<an absolute directory for launcher workspaces>", "presets": {"sol": {"preset": "<paseo-bridge preset>"}}}`.
3. `just ln-run check --config …` must show `paseo` healthy with its presets; then restart the unit.

A Paseo settle never removes work: the agent's commits stay on `launch/<job_id>` (pushed too, when the requester set `deliver.push_branch`), and the sweep reclaims a workspace only after `workspace_retention_ms`, only if it is clean and its branch is present.

**Build boxes x-tuf, x-p35, x-tall**, one at a time (coordinator-only):
1. Clone `t3-bridge` unmodified. Pairing an auto token there is Max's step; no shared token is touched.
2. Confirm that box's T3 (0.1.0–0.1.1) answers `t3-bridge projects --json`, `t3-bridge models --json` and `t3-bridge call t3_thread_list`.
3. Enrol `launcher-<box>` as in [Install on xsm](#install-on-xsm) and install the user unit there.
4. Presets name that box's live LLMP models (`llmp-gpt-6.1-sol`, …).
5. x-tall only: apply `~/.llm-general/buildboxes/llmp-harness-setup.md` first, and only if a preset `route` check against `127.0.0.1:18080` fails (D11).
6. Finish with one launch per box and confirm a turn reply.

## Manual smoke test on xsm with real T3

**Coordinator-only.** It uses only existing credentials, reads T3 only through the auto token, and never uses port 45752 or the production hub.

1. Start a throwaway hub:
   ```sh
   just build
   d=$(mktemp -d)
   bin/vecnet-hub init --data-dir "$d" --operator-name Max
   bin/vecnet-hub --listen 127.0.0.1:0 --data-dir "$d"     # note the address it prints
   ```
2. Enrol `launcher-smoke` (kind `launcher`) and a requester on that hub. Write a temporary config for project `vecnet4` (listed in `policies.default.projects`), `state_dir` inside `$d`, with the T3 preset `sol` (instance `codex`, model `ocx/gpt-6.1-sol`, its options written out).
3. `just ln-run run --config <tmp config>`.
4. From the requester, send a launch job (`expires_at` now + 1 d, `permission_mode: "auto"`) asking "list the files changed in the last commit and stop". Expect `accepted`, then `running` with a `t3-thread://` link, then one turn reply.
5. Send a `wake` follow-up in reply to the launch message, then a `launch.control: "settle"`. Expect a second turn reply and `succeeded {ended: "settled"}`.
6. Check that the T3 thread is titled `vl-<job_id> …` on branch `launch/<job_id>`, that `t3-bridge outbox list` shows no new event, and that `~/.local/state/t3-bridge/launches.json` has no `vl-` thread (no bridge wake was registered).
7. Stop the launcher and the hub, remove `$d`, and settle or archive the T3 thread.

Record the evidence (job ids, link, screenshots of the T3 thread) in the build's status directory, `.scratch/vecnet/build-launcher/`.

## When it breaks

| Symptom | Cause |
|---|---|
| Unit exits at once, `credential must be a readable regular file with mode 0600` | `chmod 600` the credential file |
| Unit exits 1 after running, and systemd restarts it every 10 s | The event stream ended for good (401/403, or the peer was disabled; the journal shows the stream ending with `auth` or `disabled`). Fix the credential or re-enable the peer |
| Unit exits 3 and stays stopped, `cursor_purged` | The hub no longer holds events the launcher missed while it was down, so launches, controls or inputs in that gap were never admitted. Check the open job threads and any launches sent to it meanwhile, then run once with `--accept-cursor-gap` (`node … main.ts run --config PATH --accept-cursor-gap`), which resumes from the hub's oldest retained event; then start the unit again |
| Every launch `backend_unhealthy`; presence `blocked` | `just ln-run check` shows which backend is down and why |
| `policy_denied` / `project` | The alias is not in the requester's `projects` ceiling (the default allows none) |
| `backend_error` on `full-access` (T3) | `vecnet-launcher` is not in `full_access.callers`; see [T3 prerequisites](#t3-prerequisites) |
| A requester's replies get 422 `expired` | It replied to an old reply; reply to the launch message |
