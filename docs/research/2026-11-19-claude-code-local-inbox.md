# Claude Code local inbox delivery (2026-11-19)

**Question.** Can a standalone TUI find an already-running Claude Code session on the same machine and post text to it without tmux, a relay model turn, or a preinstalled vecnet hook?

**Answer.** Yes on Linux with Claude Code **2.1.322**, for a compatible session that exposes an inbox and accepts the post. We discovered a disposable headless session, posted a unique marker directly to its Unix socket, and got the exact marker back from the model. A second disposable session configured to refuse inbound messages did not run a turn, even though the socket write succeeded. The wire format and registry fields below are version-specific observations, not a stable API contract. Vecnet does not ship this adapter or the proposed TUI; see [the Claude Code connector guide](../connectors/claude-code.md) for the shipped Monitor and hook paths.

## Use from Claude Code

Run `/list-agents` (documented alias: `/peers`) to see reachable sessions. Use `/rename NAME` in the target or launch it with `claude --name NAME`, then ask the sending session, for example, “Tell @api-worker the migration finished.” Claude Code uses `ListAgents` for discovery when needed and `SendMessage` for delivery. A picked `@` mention identifies the target; duplicate names may require the short identifier shown in the listing. You do not need an agent team, tmux, or Remote Control for same-machine messaging.

The [upstream guide][messaging] says an accepted inbound message starts a turn if the agent is idle, or enters the active context between tool calls. Incoming peer messages are shown as a one-line preview; `Ctrl+O` opens the transcript viewer. Messages are plain text, not file attachments, slash commands, conversation history, or approval from the user. Native peer messaging can include a reply address or request a one-shot idle notice. The standalone socket post below does neither.

## Discovery on Linux

1. Run `claude agents --json` under the intended Claude Code profile. The installed build returned `id`, `sessionId`, `pid`, `name`, `cwd`, `kind`, `state`, `status`, and `startedAt`. The listing can include both interactive and background sessions; the disposable `-p` receiver also appeared while its stream was open. Not every kind of teammate or subagent is exposed as a standalone row.
2. In the configuration root (`$CLAUDE_CONFIG_DIR`, normally `~/.claude` when unset), read `sessions/<pid>.json`. In this build, its `messagingSocketPath` field was the inbox path. Match its `sessionId` against the CLI row before using it. At the time of inspection, all 15 listed rows on the test host had matching address metadata. This is an observed registry layout, not an upstream promise. If users run multiple config roots, treat the root as part of discovery; one CLI listing should not be assumed to cover every profile.
3. Use the recorded address rather than constructing it from the PID. The ordinary test-host path looked like `/run/user/<uid>/cc-socks/<pid>.sock`, and upstream documents a fallback under `/tmp/cc-socks-<uid>`. For manual diagnosis, `/status` shows **Peer address**; Claude Code also exports `CLAUDE_CODE_MESSAGING_SOCKET` to its own Bash commands and hooks. Neither the existence of an address nor a listing proves that the receiver will accept messages.

## Wire format on the tested build

Connect to the target's Unix domain stream socket. Send one UTF-8 JSON object terminated by a literal LF, then close the write side. This exact envelope comes from the installed executable's `[uds-messaging] Inject messages` debug guidance and was exercised live:

```json
{"type":"user","message":{"role":"user","content":"Echo this exact test marker:\nPEER-TUI-EXAMPLE"}}
```

The `\n` in `content` is a JSON escape; the record itself gets a separate framing LF. Use a JSON encoder, not shell interpolation, to preserve multiline and Unicode text. The successful probe did not send an auth line, a sender permission-mode claim, or a reply address. Upstream documents an optional `{"type":"auth","token":"…"}` first line on macOS/Linux for scripts posting back to their own session. A standalone sender aimed at other sessions does not need to read or forward that token on the tested Linux build.

**A successful write means only “sent to socket.”** The probe did not obtain an application-level delivery receipt for this envelope. Do not infer acceptance from connection success, process state, or a lack of socket errors. Do not automatically retry after an ambiguous write: the first post may already have reached the receiver.

## Permission and compatibility boundaries

