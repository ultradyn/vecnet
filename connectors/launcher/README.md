# vecnet launcher

A daemon that runs coding-agent sessions for `launch` jobs addressed to a vecnet peer of kind `launcher` ([PROTOCOL §13.8](../../docs/PROTOCOL.md#138-launcher-issue-26), §18). It relays the job thread into the agent, posts each finished turn back, and routes the agent's questions and permission prompts to the requester. **Operator and requester guide: [`docs/connectors/launcher.md`](../../docs/connectors/launcher.md)** (install, configuration, T3 prerequisites, P2 rollout, smoke test).

## Layout

- `src/main.ts`: `run --config PATH` (the daemon) and `check --config PATH` (backend health and live presets as JSON).
- `src/daemon.ts` wires the pieces: `ingress.ts` (event stream into the ledger), `dispatch.ts` (routes each Delivery), `policy.ts` (the launch gate and approval routing), `launch.ts` and `reattach.ts` (one session per job, across restarts), `session.ts` (the per-job lane: relay, priority mapping, turn replies, controls), `parent-bridge.ts` (questions and permission prompts), `idle.ts` (park, settle, deadline), `presence.ts`, `sweep.ts` (only `vl-` tagged sessions), `ledger.ts` and `outbox.ts` (SQLite, exactly-once posts).
- `src/backend.ts`: the plugin contract. Plugins live in `src/backends/<id>/` (`t3` through `t3-bridge`, `paseo` through `paseo-bridge`) and may import only `../../backend.ts`, their own files and `node:*`.
- `schema/`: the closed JSON Schemas of the `launch.*` objects. `config.example.json`: an xsm config to copy. `systemd/vecnet-launcher.service`: the user unit.

## Develop

Shared wire types, ports and fakes come from [`../hub-client`](../hub-client/README.md). Run `just ln-setup` once per worktree, then `just ln-check` (strict type check and unit tests, through `build2` with at most two workers) and `just ln-e2e` (against a real hub with a fake backend). On the build boxes: `just bb bash -c 'just ln-setup && just ln-check'`. Node 22.18 or newer runs the TypeScript as written; there are no runtime dependencies.

`just ln-run check --config …` and `just ln-run run --config …` run it from the checkout. `just ln-install` (coordinator or Max only) copies the checkout to `~/.local/share/vecnet-launcher/<sha>` and installs the unit without enabling it.

## Windows

Run Node 22.18+ and the **matching native `vecnet.exe`**. TypeScript has no
runtime dependencies or native Node addons. From PowerShell:

```powershell
npm install --prefix connectors/launcher --ignore-scripts --no-package-lock
./connectors/launcher/check-windows.ps1 -VecnetBin C:\path\to\vecnet.exe
node connectors/launcher/src/main.ts check --config C:\path\to\launcher.json
node connectors/launcher/src/main.ts run --config C:\path\to\launcher.json
```

Use absolute native paths (JSON requires doubled backslashes, or use `C:/...`).
Explicit `HOME` takes precedence; Windows falls back to `USERPROFILE`. The shared
Codex authority remains `<home>/.local/state/vecnet/codex/bindings.json`, independent
of XDG/profile selection. `VECNET_BIN` may pin the CLI used by native helpers.

Security is **not disabled on Windows**. Credential/route-secret/bindings reads
use `vecnet local-platform read-private`, validating the opened object's owner,
DACL, type, hard links and size before reading. Status writes use protected atomic
replacement; recovery directories have inheritable private DACLs. Unsafe existing
files/directories are refused, not silently repaired. Unix keeps its existing
mode/uid and `flock` rules. A missing/outdated helper fails closed.

The singleton status publisher starts `local-platform lock PATH`, waits at most
5 seconds for exactly `locked\n`, and keeps its stdin open. The native byte-range
lock survives heartbeats and releases on EOF/process death, with no stale-file
or PID-based fallback. The lock inode is never deleted. Loss of the helper stops
engines and reporting synchronously, including pending offline reports. The
connector's `stopped` promise settles after fenced cleanup with
`status_lock_lost`; the standalone runner exits **1** and the embedded launcher
also stops with exit code **1**, allowing the service manager's failure-restart
policy to reacquire the singleton. Normal signal shutdown remains exit **0** and
waits for lock release. No automatic unfenced in-process reacquisition occurs.

**Codex uses the existing daemon**, not a new app-server. On Windows the clients
run only native `codex.exe app-server proxy --sock <exact-codex-home>/app-server-control/app-server-control.sock`.
The proxy tunnels raw RFC6455 bytes (HTTP Upgrade, masked WebSocket frames), not
JSONL. It never uses `--listen stdio://` and never calls the shutdown endpoint.
Initialization/version, exact-root delivery and effective-policy fences remain.
Native binary discovery checks managed releases in the binding's home and the
selected/default user's `.codex`, the npm native vendor layout (also without
`APPDATA`), then native executables on PATH. `VECNET_CODEX_BIN` can pin an absolute
native executable for custom installs; every candidate is checked for safe owner
and DACL before execution. Custom `CODEX_HOME` changes the socket/state, not the
installation location. No npm shell shim is used for this control transport.

ACP still uses UTF-8 JSON-RPC stdio, with argv-preserving `.exe`/`.cmd` resolution
and owned-process-tree termination. T3/Paseo remain available when their bridge
and backend support Windows; Paseo's doctor remains authoritative while Linux
systemd-unit checks are Linux-only. Windows T3 advertises a smaller input budget
to stay below the platform's command-line limit. Subprocess cwd is native (no
GNU `env -C`). Tests run Windows DACL/locking/proxy coverage and portable shared
fixtures; Unix-only socket-layout/mode fixtures remain explicitly platform-scoped.

Batch/npm shims are argv-escaped with delayed expansion disabled; raw CR/LF arguments
and commands beyond cmd.exe's limit are refused before spawn. Prefer native bridge
executables or an explicit Node executable plus script for large/multiline payloads.

### Platform test coverage limitation

The Windows scoped suite currently skips 389 Codex scenarios whose shared test
fixtures still use Unix socket layouts, POSIX modes, and shell stand-ins. These
include **feature-level** policy, approval, exact-root, retry, receipt, recovery,
and lifecycle tests, not only syscall tests. They remain enabled on Linux; the
Windows native proxy/ACL/locking tests do not replace that exhaustive scenario
coverage. Seven additional Windows skips concern POSIX `/tmp` or symlink/`..`
semantics. No corresponding runtime feature is deliberately disabled by these
skips. Use both Linux and Windows suites for regression validation.
