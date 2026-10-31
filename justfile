# vecnet task runner. Recipes grow as the project does.

set shell := ["bash", "-euo", "pipefail", "-c"]

default:
    @just --list

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
