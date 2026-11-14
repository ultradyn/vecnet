# Codex automatic delivery

Use one authenticated vecnet peer per root conversation. A profile name alone
is not an owner: aliases of the same recipient cannot bind different sessions.
Run the invitation's enrollment command **inside the intended root conversation**:

```sh
vecnet enrol --hub https://YOUR-HUB --token - --profile P
```

Paste only the token on stdin. Enrollment is the single ordinary setup action:
it saves credentials, reconciles recognized vecnet hooks, binds the exact root,
activates the matching managed runtime and checks actual delivery readiness.
It uses the host-provided `CODEX_THREAD_ID`, never cwd, the most recent thread or
whichever hook runs first. New sessions, forks and children do not inherit ownership.

`credentials saved; session setup pending` means the token was redeemed and the
profile exists, but automatic delivery is not ready. Fix the specific reported
requirement and run `vecnet repair`; it retries saved enrollment intent for the
original root without redeeming the one-use token again or following the thread
that happens to run repair. Do not request a new invitation just for setup retry.

```sh
vecnet --net P codex status
vecnet repair --json
```

Ready requires fresh connector status for the exact owner generation, a reachable
supported app-server and a loaded root. Service activation or a successful file
write alone is insufficient. Managed activation requires Linux user systemd and
Node ≥22.18 with working built-in TypeScript and SQLite. Unsupported/embedded
hosts and hosts requiring an unavailable bridge remain pending; use explicit
`vecnet --net P inbox`. See the [capability matrix](codex-appserver.md).

For advanced setup with already saved credentials, preview
`vecnet --net P codex setup --dry-run`, then run `vecnet --net P codex setup`.
If the host does not expose the root ID, the host/operator must supply exact
`--thread UUID`; `--codex-home DIR` selects its exact home. Dry-run does not bind
or write. Trust review in Codex `/hooks` remains a host-required interaction;
setup never approves it. An existing peer owner requires explicit addressed
handoff, not automatic replacement.

## Local inventory and maintenance

```sh
vecnet projects list --json
vecnet projects forget /absolute/project/path
vecnet repair --status --json
vecnet repair --json
```

Ordinary project-scoped CLI use, enrollment and setup register projects locally
for this OS user. The inventory records canonical roots, Git/worktree identity
when available, first/last use, observed CLI version, managed artifact paths and
successfully applied versions/hashes, migration progress and pending/deferred
work. Path aliases deduplicate; separate worktrees and non-Git directories remain
supported. It contains no credentials or message bodies and grants no delivery
ownership. Merely observing a directory does not install hooks there.

First ordinary use after a CLI update, including npm updates and direct binary
replacement, schedules background maintenance with bounded startup work. This
also adopts recognized legacy vecnet hooks on first use of an unrecorded project.
Help, inventory inspection and delivery internals do not recursively run setup.
`vecnet repair --status --json` reads the last maintenance attempt without running
repair or changing files; `not_run` means no saved attempt exists.
`repair` explicitly reconciles registered managed artifacts across projects and
user-level targets, updates the managed runtime/private CLI and retries pending
enrollments. Seeing a newer executable is distinct from successfully applying
configuration or connector updates. Keep checking deferred/pending work; an update
command finishing does not prove all installations or sessions are ready.

Missing/inaccessible directories and changed managed paths are deferred for
review; maintenance does not recreate deleted projects. Restore access or resolve
the reported conflict, then retry repair. Forgetting removes only the inventory
entry: it does not uninstall files, revoke credentials, unbind sessions, erase
recovery state or cancel separately saved enrollment intent. Ordinary use may
register that directory again. Repair and upgrades never hand off ownership or
renew expired bindings. Custom content, backups and recovery state remain intact.

## Shared hooks and migration

The installer now writes generic, profile-free handlers:

```sh
vecnet hook install --client codex --settings .codex/hooks.json
vecnet hook install --client codex --settings .codex/hooks.json --write
vecnet hook install --client codex --settings .codex/hooks.json --rollback BACKUP_PATH --write
```

The preview shows the proposed migration. Only identifiable vecnet-owned command
handlers are replaced. Foreign/custom shell commands remain unchanged and require
manual inspection. A saved backup permits rollback only while the current file
matches the installed result, so rollback cannot overwrite subsequent edits.
Review user-level hooks, nested projects, and neighboring worktrees too: Codex loads
matching hooks from multiple config layers. Never assume one project file replaces
another layer. New or changed hook definitions require trust review in `/hooks`;
setup never changes trust state.

`SessionStart`, `UserPromptSubmit`, and `PostToolUse` are signal-only for Codex.
They do not poll, claim, emit peer text, advance cursors, or acknowledge deliveries.
The [official hook contract](https://developers.openai.com/codex/hooks) says a
subagent may carry its parent's `session_id`, and SessionStart also runs for
subagents. Neither event nor ID alone proves that hook output reaches the root.
Async hooks also cannot wake an idle conversation. Delivery therefore goes through
the exact-thread app-server connector, not additionalContext on a lifecycle hook.
A legacy hook recognized as running in Codex also stays silent. Even without a
client marker, lifecycle hooks and `hook rebind` cannot use a registered Codex
recipient (including aliases) or its delivery-state file. They take the same
ownership lock through output, commit and automatic acknowledgement. Expiry does
not give a legacy hook ownership. For an unregistered recipient, an unmarked
custom hook remains indistinguishable from an older Claude hook; review custom
handlers before migration is complete.

## Ownership and recovery

All automatic delivery for one OS user shares the canonical authority
`$HOME/.local/state/vecnet/codex/bindings.json`, with mode 0600. A binding records
canonical config path, profile/`--as`, normalized hub, authenticated caller,
canonical Codex home, exact thread ID, expiry, and a random generation. A different
`VECNET_CODEX_BINDINGS` authority is refused. Disposable tests isolate `HOME`.
This is local per-user arbitration; it does not arbitrate independent users or
machines against the same hub recipient.

Peer credentials bind directly. A principal credential must select one owned
peer with `--as PEER_ID`; the hub must support the scoped
`GET /v1/peers/me?peer=PEER_ID` identity check. An older hub that cannot return
`recipient_id` cannot verify that principal binding. Credential rotation is
supported when the authenticated caller and recipient remain the same.

Renewal keeps the owner generation. Moving to a different conversation requires
an addressed compare-and-swap:

```sh
vecnet codex bindings --json
vecnet --net P codex bind --thread TARGET_UUID --expect-generation CURRENT_GENERATION
```

A stale generation fails. A live send holds the same interprocess lock as handoff;
a send whose outcome is unknown leaves a durable fence. Handoff waits for active
sends and refuses unresolved ones until exact-thread receipt recovery finishes.
Each RPC attempt has a distinct durable identifier, so recovery for an earlier
attempt cannot settle a later retry's fence.
Unsent claims return to the replay queue; accepted claims remain shown. Never
remove state files to hide duplicates. `hook rebind`'s next-hook window is not a
Codex recovery mechanism.

Acknowledgements remain explicit. A claim, successful stdout write, or accepted
RPC is not proof the model processed a message. The connector reconciles exact
thread receipts; agents record processing and ack only after handling the work.
`vecnet --net P inbox` remains an explicit manual operation, including for `hold`
messages. It is not an automatic identity bypass.