- The test socket was mode `0600` and owned by the local user. Upstream documents OS-user restrictions on Unix sockets. Local socket transport does not pass through Anthropic servers; if the message starts or enters a model turn, the receiving session uses its configured model provider as usual.
- The receiving session applies `crossSessionInbound` (`accept`, `hold`, or `refuse`). With no explicit setting, the default checks the sender and receiver permission classes. A standalone post makes no permission-class assertion; the upstream guide documents that a bypassing receiver can hold it for approval. Explicit `refuse` was verified. The default/manual writable route was verified without an explicit inbound override in the test launch; the `hold` UI path and bypass default were not exercised.
- Do not impersonate a session, synthesize a permission claim, forward an own-child token, or change settings to force delivery. For a future vecnet connector, a local post is not a hub acknowledgment. Preserve the connector's untrusted-peer-text handling and existing claim/ack semantics; test how Claude Code renders and attributes this envelope before using it for arbitrary hub deliveries.
- The upstream availability floor for native messaging is v2.1.224 on macOS/Linux/WSL 2 and v2.1.248 on third-party providers or with feature-flag fetching disabled. The upstream guide lists v2.1.234 for native Windows, which uses authenticated named pipes, not this Unix recipe. These are upstream feature minimums, **not** tested adapter versions. Host/container and native Windows/WSL sessions do not share a local inbox domain. Bare `claude -p` does not bind an inbox according to upstream; the tested headless receiver was not started with `--bare`.

## Live evidence and reproduction

Both probes used Claude Code 2.1.322 on Linux. They launched fresh named receivers in disposable working directories, in manual permission mode, with hooks disabled, no tools, an empty MCP configuration, and the user's configured provider. The harness used `claude -p --verbose --input-format stream-json --output-format stream-json` and kept stdin open after the first prompt. Only the disposable receivers were written to. Both were stopped afterward.

| Probe | Observation |
| --- | --- |
| Accepted post | Sent a `READY` prompt through stdin. Matched the receiver's session ID and PID in `claude agents --json`, then read and checked its same-user Unix socket. Sent a multiline instruction containing `PEER-TUI-91069fbd2870e295` **only over the socket**. The next model result echoed that exact marker. |
| Explicit refusal | Used a fresh receiver with `crossSessionInbound: "refuse"`. After `READY`, posted a unique socket marker. The write succeeded but produced no model result during the observation window. A different marker sent through stdin then got its exact reply, and no extra result followed. |

To repeat, target a disposable receiver you started yourself. Wait for `READY`, resolve the registry entry as above, post a fresh marker using the exact envelope, and require a matching **model result** rather than just a successful write. For the negative control, launch another disposable receiver with explicit `refuse` and verify that a later stdin prompt still works. Do not substitute an existing work session, and do not treat the negative control as proof of a general delivery timeout. Interactive, busy-turn, `hold`, multi-profile aggregation, macOS, and Windows behavior were not live-tested.

## Proposed TUI boundary (not implemented)

- **Linux-first; send-only.** List compatible live local sessions; filter by name or working directory. Distinguish duplicate names with the directory and short session ID. Select one target, compose multiline text, and send only on explicit confirmation. No cloud, transcript viewer, attachments, broadcast, or replies in v1.
- **Separate discovery, transport, and UI.** Keep private registry and wire details in a versioned adapter. Before connecting, refresh the selected PID/session-ID pair, check the metadata and socket are owned by the current UID, and reject stale IDs, symlinked endpoints, or unexpected file types. Do not silently fall back to tmux/TTY injection or a relay model turn.
- **Useful failure states.** Distinguish missing inbox, stale target, connection/write error, and “sent to socket; delivery not confirmed.” Preserve the draft on failure, use bounded I/O, and avoid duplicate sends. Test discovery and exact framing with a fake Unix listener; use the two disposable-agent probes as compatibility checks.

## References

- [Claude Code cross-session messaging][messaging], especially [inbox sockets][inbox] and [inbound controls][inbound].
- [Claude Code tools reference](https://code.claude.com/docs/en/tools-reference) and [settings reference](https://code.claude.com/docs/en/settings-reference).
- [Claude Code changelog](https://github.com/anthropics/claude-code/blob/main/CHANGELOG.md).
- [Vecnet's current Claude Code connector](../connectors/claude-code.md) and [the analogous Codex app-server delivery spike](2026-11-04-codex-app-server-delivery.md).

[messaging]: https://code.claude.com/docs/en/cross-session-messaging
[inbox]: https://code.claude.com/docs/en/cross-session-messaging#the-sessions-inbox-socket
[inbound]: https://code.claude.com/docs/en/cross-session-messaging#control-inbound-messages
