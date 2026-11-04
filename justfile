# vecnet task runner. Recipes grow as the project does.

set shell := ["bash", "-euo", "pipefail", "-c"]

default:
    @just --list

# Build both binaries into bin/
build:
    CGO_ENABLED=0 build2 go build -p 2 -o bin/ ./cmd/...

# Run the test suite (two threads, via build2)
test:
    build2 go test -p 2 ./...

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
    fly deploy --remote-only

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

# coordinator/Max only; never enables the unit
ln-install:
    #!/usr/bin/env bash
    set -euo pipefail
    dest="$HOME/.local/share/vecnet-launcher/$(git rev-parse --short HEAD)"
    mkdir -p "$dest"
    rsync -a --delete --exclude node_modules connectors/hub-client connectors/launcher "$dest/"
    ln -sfn "$dest" "$HOME/.local/share/vecnet-launcher/current"
    install -Dm0644 connectors/launcher/systemd/vecnet-launcher.service "$HOME/.config/systemd/user/vecnet-launcher.service"
    echo "installed $dest; run: systemctl --user daemon-reload"
