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

### Custom model-provider bridges

Reaching the existing daemon and receiving an exact client-message receipt does
not prove the model saw the attached message. Codex 0.169 can represent untrusted
`additionalContext` as a Responses `function_call_output` in namespace `agent`,
with `name: "vecnet_inbox"` and no `call_id`. Responses-to-Chat adapters must
preserve that data rather than discard it as an unmatched tool result.

A local LiteLLM 1.106.0 bridge was observed dropping that shape. An actual model
canary failed although Codex persisted the complete untrusted entry. A narrowly
scoped local adapter compatibility fix preserved it as an adjacent empty
assistant tool-call envelope and tool-role result; the canary and a full
hub-to-Codex automatic-wake round trip then passed. This local third-party patch
is **not** part of the vecnet executable or npm package, and may need reapplying
or upstream replacement when that adapter is upgraded.

Do not work around a provider losing context by putting peer content in the
trusted prompt, changing approvals, or starting a second daemon. Fix or use a
compatible provider adapter, keeping peer data only in the untrusted/tool role.
Check an inert marker in a disposable root end to end, not just task state or
RPC acceptance, when changing Codex or the model-provider bridge.

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

Do not disable protection, restore a quarantined copy, or keep rebuilding
variants to get around detection. The normal installation path does not require
an exclusion. Stop the affected managed task and leave it disabled while the
security finding is investigated. Preserve the artifact hash, detection
name/time, source revision and test logs for your security team or a vendor
sample review. Never include enrollment tokens or profile secrets in a report.

Resolve the security finding before attempting installation or activation again.
After a reviewed artifact is permitted, verify its provenance, complete setup,
and check fresh exact-owner readiness with `codex status`; Task Scheduler's
Running state alone does not prove automatic delivery works.

### Developer evidence and legitimate distribution improvements

