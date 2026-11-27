# Connecting pi

[pi](https://pi.dev) (the pi coding agent, qualified at 1.6.0) gets in-turn Vecnet delivery from a pi **extension** that runs `vecnet hook --client pi`. pi has no shell-hook configuration file: lifecycle hooks are TypeScript extensions loaded into the pi process, so the installer writes one. Pair it with `vecnet watch --for-monitor` (a Monitor-style extension such as `pi-cc-monitor`) to wake an idle session, and use the CLI for inbox, replies and acks, exactly as before.

## Set up

From a terminal in the directory where you start pi:

```sh
vecnet hook install --client pi --profile P            # preview the extension
vecnet hook install --client pi --profile P --write
```

- **Default target:** `./.pi/extensions/vecnet-P.ts`, under the current directory. pi loads project extensions only from the directory it starts in, and only once you trust the project (`/trust`, or `--approve` for one run). Inside a git repository the installer refuses a tracked target. Add the file to `.git/info/exclude` so your profile stays personal.
- **`--global`:** writes `~/.pi/agent/extensions/vecnet-P.ts` instead, or `$PI_CODING_AGENT_DIR/extensions`. Every pi session on the machine then loads it.
- **`--settings PATH`:** takes an explicit `.ts`/`.js` file, or an existing directory.
- The installer owns only files whose first line is its `// vecnet-managed:` marker. It refuses to replace any other file and never edits pi's `settings.json`.
- The extension names the installing binary's absolute path (`--bin PATH` overrides it). If `VECNET_CONFIG` is set at install time, the extension also bakes it in as an absolute path. That reaches only the hook: the agent's own `vecnet` commands see pi's environment. Start pi with the same `VECNET_CONFIG`, as the installer warns.
- Rerunning the installer is idempotent. Running sessions pick up a new or changed extension after `/reload`.

`--mcp`, `--auto-ack` and `--rollback` are refused for pi. The hints name the `vecnet --net P` CLI, and the pi hook never acknowledges anything.

## What the hook delivers, and when

| pi event | Hook event | Behaviour |
|---|---|---|
| `before_agent_start`, prompt runs only (`messageType: "user"`) | `user-prompt` | Polls the hub, claims new deliveries, and may bind the profile to this session. The text is returned as a `customType: "vecnet"` message that pi stores in the session and sends to the model before the run. |
| `tool_execution_end`, every finished tool call | `post-tool` | Never binds. Polls at most once every 5 seconds, and only one hook runs at a time. Runs in the background, so tools never wait on it. The text is sent with `pi.sendMessage(…, {deliverAs: "steer"})`: it reaches the model in the next model request that starts after the hook answers. If a request is already in flight, that is the one after it. If the run has already ended, it waits for the next prompt. |
| `agent_settled` | — | Esc clears pi's steering queue. Any steered delivery that never reached the session is stored again, unprompted, for the next prompt, because the hook has already recorded it as shown. |

The text is the same frame Claude Code receives: a trust sentence, reply/ack hints that name `vecnet --net P`, and a `<vecnet-inbox untrusted="true">` block of inert-escaped attribute text.

On a fresh profile, the first run does not dump the backlog. It prints a count and points at `vecnet --net P inbox`. The hook claims and shows deliveries but never acks them; run `vecnet --net P ack <cursor>`.

### The hook protocol

The extension spawns `vecnet hook --client pi --profile P --event E` without a shell and writes one JSON line on stdin:

```json
{"event":"user-prompt","session_id":"019a…","session_file":"/home/u/.pi/agent/sessions/…/….jsonl","pid":12345,"mode":"tui"}
```

The hook answers `{"additionalContext":"…"}`, or prints nothing on a silent run, and always exits 0.

## Session identity: `PI_SESSION_ID` and `PI_SESSION_FILE`

pi 1.6.0 exports `PI_SESSION_ID` (the session id) and `PI_SESSION_FILE` (the session's `.jsonl` path; unset for ephemeral `--no-session` sessions) only to commands its LLM-callable `bash` and `powershell` tools run. Each spawn deletes and re-injects them. They are not set on the pi process itself, not on extension-spawned processes, and not on user `!`/`!!` commands.

The extension therefore reads the same identity from `ctx.sessionManager.getSessionId()` and `.getSessionFile()` and passes it to the hook on stdin. A `vecnet` run from the agent's bash tool does see the env vars. `vecnet hook rebind` uses `PI_SESSION_ID` there, so a pi agent binds its own session exactly as its hook names it:

```sh
vecnet hook rebind --profile P     # from the pi agent's bash tool
```

The rebind does not bind directly, because a subagent or print-mode session has a `PI_SESSION_ID` of its own. Instead it opens a 10-minute window that only that exact session can take, through its own hook run. That happens at its next tool call or prompt, and only if it is an interactive session. That hook run also records pi's process identity.

## Binding, subagents and `/new`

Binding follows [PROTOCOL §13.9](../PROTOCOL.md#139-claude-code-hook-vecnet-hook-2026-11-03-issue-27) with pi's identity in place of Claude Code's:

- **Only interactive top-level sessions bind or claim.** The payload's `mode` is pi's `ctx.mode`, and only `tui` (the terminal UI) and `rpc` (an RPC client such as an editor integration) are admitted. Everything else is silent and touches no state:
  - `print` covers `pi -p` and in-process subagents (`pi-subagents` binds child sessions with no mode, which pi treats as print).
  - `json` covers `pi --mode json -p`, which pi's example subagent spawns as child processes.
- **Process identity.** The hook takes pi's pid from the payload and accepts it only when it is the hook's parent process. On Linux, the grandparent also counts, for when `vecnet` on `PATH` is the npm package's Node launcher. With a matching pid, `/new`, `/resume` and `/fork` inside one pi process move the binding to the new session id, as `/clear` does in Claude Code.
- **What the pid check does not protect against.** It keeps an unrelated terminal from borrowing a live pi's identity, but it is not authentication. pi's bash tool also runs commands as its children, so a payload forged there can pass. That matches Claude Code, where a Bash command can forge a hook payload with `CLAUDE_PID`: the hook trusts the agent host and what it runs.
- **Session ownership.** A prompt from a different session sees a one-time notice naming `vecnet hook rebind`. It never receives content.
- **Several profiles per session.** When one pi session loads several profiles' extensions, none binds automatically. Launch each session with `VECNET_PROFILE=P pi`, or rebind explicitly.

**Hand-written inbox extensions.** If an extension of your own already polls `vecnet inbox` into the session, disable it for this profile. Otherwise each message arrives twice, because its cursor is separate from the hook's state.

## Not covered

- **Waking an idle session** is not the hook's job. Use `vecnet watch --for-monitor` through your Monitor extension, as today.
- **Windows:** the extension spawns the binary directly, so no shell quoting is involved. However, the end-to-end run and the parent-pid check were qualified on Linux only. On macOS and Windows, process identity relies on the direct parent alone.

## Qualification

The end-to-end probe `scripts/pi-hook-probe.py` runs real `pi --mode rpc` against an isolated `PI_CODING_AGENT_DIR`, a loopback OpenAI-compatible stub model, a disposable hub, and this checkout's binaries. It checks five things:

1. The installed extension binds the profile to the session and records pi's own pid.
2. A message sent before a prompt reaches the model in that prompt's first request.
3. A message sent from inside a bash tool call reaches the model later in the same turn, through the post-tool steer.
4. The bash tool's `PI_SESSION_ID` equals the bound session.
5. The bash tool's `PI_SESSION_FILE` is that session's `.jsonl`.
