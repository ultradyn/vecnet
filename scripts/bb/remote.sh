#!/usr/bin/env bash
# Runs ON the build box, from the lane's synced source. run.sh invokes it as
#   remote.sh <lane> <base64 NUL-separated NAME=VALUE env> <base64 NUL-separated argv>
# so no argument passes through a remote shell's quoting.
set -uo pipefail

BASE="$HOME/bb-src/vecnet4"
# The box may symlink bb-src onto another volume. Derive every private path
# from that physical base so canonical temp/data comparisons agree.
BASE=$(cd "$BASE" && pwd -P) || exit 64
lane=$1
RL="$BASE/$lane"
TC="$BASE/.toolchain"
[[ $lane =~ ^[a-z0-9][a-z0-9._-]{0,63}$ && -d $RL/src ]] || { echo "bb: bad lane $lane" >&2; exit 64; }

declare -a envs argv
mapfile -d '' -t envs < <(printf '%s' "$2" | base64 -d)
mapfile -d '' -t argv < <(printf '%s' "$3" | base64 -d)
((${#argv[@]})) || { echo "bb: no command" >&2; exit 64; }

# A clean environment: only what run.sh forwarded plus the lane's own paths.
# HOME is lane-private so nothing reads or writes the box user's dotfiles.
mkdir -p "$RL/home/.local/share" "$RL/tmp"
[[ -d $TC/fonts ]] && ln -sfn "$TC/fonts" "$RL/home/.local/share/fonts"
cleanenv=(
  "HOME=$RL/home" "USER=${USER:-$(id -un)}" "LOGNAME=${LOGNAME:-$(id -un)}" "LANG=C.UTF-8" "TERM=dumb"
  "XDG_CONFIG_HOME=$RL/home/.config" "XDG_CACHE_HOME=$RL/home/.cache" "XDG_DATA_HOME=$RL/home/.local/share"
  "TMPDIR=$RL/tmp" "TMUX_TMPDIR=$RL/tmp" "REAP_ROOTS=$RL/tmp"
  "PATH=$RL/bb/bin:$TC/go/bin:$TC/node/bin:$TC/py/bin:/usr/local/bin:/usr/bin:/bin"
  "GOTOOLCHAIN=local" "GOTELEMETRY=off" "GOCACHE=$BASE/.cache/go-build" "GOMODCACHE=$BASE/.cache/gomod" "GOPATH=$RL/home/go"
  "PLAYWRIGHT_BROWSERS_PATH=$TC/ms-playwright" "PLAYWRIGHT_MODULE=$TC/playwright-1.61.1/node_modules/playwright/index.mjs"
  "npm_config_cache=$BASE/.cache/npm" "npm_config_update_notifier=false"
  "VECNET_BB=1" "VECNET_BB_LANE=$lane"
)
jobs=8 goflags=""
for kv in "${envs[@]}"; do
  case "$kv" in
    BB_JOBS=*) jobs=${kv#BB_JOBS=} ;;
    GOFLAGS=*) goflags=${kv#GOFLAGS=} ;;
    *) cleanenv+=("$kv") ;;
  esac
done
[[ $jobs =~ ^[1-9][0-9]?$ ]] || { echo "bb: BB_JOBS must be 1-99" >&2; exit 64; }
cleanenv+=("BB_JOBS=$jobs" "GOFLAGS=-p=$jobs${goflags:+ $goflags}")

# `go … -p N` (the xsm two-thread habit) becomes -p $BB_JOBS on the box.
# bin/build2 (shipped beside this script) applies the same rule to `build2 go …`.
if [[ ${argv[0]} == go ]]; then
  for ((i=1; i<${#argv[@]}; i++)); do
    case "${argv[i]}" in
      -p) ((i+1 < ${#argv[@]})) && argv[i+1]=$jobs ;;
      -p=*) argv[i]="-p=$jobs" ;;
    esac
  done
fi

cd "$RL/src"
rm -f -- "$RL/changed.list"
: >"$RL/.runstamp"
sleep 0.01
# setsid: the command leads its own process group, so the watchdog can stop
# all of it. nice: the box is shared with other agents' lanes.
setsid env -i "${cleanenv[@]}" nice -n 10 timeout --kill-after=30 "${BB_TIMEOUT:-7200}" "${argv[@]}" </dev/null &
pid=$!

# If the ssh session goes away (Ctrl-C, dropped link), this shell is
# reparented; stop the command group instead of leaving an orphan run.
parent=$PPID
(
  while kill -0 "$pid" 2>/dev/null; do
    if [[ $(ps -o ppid= -p $$ 2>/dev/null | tr -d ' ') != "$parent" ]]; then
      kill -TERM -- "-$pid" 2>/dev/null
      sleep 5
      kill -KILL -- "-$pid" 2>/dev/null
      exit 0
    fi
    sleep 1
  done
) &
watchdog=$!
wait "$pid"
rc=$?
kill "$watchdog" 2>/dev/null
wait "$watchdog" 2>/dev/null

# Source files the command wrote (goldens, committed shots, go.sum): run.sh
# pulls these back. The prunes mirror run.sh's sync excludes.
find . \( -name .git -o -name node_modules -o -name .worktrees -o -name __pycache__ \
       -o -path ./.data -o -path ./.demo-data -o -path ./.scratch -o -path ./bin \) -prune \
     -o -type f -newer "$RL/.runstamp" -printf '%P\0' >"$RL/changed.list"
exit "$rc"
