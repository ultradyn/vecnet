# vecnet task runner. Recipes grow as the project does.

set shell := ["bash", "-euo", "pipefail", "-c"]

default:
    @just --list

# Build both binaries into bin/
build:
    CGO_ENABLED=0 build2 go build -p 2 -ldflags "-X github.com/XertroV/vecnet/internal/mcpserver.Version=$(just version) -X github.com/XertroV/vecnet/internal/buildinfo.Version=$(just version) -X github.com/XertroV/vecnet/internal/buildinfo.CommitDate=$(git show -s --format=%cI HEAD)" -o bin/ ./cmd/...

# Build version (MCP serverInfo.version): UTC date.time + short commit (+dirty)
version:
    @echo "$(date -u +%Y.%m%d.%H%M)+$(git rev-parse --short HEAD)$(git diff --quiet HEAD -- 2>/dev/null || echo .dirty)"

# Run the test suite (two threads, via build2)
test:
    build2 go test -p 2 ./...

# Isolated hub benchmark; invoke via BB_HOST=x-boss BB_JOBS=2 just bb GOMAXPROCS=2 just bench ...
bench *ARGS:
    CGO_ENABLED=0 build2 go build -p 2 -o bin/vecnet-bench ./bench
    bin/vecnet-bench {{ARGS}}

# Run the dev hub on the generated port; extra flags pass through
# (e.g. `just run --dev-ui-dir internal/ui/static` serves the operator UI from disk)
run *ARGS:
    CGO_ENABLED=0 build2 go run ./cmd/vecnet-hub --listen 127.0.0.1:45752 --data-dir .data --public-url http://127.0.0.1:45752 {{ARGS}}

# Check built binaries carry no x86 ISA-needed stamp (must print 0 for each; see docs/DEPLOY.md)
isa-check: build
    for b in bin/vecnet-hub bin/vecnet; do n=$(readelf -n "$b" | grep -c 'x86 ISA needed' || true); echo "$b: $n"; [ "$n" = 0 ]; done

# Initialise a local dev hub's operator in .data/ (prints the secret once)
hub-init NAME="Max":
    CGO_ENABLED=0 build2 go run ./cmd/vecnet-hub init --data-dir .data --operator-name {{NAME}}

# Deploy to Fly (builds the image remotely from Dockerfile; Max runs this, tests never do)
fly-deploy:
    ~/.fly/bin/flyctl deploy --remote-only -a vecnet --yes --build-arg VECNET_VERSION=$(just version) --build-arg VECNET_COMMIT_DATE=$(git show -s --format=%cI HEAD)

# Create the operator on the deployed hub (prints the secret once)
fly-init:
    fly ssh console -C "/vecnet-hub init --data-dir /data --operator-name Max"

# Copy the live hub.db off the Fly volume to backups/ (manual; nightly copy: docs/DEPLOY.md)
fly-backup:
    mkdir -p backups
    fly ssh sftp get /data/hub.db "backups/hub-$(date -u +%Y%m%dT%H%M%SZ).db"

# Show the wayfinder frontier: open, unblocked, unclaimed tickets
frontier:
    #!/usr/bin/env bash
    cd .scratch/vecnet/issues
    for f in *.md; do
        status=$(grep -m1 '^Status:' "$f" | awk '{print $2}')
        [ "$status" = "open" ] || continue
        blocked=$(grep -m1 '^Blocked by:' "$f" | sed 's/^Blocked by:[[:space:]]*//' | tr -d ',')
        ok=1
        for b in $blocked; do
            dep=$(ls "$b"-*.md 2>/dev/null | head -1)
            [ -n "$dep" ] && grep -q '^Status: resolved' "$dep" || ok=0
        done
        [ "$ok" = 1 ] && echo "$f  [$(grep -m1 '^Type:' "$f" | awk '{print $2}')]  $(head -1 "$f" | sed 's/^# //')" || true
    done

# Check the embedded schema copy has not drifted, then validate docs/protocol/examples
# against the envelope schema (the example check needs python3 + jsonschema; skipped without it)
validate-protocol:
    diff -u docs/protocol/envelope.schema.json internal/envelope/schema.json || { echo "internal/envelope/schema.json has drifted from docs/protocol/envelope.schema.json" >&2; exit 1; }
    if python3 -I -c 'import jsonschema' 2>/dev/null; then python3 -I scripts/validate_examples.py docs/protocol; else echo "skip: python3 jsonschema not installed (examples are also checked by go test ./internal/envelope)"; fi

# Show every ticket with status and type
tickets:
    @cd .scratch/vecnet/issues && for f in *.md; do printf '%-36s %-9s %-10s %s\n' "$f" "$(grep -m1 '^Status:' "$f" | awk '{print $2}')" "$(grep -m1 '^Type:' "$f" | awk '{print $2}')" "$(head -1 "$f" | sed 's/^# //')"; done

# Independent demo, seeded once; --reset starts a fresh scenario.
ui-demo *ARGS:
    CGO_ENABLED=0 build2 go run -p 2 ./internal/opseed/demo --listen 127.0.0.1:49575 --data-dir .demo-data --dev-ui-dir internal/ui/static {{ARGS}}

