# Disposable native/ACP acceptance preparation

No models, real systemd services or builds were run for this preparation. Synthetic tests execute only a private Node timer through the generated unit argv. MAIN will provide final combined CLI/demo paths after display-proof and managed-repair integration. The previous package-gate demo was deliberately deleted with its private temporary directory; there is no retained final combined demo candidate to run now.

## Read-only inventory

- Reusable prior provider-only seed: `/tmp/vecnet-auto-ClcUTt/home/.codex/config.toml` (0600, private parent). Its filename/permissions were inspected; its contents were not printed or inspected. The automatic-session receipt attributes this fixture to the successful private provider run. Copy only that minimal config into a new private CODEX_HOME, never the old fixture's sessions, credentials/config for vecnet, bindings, messages or recovery state.
- Native executable: `/home/xertrov/.codex/packages/app-server-daemon/releases/0.166.0-x86_64-unknown-linux-musl/bin/codex`. Its `app-server --help` confirms direct `--listen unix://PATH`. Use a run-owned direct listener rather than daemon-manager commands; this avoids a package updater and touching any real daemon. The modern connector accepts 0.167.2 but the native job backend currently accepts only 0.166.0/0.167.0; a default 0.167.2 daemon would be refused. This plan uses the existing compatible binary without changing that allowlist.
- ACP executable: `/work/package-cache/xertrov/fleet/vecnet-acp/claude-agent-acp-0.101.0/bin/claude-agent-acp`. Its public nested `node_modules/@agentclientprotocol/claude-agent-acp/package.json` reports name `@agentclientprotocol/claude-agent-acp`, version `0.101.0`. The wrapper executes `/home/xertrov/.local/opt/node-v22.23.3-linux-x64/bin/node` and that package's `dist/index.js`. No ACP handshake/model process was started.
- ACP private authentication is not established by package presence. `/home/xertrov/.claude/settings.json` exists; neither its contents nor credentials were inspected. No `.claude/.credentials.json` or `.claude.json` was found in the retained automatic-session fixture. At execution, derive a private provider-only settings file from the existing user settings by retaining only provider environment keys/model; do not copy the user HOME, OAuth account state, hooks, MCP configuration or project trust. Fail before model dispatch if the minimal seed contains no configured credential. Launcher environment filtering intentionally removes provider secrets from inherited child environment, so the private settings seed must supply them inside the pinned adapter/CLI boundary.

## Reuse boundary

Use `scripts/native-launcher-live-smoke.mjs` unchanged for all scenarios. It already performs web enrollment/setup, exact request journaling/retry, initial/follow-up markers, native restart/same session, workspace/dirty-sentinel preservation, exact cancellation, unit verification/stop/removal and recovery retention. ACP uses the same live stdio owner and never claims restart resume.

Extend only the existing `scripts/automatic-session-smoke.mjs` private manager adapter and its tests. Default standalone behavior remains unchanged. `createFixtureSystemctl({root,home,kind:'launcher'})` validates profile unit names and generated environment/argv, executes the actual generated command directly, and stores exact argv/PID/start-time ownership. It never delegates to systemctl. An unresolved pre-spawn intent must refuse stop/restart/cleanup; absence of a subprocess projection never proves nonexecution. Profiles live exclusively inside the marked fixture root. The adapter must implement the native smoke's `restart`, `is-active`, `show ActiveState/MainPID`, `stop`, and `daemon-reload` calls, plus harmless explicit capability/metadata queries required by the installer.

Use a fresh canonical `/var/tmp/vecnet-launcher-accept-*` root **for each scenario**. Both native and ACP reject temp workspace roots under `/tmp`; private `$RUN/home/.local/share` under `/var/tmp` satisfies that deliberate persistence check. Place the run's HOME, CODEX_HOME, XDG directories, config, runtime assets, CLI copy, manager adapter, repo, logs and screenshots under that root. This is disposable fixture publication, not persistent user deployment.

## Concrete execution envelope, after final candidate

MAIN supplies `FINAL_SOURCE`, `FINAL_CLI`, `FINAL_DEMO`; all must identify the same final combined source. The commands below are preparation only, not executed here. Each run repeats this setup with the requested mode; no reused identities/backlog:

```bash
set -euo pipefail
umask 077
: "${FINAL_SOURCE:?}" "${FINAL_CLI:?}" "${FINAL_DEMO:?}"
source_root=$(realpath "$FINAL_SOURCE")
run_root=$(mktemp -d /var/tmp/vecnet-launcher-accept-XXXXXX)
mkdir -m700 -p "$run_root"/{home,bin,runtime,tmp,logs,shots} \
  "$run_root/home"/{.codex,.config,.cache,.local/share,.local/state}
install -m600 /tmp/vecnet-auto-ClcUTt/home/.codex/config.toml "$run_root/home/.codex/config.toml"
install -m700 "$FINAL_CLI" "$run_root/bin/vecnet"
install -m700 "$FINAL_DEMO" "$run_root/bin/demo"
printf %s fixture-v1 > "$run_root/.automatic-session-fixture"
```

Create the wrapper with literal private paths (Node here is the same approved Node used for the fixture):

