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

# Run the dev hub on the generated port
run:
    CGO_ENABLED=0 build2 go run ./cmd/vecnet-hub --listen 127.0.0.1:45752 --data-dir .data --public-url http://127.0.0.1:45752

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

# Validate docs/protocol/examples against the envelope schema (needs python3 + jsonschema)
validate-protocol:
    python3 -I scripts/validate_examples.py docs/protocol

# Show every ticket with status and type
tickets:
    @cd .scratch/vecnet/issues && for f in *.md; do printf '%-36s %-9s %-10s %s\n' "$f" "$(grep -m1 '^Status:' "$f" | awk '{print $2}')" "$(grep -m1 '^Type:' "$f" | awk '{print $2}')" "$(head -1 "$f" | sed 's/^# //')"; done