# Pure UI model tests.
test-ui:
    build2 node --test --test-concurrency=2 internal/ui/jstest/

# Opt-in live Codex hook check, coordinator-run on a prepared build box only.
codex-smoke: build
    build2 node scripts/codex/hook-smoke.mjs

# Opt-in live Codex app-server connector check: coordinator-run on xsm only, with
# Max's go-ahead (VECNET_CODEX_APPSERVER_SMOKE=1); otherwise prints `skipped: …`.
codex-appserver-smoke: build
    build2 node --disable-warning=ExperimentalWarning scripts/codex-appserver-smoke.mjs

# Screen scripts run serially; helpers are not executable checks.
ui-shots:
    #!/usr/bin/env bash
    set -euo pipefail
    bash scripts/ui/reap-demos.sh
    build2 node --test --test-concurrency=2 scripts/ui/lib.test.mjs scripts/ui/closer.test.mjs
    for script in scripts/ui/*.mjs; do
        case "$script" in */lib.mjs|*/tmux.mjs|*/ansi.mjs|*/shots-index.mjs|*.test.mjs) continue ;; esac
        build2 node "$script"
    done
    build2 node scripts/ui/shots-index.mjs

# Run screen scripts N times each, run i into $SHOTS_DIR/<script>/run-<i>, to
# check their PNGs agree. UI_CPU_THROTTLE=4 slows the browser to expose load races.
# e.g. just bb SHOTS_DIR=.scratch/vecnet/mine/rep just ui-repeat 3 sidebar thread
ui-repeat N +SCRIPTS:
    #!/usr/bin/env bash
    set -euo pipefail
    base="${SHOTS_DIR:-.scratch/ui-repeat}"
    bash scripts/ui/reap-demos.sh
    failed=0
    for s in {{SCRIPTS}}; do
        for i in $(seq 1 {{N}}); do
            if SHOTS_DIR="$base/$s/run-$i" build2 node "scripts/ui/$s.mjs"; then echo "ui-repeat: ok $s run-$i"
            else echo "ui-repeat: FAIL $s run-$i"; failed=$((failed + 1)); fi
        done
    done
    exit $((failed > 0))

# Run a command on a build box against this worktree: sync, run, stream,
# pull back SHOTS_DIR + files it wrote, exit with its status (docs/BUILDBOX.md)
[positional-arguments]
bb +ARGS:
    bash {{quote(justfile_directory())}}/scripts/bb/run.sh "$@"

# Remove one lane (a worktree basename) from the build boxes
bb-clean LANE:
    bash {{quote(justfile_directory())}}/scripts/bb/clean.sh {{quote(LANE)}}

tui *ARGS:
    CGO_ENABLED=0 build2 go run -p 2 ./cmd/vecnet tui {{ARGS}}

# launcher: first-time setup in a worktree (dev dependencies only)
ln-setup:
    #!/usr/bin/env bash
    set -euo pipefail
    cd connectors/launcher
    manager=(pnpm)
    command -v pnpm >/dev/null 2>&1 || manager=(corepack pnpm)
    build2 "${manager[@]}" install --frozen-lockfile --child-concurrency=2

# launcher + hub-client: type-check
ln-typecheck:
    #!/usr/bin/env bash
    set -euo pipefail
    cd connectors/launcher
    manager=(pnpm)
    command -v pnpm >/dev/null 2>&1 || manager=(corepack pnpm)
    GOMAXPROCS=2 build2 "${manager[@]}" exec tsc -p tsconfig.json

