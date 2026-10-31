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
