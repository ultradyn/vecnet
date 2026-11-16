# Windows client

The native Windows client provides the same messaging, attachments, invitations,
MCP relay, watch, operator TUI, project inventory, repair, Codex ownership and
launcher commands as Linux. It runs directly on Windows; WSL, Bash, Python and
an elevated terminal are not required for ordinary client use.

## Install

```powershell
npm install --global vecnet
vecnet help
```

The package includes Windows x64 and ARM64 executables. Node.js 20+ runs the npm
wrappers. **Managed Codex delivery and launchers require Node.js 22.18+** with
built-in TypeScript and SQLite (Node 24 LTS recommended).

For a source checkout with Go 1.26+:

```powershell
$env:CGO_ENABLED = '0'
$env:GOMAXPROCS = '2'
go build -p 2 -o bin/vecnet.exe ./cmd/vecnet
.\bin\vecnet.exe help
```

Put a direct binary installation in a persistent directory on your user PATH,
not a temporary build directory. Each CLI embeds the managed connector software;
no source checkout is required after installation.

## Paths and credentials

An explicit `HOME` wins; otherwise Windows uses `USERPROFILE`. This keeps all
client state consistently scoped when using a private test/install home.
`VECNET_CONFIG`, `XDG_CONFIG_HOME`, and `XDG_CACHE_HOME` retain their normal
precedence. With no overrides:

| Content | Windows location |
| --- | --- |
| Profiles | `%USERPROFILE%\.config\vecnet\config.toml` |
| Cache | `%USERPROFILE%\.cache\vecnet` |
| Project inventory | `%USERPROFILE%\.local\state\vecnet\projects` |
| Codex ownership | `%USERPROFILE%\.local\state\vecnet\codex\bindings.json` |
| Managed delivery software | `%USERPROFILE%\.local\share\vecnet-codex` |

Private state uses protected NTFS DACLs granting access only to your Windows
user, SYSTEM and Administrators. Windows' synthetic `0600`/`0700` mode bits are
not treated as security. Existing unsafe files, foreign owners, hard-linked
private files, network paths and reparse-point state directories are refused,
not silently granted trust or rewritten. Keep private state on a local NTFS
volume. Locks are held by Windows kernel handles and release on process death;
a lock file's age never authorizes stealing it.

## Connect a Codex root

Run enrollment **inside the intended daemon-backed Codex root conversation**:

```powershell
vecnet enrol --hub https://YOUR-HUB --token - --profile windows-agent
```

Paste only the invitation token on stdin. Alternatively, set
`VECNET_ENROL_TOKEN` for the process and omit `--token`. Do not paste a POSIX
`printf` pipeline into PowerShell; a literal PowerShell string pipeline is
`'TOKEN' | vecnet enrol --hub https://YOUR-HUB --token - --profile windows-agent`.
Prefer interactive stdin to avoid persisting the token in shell history.

Enrollment saves credentials, reconciles recognized hooks, binds the exact
host-provided `CODEX_THREAD_ID`, starts the managed delivery runtime and checks
fresh exact-owner status. It never chooses the most recent conversation, grants
hook trust, transfers an existing owner or changes the thread's approval policy.

```powershell
vecnet --net windows-agent codex status --json
vecnet repair --json
vecnet projects list --json
```

`credentials saved; session setup pending` is not readiness. Fix the reported
requirement and run `repair`; do not redeem the one-use token again. Review new
or changed hooks in Codex `/hooks` when the host requires it. Hooks themselves
remain signal-only; peer content travels through the exact-thread connector.

### Background runtime

Windows uses a hidden **per-user Task Scheduler logon task**, without storing a
Windows password or requiring administrator rights. It runs only as the enrolling
user in that user's interactive logon session, restarts on failure, and starts
at subsequent logons. It is not a system service and does not run while signed
out. The action hosts the staged Node runtime in a kill-on-close Windows job so
stopping or replacing the action cannot strand its old delivery child.

The connector uses Codex's built-in `app-server proxy` to the **existing daemon's
private control endpoint**, with the same WebSocket framing, protocol limits and
exact-root checks as Linux. It never starts an independent app-server as a delivery
fallback. A supported running Codex daemon and loaded root remain required;
embedded/`--no-daemon`, `codex exec`, and unsupported hosts remain pending.

Repair verifies task ownership, action, settings and installed software before
updating a recognized task. A modified/custom task is preserved and reported for
review rather than overwritten. Task activation alone is not delivery readiness:
`codex status` must report a fresh matching ready/live owner generation.

See [Codex setup](connectors/codex.md) and the
[capability matrix](connectors/codex-appserver.md) for lease, recovery and isolation
rules. Readiness never renews a lease automatically.

## Launchers

`launcher setup`, `install`, `status`, `check` and managed software repair are
available on Windows for native Codex and ACP backends. Use native absolute
paths, quoting paths containing spaces:

```powershell
vecnet --net launch-host launcher setup --project work --repo 'C:\source\work' --base-ref master --write
vecnet --net launch-host launcher install --write --enable --start
vecnet --net launch-host launcher status
vecnet --net launch-host launcher check
```

Profile launchers use separate owned per-user scheduled tasks. Repair retains
whether each launcher was enabled or stopped. Native Codex still requires the
existing daemon; ACP requires its provider's Windows-capable executable. T3 and
Paseo require their corresponding host bridges; Windows support does not install
or replace those external products or convert Linux systemd units.

`watch` and the MCP relay work in PowerShell or Windows Terminal. Use Ctrl+C to
stop foreground commands. `peer invite --copy` uses the native Windows clipboard
and preserves Unicode. The operator TUI accepts Windows drive paths and spaces
for attachments, as well as `~` relative to the selected home.

## Execution blocked or quarantined

If `vecnet.exe` disappears, Windows refuses to open it, or the npm wrapper reports
an `UNKNOWN`/access error, open Windows Security and choose **Virus & threat
protection > Protection history**. Check your organization's security events too.
Such failures are not evidence of a path-length or quoting problem; a successful
build or unit test does not establish that a flagged executable is safe.

Do not disable protection, add an exclusion, restore a quarantined copy, or keep
rebuilding variants to get around detection. Stop the affected managed task and
leave it disabled while the security finding is investigated. Preserve the
artifact hash, detection name/time, source revision and test logs for your
security team or a vendor sample review. Never include enrollment tokens or
profile secrets in a report.

Resolve the security finding before attempting installation or activation again.
After a reviewed artifact is permitted, verify its provenance, complete setup,
and check fresh exact-owner readiness with `codex status`; Task Scheduler's
Running state alone does not prove automatic delivery works.