```bash
node --input-type=module - "$run_root" "$source_root" <<'ADAPTER'
import { writeFile } from 'node:fs/promises';
import { pathToFileURL } from 'node:url';
const [root, source] = process.argv.slice(2);
const moduleURL = pathToFileURL(source + '/scripts/automatic-session-smoke.mjs').href;
const config = { root, home: root + '/home', kind: 'launcher' };
await writeFile(root + '/bin/systemctl', `#!${process.execPath}
import { createFixtureSystemctl } from ${JSON.stringify(moduleURL)};
try { process.stdout.write(await createFixtureSystemctl(${JSON.stringify(config)})(process.argv.slice(2))); }
catch { process.stderr.write('private fixture adapter refused operation\\n'); process.exitCode = 1; }
`, { mode: 0o700, flag: 'wx' });
ADAPTER
```

The factory defaults to the existing standalone adapter; `kind: 'launcher'` accepts only exact profile unit names. It parses the generated argv without a shell, validates owner-private nonsymlink files, serializes operations with a fail-closed lock, and writes fsynced spawn intent before starting Node. PID/start-time, exact argv, executable, UID and private HOME must still agree before status or signalling. Missing process projection after a recorded start, interrupted lock, unresolved intent or lingering process-group descendants retain ambiguity and refuse replacement/cleanup. It signals only the verified leader, never a broad group. This adapter models explicit restarts, not systemd's automatic restart policy or resource enforcement.

Launch subprocesses using the same environment sanitizer already present in `runSmoke`: remove inherited `CODEX_*`, `VECNET_*`, `XDG_*`, `GIT_*`, `BASH_ENV`, `ENV`, and `NODE_OPTIONS`, preserving provider authentication environment without printing it. Set private HOME/CODEX_HOME/XDG/TMPDIR, PATH beginning with `run_root/bin`, and `VECNET_LOCAL_MAINTENANCE=0`. Do not change real HOME variables in the parent session. With that fixture-only child environment:

```bash
# Native only: record exact executable, argv, HOME, PID and /proc start-time.
# Spawn detached; retain its private log and identity record before any model send.
(cd "$run_root"; exec setsid "/home/xertrov/.codex/packages/app-server-daemon/releases/0.166.0-x86_64-unknown-linux-musl/bin/codex" \
  app-server --listen "unix://$run_root/runtime/native.sock") \
  >"$run_root/logs/native.log" 2>&1 &
native_pid=$!
# Create private rendezvous parent and symlink; wait for the exact owned socket.
mkdir -m700 -p "$run_root/home/.codex/app-server-control"
ln -s "$run_root/runtime/native.sock" "$run_root/home/.codex/app-server-control/app-server-control.sock"
```

The parent execution controller must save and revalidate this child's PID/start-time/executable/argv/private HOME before signalling it. A failure retains the entire run root and journal; do not turn a missing PID projection into cleanup permission. On success, stop only this direct listener after the smoke's own exact job/unit teardown and retain the public receipt plus private recovery evidence as appropriate. There is no daemon-manager unit or updater to stop.

Create the harness's existing public `approval.json` from current bytes, not historical hashes: `source_revision=git -C FINAL_SOURCE rev-parse HEAD`, `snapshot_sha256=smokeSnapshotDigest(FINAL_SOURCE)`, CLI/demo SHA-256 of the run copies, and native `{executable,executable_sha256,version:'0.166.0'}`. For ACP add `{executable,executable_sha256,package_json,package_name:'@agentclientprotocol/claude-agent-acp',package_version:'0.101.0',entry,entry_sha256,node_executable,node_sha256,agent_name:'@agentclientprotocol/claude-agent-acp',agent_version:'0.101.0',protocol_version:1}` with the inventoried wrapper/package paths above. This manifest is byte/provenance input to the existing harness, not a new user approval gate; current user instructions already authorize acceptance. Never include provider secrets.

Under the private child environment, execute each native mode sequentially with its own setup/root/listener:

```bash
VECNET_BIN="$run_root/bin/vecnet" VECNET_UI_DEMO_BIN="$run_root/bin/demo" \
VECNET_SMOKE_APPROVAL="$run_root/approval.json" VECNET_NATIVE_LAUNCHER_SMOKE=1 \
VECNET_SMOKE_BACKEND=codex-app-server VECNET_SMOKE_WORKSPACE=worktree \
SHOTS_DIR="$run_root/shots" node "$source_root/scripts/native-launcher-live-smoke.mjs"
# Repeat from a NEW root with VECNET_SMOKE_WORKSPACE=project-root, then temp.
```

For ACP, omit native listener creation and retain a fresh private HOME/XDG/run root and the same validated profile adapter. At execution only, construct the private seed without printing its values:

