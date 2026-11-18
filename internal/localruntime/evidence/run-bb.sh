#!/usr/bin/env bash
# Retained invocation wrapper: run from repository root; all execution is on BB.
set -euo pipefail
export BB_HOST=x-boss BB_JOBS=2 BB_LANE=ci80-concurrent BB_PULLBACK=0 GOMAXPROCS=2
just bb GOMAXPROCS=2 bash -c '
set -eu
umask 022
unset ACP_TEST_ROOT
private=$(mktemp -d /tmp/ci80-concurrent.XXXXXXXX)
chmod 0700 "$private"
private=$(cd "$private" && pwd -P)
export TMPDIR="$private/tmp" HOME="$private/home"
export XDG_CONFIG_HOME="$HOME/.config" XDG_CACHE_HOME="$HOME/.cache" XDG_DATA_HOME="$HOME/.local/share" XDG_STATE_HOME="$HOME/.local/state" XDG_RUNTIME_DIR="$private/run"
mkdir -m 0700 -p "$TMPDIR" "$HOME" "$XDG_CONFIG_HOME" "$XDG_CACHE_HOME" "$XDG_DATA_HOME" "$XDG_STATE_HOME" "$XDG_RUNTIME_DIR"
trap '\''rm -rf -- "$private"'\'' EXIT
"$@"
' bash "$@"