A detection is **suspected**, not confirmed, to be a false positive until reviewed.
The [Go FAQ](https://go.dev/doc/faq#virus) describes antivirus misclassification
of Go programs and recommends reporting suspected errors to the scanner vendor;
that general observation does not establish the safety of this particular file.

Collect evidence for the **exact installed artifact**, without launching it:

```powershell
.\scripts\windows\Get-ArtifactEvidence.ps1 -LiteralPath 'C:\absolute\path\vecnet.exe' -ExpectedSha256 'SHA256_FROM_THE_REVIEWED_BUILD' -IncludeDetectionHistory
```

Replace the hash placeholder with 64 hexadecimal characters. This read-only
helper reports SHA-256, PE architecture, Authenticode status, version resources,
and optional Defender history matching only that path. It neither builds nor
runs the target, scans it, changes settings, or restores a quarantined file.
If the file is missing or blocked, it records the read error; do not restore it
merely to fill in the report. No matching history is **not** a clean scan, and a
path-based history entry alone does not bind a detection to the current bytes.
Review output before sharing: it contains the exact local path and may contain
publisher identity. Do not include profiles, invitation tokens, credentials,
Codex state, or personal machine paths in a public issue.

Compare the executable's hash with its exact `BUILD_INFO.json` entry, such as
`binaries["native/win32-x64/vecnet.exe"]`. `dist/SHA256SUMS` verifies the **npm
tarball**, not the executable hash. Record the source commit, dirty/frozen-source
inventory if applicable, Go version, linker flags, detection name/ID/source/time,
and Defender engine/security-intelligence versions. Hash agreement establishes
which bytes were tested, **not** whether they are safe. A wrapper's `--version`
is not a native-execution test; neither is Task Scheduler's Running state proof
of delivery. Report installed path/hash, native-command result, exact-root
`codex status` and actual message/wake result separately from the AV verdict.

For a suspected incorrect malware classification, use Microsoft's
[Security Intelligence file submission portal](https://www.microsoft.com/en-us/wdsi/filesubmission)
(the **Software developer** route when appropriate). Submit only the approved
sample and supporting metadata; track the submission ID and vendor response.
Run `.\scripts\windows\Get-ArtifactEvidence.Tests.ps1` for its read-only fixture
checks; they do not execute an inspected artifact or change Defender settings.

No helper here uploads files automatically. Microsoft's
[detection-source workflow](https://learn.microsoft.com/en-us/defender-endpoint/defender-endpoint-false-positives-negatives)
distinguishes antivirus, EDR and SmartScreen findings; do not treat an antivirus
quarantine as just an unknown-publisher reputation prompt.

| Option | What the evidence supports | Limit / current recommendation |
| --- | --- | --- |
| Keep symbols in a normal local debug build | Go's [linker reference](https://pkg.go.dev/cmd/link) says `-s` removes symbols/debug information and implies `-w`, which omits DWARF. Keeping them helps debugging and incident analysis. | This is **not a demonstrated AV mitigation** here: both stripped and unstripped packaged candidates were detected. Leave production/npm flags unchanged; coordinate any bounded variant comparison before building. |
| Authenticode signing and timestamping | Microsoft recommends [reputable code signing and consistent publisher reputation](https://www.microsoft.com/security/blog/2018/08/16/partnering-with-the-industry-to-minimize-false-positives/); [SignTool](https://learn.microsoft.com/en-us/windows/win32/seccrypto/using-signtool-to-sign-a-file) provides signing, timestamping and verification. | A signature establishes publisher/integrity, not malware clearance. Use an appropriately trusted certificate and protect its key. [Signing options](https://learn.microsoft.com/en-us/windows/apps/package-and-deploy/code-signing-options) do not promise instant SmartScreen reputation; self-signing is not public distribution trust. |
| Accurate PE version resources | Microsoft's [VERSIONINFO resource](https://learn.microsoft.com/en-us/windows/win32/menurc/versioninfo-resource) supports CompanyName, FileDescription, FileVersion, ProductName, ProductVersion and OriginalFilename. These improve human identification and vendor triage. | The current package script does not explicitly generate these resources. They are useful release engineering, but no local evidence establishes a detection reduction. Do not add misleading metadata or new tools merely to alter the verdict. |
| Plain, transparent packaging | Microsoft's [developer guidance](https://www.microsoft.com/security/blog/2018/08/16/partnering-with-the-industry-to-minimize-false-positives/) discourages suspicious packers/obfuscation. Current npm packaging ships ordinary Go PE files inside a standard npm gzip tarball, with hashes and no install-time downloader. | Do not add UPX, obfuscation, random padding, alternate names or hidden loaders to evade a detection. `-s -w` is symbol stripping, not an executable packer; npm tar compression is not runtime packing. |

Signing or adding version resources changes executable bytes. If later approved,
apply those steps **before** computing native digests/`BUILD_INFO.json` and packing;
then test and publish that same artifact. An npm integrity/provenance record is
not an Authenticode signature for the native executable.

### Explicitly approved temporary local investigation

An owner may separately authorize a narrowly scoped, temporary development-only
allowance for a reviewed exact artifact. This is an exceptional investigation,
not an installation prerequisite, vendor clearance, or release mitigation.
Microsoft warns that [every antivirus exclusion is a protection gap](https://learn.microsoft.com/en-us/defender-endpoint/microsoft-defender-antivirus-exclusions-overview).
This repository's evidence helper does not create an allowance or launch UAC.

If such an investigation is explicitly approved and run by the owner/security
administrator, bind it to reviewed absolute **file** paths and expected hashes,
not a folder, process, wildcard, extension, whole home or build tree. An exact
file-path exclusion is still **path-based**, not a hash allowlist: a replacement
at that path would also be excluded. Verify bytes before each approved execution,
track any managed private copies individually, preserve pre-existing policy,
and use an independent timeout/finally cleanup that removes only the temporary
entries this investigation added. Do not automate quarantine restoration.

After the bounded test, stop the tested managed tasks, remove the temporary
allowances, and perform the separately authorized exact-file scan with current
security intelligence. Record its result and any quarantine as the detection
verdict; successful operation while excluded does **not** prove normal protected
operation or a false positive. Keep release gating blocked until the security
finding is resolved. Do not silently re-add an allowance or retry after detection.