# launcher + hub-client: unit tests; pass files to narrow
ln-test *ARGS:
    #!/usr/bin/env bash
    set -euo pipefail
    shopt -s nullglob
    files=({{ARGS}})
    [ ${#files[@]} -gt 0 ] || files=(connectors/hub-client/test/*.test.ts connectors/launcher/test/*.test.ts connectors/launcher/test/backends/*.test.ts)
    build2 node --disable-warning=ExperimentalWarning --test --test-concurrency=2 "${files[@]}"

ln-check: ln-typecheck ln-test

# launcher: end to end against a real hub and fake backend
ln-e2e: build
    build2 node --disable-warning=ExperimentalWarning --test --test-concurrency=1 connectors/launcher/test/e2e/*.test.ts

ln-run *ARGS:
    node --disable-warning=ExperimentalWarning connectors/launcher/src/main.ts {{ARGS}}

# coordinator/Max only; refreshes active delivery owners, never enables launcher
ln-install:
    #!/usr/bin/env bash
    set -euo pipefail
    umask 077
    dest="$HOME/.local/share/vecnet-launcher/$(git rev-parse --short HEAD)"
    install -d -m0700 "$HOME/.local/share/vecnet-launcher"
    mkdir -p "$dest"
    rsync -a --delete --exclude node_modules --exclude '.env*' --exclude '*.pem' --exclude '*.key' --exclude auth.json --exclude 'credentials*' connectors/hub-client connectors/launcher "$dest/"
    ln -sfn "$dest" "$HOME/.local/share/vecnet-launcher/current"
    install -Dm0644 connectors/launcher/systemd/vecnet-launcher.service "$HOME/.config/systemd/user/vecnet-launcher.service"
    node --input-type=module - "$dest" <<'JS'
    import {execFileSync} from 'node:child_process';
    import {chmodSync, readFileSync, realpathSync, renameSync, writeFileSync} from 'node:fs';
    import {dirname, isAbsolute, join} from 'node:path';
    const home = process.env.HOME, destination = process.argv[2];
    const requested = process.env.VECNET_BIN || execFileSync('/bin/sh', ['-c', 'command -v vecnet'], {encoding:'utf8'}).trim();
    if (!isAbsolute(requested)) throw new Error('VECNET_BIN must name an absolute installed executable');
    const binary = realpathSync(requested), node = realpathSync(process.execPath);
    const entry = realpathSync(join(destination, 'launcher/src/codex/delivery-main.ts'));
    const path = dirname(node) + ':/usr/local/bin:/usr/bin:/bin';
    for (const value of [home,binary,node,entry,path]) if (!/^\/[A-Za-z0-9_./+:-]+$/.test(value)) throw new Error('installed paths require unambiguous systemd tokens');
    const metadata = {version:1,binary,entry,node,path};
    const template = readFileSync('connectors/launcher/systemd/vecnet-delivery@.service','utf8').replaceAll('@BINARY@',binary).replaceAll('@ENTRY@',entry).replaceAll('@PATH@',path);
    const unit = join(home,'.config/systemd/user/vecnet-delivery@.service');
    writeFileSync(unit+'.tmp',template,{mode:0o600}); renameSync(unit+'.tmp',unit);
    const manifest = join(home,'.local/share/vecnet-launcher/delivery.json');
    writeFileSync(manifest+'.tmp',JSON.stringify(metadata)+'\n',{mode:0o600}); chmodSync(manifest+'.tmp',0o600); renameSync(manifest+'.tmp',manifest);
    JS
    systemctl --user daemon-reload
    node --input-type=module - <<'JS'
    import {execFileSync} from 'node:child_process';
    import {existsSync, readFileSync, readdirSync} from 'node:fs';
    import {join} from 'node:path';
    const home = process.env.HOME;
    const installed = JSON.parse(readFileSync(join(home,'.local/share/vecnet-launcher/delivery.json'),'utf8'));
    const root = join(process.env.XDG_DATA_HOME || join(home,'.local/share'),'vecnet/codex-delivery');
    const env = {...process.env,VECNET_BIN:installed.binary,VECNET_CODEX_DELIVERY_ENTRY:installed.entry,VECNET_CODEX_AUTO_DELIVERY:'0'};
    if (existsSync(root)) for (const entry of readdirSync(root,{withFileTypes:true})) {
      if (!entry.isDirectory() || !/^[0-9a-f]{64}$/.test(entry.name) || !existsSync(join(root,entry.name,'descriptor.json'))) continue;
      const run = command => execFileSync(installed.binary,['codex',command,'--hub-id',entry.name],{env,encoding:'utf8',timeout:10000});
      if (JSON.parse(run('delivery-status')).running === true) run('delivery-refresh');
    }
    JS
    echo "installed $dest; delivery template reloaded and active owners refreshed"

# Check a deployed hub's MCP and OAuth contracts (bearer in VECNET_MCP_TOKEN).
mcp-conformance ORIGIN:
    node scripts/mcp-conformance.mjs {{quote(ORIGIN)}}

# Stage the npm package with CLI + hub binaries for Linux x64/arm64.
npm-build:
    build2 node scripts/npm/build.mjs

# Pack exactly the staged release files; output: dist/vecnet-<version>.tgz.
npm-pack: npm-build
    npm pack ./dist/npm --pack-destination ./dist

# Verify the packed release in an isolated npm prefix and temporary hub.
npm-check:
    build2 node --test --test-concurrency=2 scripts/npm/*.test.mjs

# Bump the npm version on a clean master checkout, then push a release tag to GitHub.
release VERSION:
    #!/usr/bin/env bash
    set -euo pipefail
    if [ -n "$(git status --porcelain)" ] || [ "$(git branch --show-current)" != master ]; then
        echo "run releases from a clean master checkout" >&2
        exit 1
    fi
    release_version={{quote(VERSION)}}
    node --input-type=module - "$release_version" <<'JS'
    import {releaseInfo} from './scripts/npm/release.mjs';
    releaseInfo({name: 'vecnet', version: process.argv[2]});
    JS
    remote_tag=$(git ls-remote gh "refs/tags/v$release_version")
    if git show-ref --verify --quiet "refs/tags/v$release_version" || [ -n "$remote_tag" ]; then
        echo "release tag v$release_version already exists" >&2
        exit 1
    fi
    npm --prefix npm version "$release_version" --no-git-tag-version --ignore-scripts --package-lock=false
    git add npm/package.json
    git commit -m "chore: release v$release_version"
    git tag "v$release_version"
    git push --atomic gh HEAD:refs/heads/master "refs/tags/v$release_version"
