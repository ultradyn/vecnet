#!/usr/bin/env bash
# Synthetic regression: exercise the real wrapper without toolchain installs.
set -euo pipefail

bb_test_wrapper=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)/remote.sh
bb_test_root=$(mktemp -d "${TMPDIR:-/tmp}/vecnet-bb-paths.XXXXXX")
trap 'rm -rf -- "$bb_test_root"' EXIT
bb_test_root=$(cd "$bb_test_root" && pwd -P)
bb_test_home="$bb_test_root/fixture home"
bb_test_volume="$bb_test_root/physical volume"
bb_test_base="$bb_test_volume/vecnet4"
bb_test_lane=physical-paths
mkdir -p "$bb_test_home" "$bb_test_base/$bb_test_lane/src"
ln -s "$bb_test_volume" "$bb_test_home/bb-src"

bb_test_probe='
set -euo pipefail
bb_test_base=$1
bb_test_rl="$bb_test_base/$2"
bb_test_tc="$bb_test_base/.toolchain"
bb_test_checks=0
bb_test_equal() {
  if [[ $2 != "$3" ]]; then
    printf "FAIL: %s: expected <%s>, got <%s>\n" "$1" "$3" "$2" >&2
    exit 1
  fi
  bb_test_checks=$((bb_test_checks + 1))
}
bb_test_equal HOME "$HOME" "$bb_test_rl/home"
bb_test_equal TMPDIR "$TMPDIR" "$bb_test_rl/tmp"
bb_test_equal TMUX_TMPDIR "$TMUX_TMPDIR" "$bb_test_rl/tmp"
bb_test_equal REAP_ROOTS "$REAP_ROOTS" "$bb_test_rl/tmp"
bb_test_equal XDG_CONFIG_HOME "$XDG_CONFIG_HOME" "$bb_test_rl/home/.config"
bb_test_equal XDG_CACHE_HOME "$XDG_CACHE_HOME" "$bb_test_rl/home/.cache"
bb_test_equal XDG_DATA_HOME "$XDG_DATA_HOME" "$bb_test_rl/home/.local/share"
bb_test_equal GOCACHE "$GOCACHE" "$bb_test_base/.cache/go-build"
bb_test_equal GOMODCACHE "$GOMODCACHE" "$bb_test_base/.cache/gomod"
bb_test_equal GOPATH "$GOPATH" "$bb_test_rl/home/go"
bb_test_equal npm_config_cache "$npm_config_cache" "$bb_test_base/.cache/npm"
bb_test_equal PLAYWRIGHT_BROWSERS_PATH "$PLAYWRIGHT_BROWSERS_PATH" "$bb_test_tc/ms-playwright"
bb_test_equal PLAYWRIGHT_MODULE "$PLAYWRIGHT_MODULE" "$bb_test_tc/playwright-1.61.1/node_modules/playwright/index.mjs"
bb_test_equal PATH "$PATH" "$bb_test_rl/bb/bin:$bb_test_tc/go/bin:$bb_test_tc/node/bin:$bb_test_tc/py/bin:/usr/local/bin:/usr/bin:/bin"
bb_test_equal cwd "$(pwd -P)" "$bb_test_rl/src"
bb_test_equal BB_JOBS "$BB_JOBS" 2
bb_test_equal GOFLAGS "$GOFLAGS" -p=2
bb_test_equal argc "$#" 3
bb_test_equal argv "$3" "literal whitespace argument"
printf "PASS: physical lane paths and framed argv/environment (%s assertions)\n" "$bb_test_checks"
'
bb_test_env=$(printf '%s\0' BB_JOBS=2 | base64 -w0)
bb_test_argv=$(printf '%s\0' bash -c "$bb_test_probe" bb-physical-paths \
  "$bb_test_base" "$bb_test_lane" 'literal whitespace argument' | base64 -w0)
env HOME="$bb_test_home" bash "$bb_test_wrapper" "$bb_test_lane" "$bb_test_env" "$bb_test_argv"