```bash
python3 - "$run_root/home" <<'PYSEED'
import json, os, pathlib, sys
source = pathlib.Path('/home/xertrov/.claude/settings.json')
raw = json.loads(source.read_text())
allowed = {'ANTHROPIC_API_KEY','ANTHROPIC_AUTH_TOKEN','ANTHROPIC_BASE_URL',
           'ANTHROPIC_MODEL','ANTHROPIC_DEFAULT_HAIKU_MODEL',
           'ANTHROPIC_DEFAULT_SONNET_MODEL','ANTHROPIC_DEFAULT_OPUS_MODEL'}
values = {k:v for k,v in raw.get('env',{}).items()
          if k in allowed and isinstance(v,str) and v}
if not any(values.get(k) for k in ('ANTHROPIC_API_KEY','ANTHROPIC_AUTH_TOKEN')):
    raise SystemExit('No self-contained private ACP provider credential seed; no model dispatched')
seed = {'env':values}
if isinstance(raw.get('model'),str): seed['model'] = raw['model']
target = pathlib.Path(sys.argv[1])/'.claude'
target.mkdir(mode=0o700)
fd = os.open(target/'settings.json', os.O_WRONLY|os.O_CREAT|os.O_EXCL, 0o600)
with os.fdopen(fd,'w') as out: json.dump(seed,out)
PYSEED
```

This does not copy or execute `apiKeyHelper`, hooks, MCP settings, account files or trust. Then run:

```bash
VECNET_BIN="$run_root/bin/vecnet" VECNET_UI_DEMO_BIN="$run_root/bin/demo" \
VECNET_SMOKE_APPROVAL="$run_root/approval.json" VECNET_NATIVE_LAUNCHER_SMOKE=1 \
VECNET_SMOKE_BACKEND=acp VECNET_SMOKE_WORKSPACE=temp \
VECNET_ACP_COMMAND=/work/package-cache/xertrov/fleet/vecnet-acp/claude-agent-acp-0.101.0/bin/claude-agent-acp \
SHOTS_DIR="$run_root/shots" node "$source_root/scripts/native-launcher-live-smoke.mjs"
```

ACP's exact present constraint is the unverified private provider seed, not missing adapter software. If the existing user settings contain only an external helper/OAuth flow and no minimizable provider credential, preserve private failure evidence and report that exact unsupported seed requirement; do not substitute real HOME. A normal ACP initialize/health check alone does not prove provider availability or effective model identity.

The resulting acceptance establishes execution of the actual generated launcher unit command through the private process adapter, real model turns, native restart continuity, workspace isolation and teardown. It deliberately does not establish real user-systemd activation or production deployment.

## Synthetic verification

Build-box lane `merge55`, host `x-boss`, jobs 2, canonical physical TMPDIR:

```bash
BB_HOST=x-boss BB_JOBS=2 BB_LANE=merge55 just bb bash -c \
  'export TMPDIR="$(realpath "$TMPDIR")"; node --test scripts/automatic-session-smoke.test.mjs'
```

Initial RED: 7 existing tests passed, 5 new profile tests failed because the factory was absent (exit 1). GREEN includes exact generated command execution/restart/teardown, foreign-path/directive refusal, unresolved intent retention, same-PID/changed-argv refusal, missing projection and interrupted-lock refusal, and all original standalone cases. Individual logs: `.scratch/merge55/adapter-red.log` and `adapter-green.log`. Actual native/ACP acceptance remains pending the final combined binaries and source; these synthetic results do not establish model execution or real systemd activation.

## Prepared controller invocation

`scripts/launcher-disposable-smoke.mjs` automates the envelope above, reusing the existing live smoke rather than replacing its journal or assertions. It starts nothing without `--run`. MAIN must first confirm the final combined source and both build-box-produced binaries. Each invocation creates and retains a new private `/var/tmp` root; parent HOME is never modified. Failure preserves the listener and all authority rather than guessing teardown safety. The controller writes only evidence paths to stdout; command/provider output goes to private logs. No local compilation occurs.

```bash
# Set only after MAIN supplies the actual final source revision and binaries.
export FINAL_SOURCE=/home/xertrov/.codex/worktrees/3013/vecnet
export FINAL_CLI="$FINAL_SOURCE/.scratch/merge55/final-artifacts/vecnet"
export FINAL_DEMO="$FINAL_SOURCE/.scratch/merge55/final-artifacts/demo"
# Each command is a distinct run root, identities, repository and backlog.
SMOKE_WORKSPACE=worktree node "$FINAL_SOURCE/scripts/launcher-disposable-smoke.mjs" --run
SMOKE_WORKSPACE=project-root node "$FINAL_SOURCE/scripts/launcher-disposable-smoke.mjs" --run
SMOKE_WORKSPACE=temp node "$FINAL_SOURCE/scripts/launcher-disposable-smoke.mjs" --run
SMOKE_BACKEND=acp SMOKE_WORKSPACE=temp node "$FINAL_SOURCE/scripts/launcher-disposable-smoke.mjs" --run
```

Run sequentially and inspect each private `recovery.json` before proceeding. The native listener is stopped only after the existing smoke exits zero and produces a matching passing backend/workspace receipt with proven cancellation and teardown. PID/start-time/argv/executable/UID/private HOME must match again before its leader receives SIGTERM; remaining descendants fail the controller. There is no automatic SIGKILL, broad group kill or cleanup of an ambiguous failure. Interrupted spawn intent and the retained ownership journal are recovery authority, not permission to infer nonexecution.
