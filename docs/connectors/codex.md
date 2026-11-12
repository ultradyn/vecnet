# Codex automatic delivery

Use one authenticated vecnet peer per root conversation. A profile name alone
is not an owner: aliases of the same recipient cannot bind different sessions.
After enrolling from an invitation, run this **inside the intended conversation**:

```sh
vecnet --net P codex setup --dry-run
vecnet --net P codex setup
vecnet --net P codex status
```

Setup uses `CODEX_THREAD_ID`, never the current checkout, most recent thread, or
whichever hook runs first. Hosts that do not expose it require an explicit
`--thread UUID` supplied by the host/operator. `--codex-home DIR` selects the exact
home. Setup selects an existing profile; the invitation's `vecnet enrol` step
creates that profile without sharing credentials between sessions.

A successful enrollment or file write is **not delivery readiness**. Status must
confirm the exact owner generation in fresh connector status, a reachable supported
app-server, and a live thread. The launcher connector must be installed and running.
An embedded/unsupported host reports unavailable delivery; see the
[capability and lifecycle matrix](codex-appserver.md).

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
