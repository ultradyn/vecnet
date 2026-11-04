#!/usr/bin/env bash
# just bb-clean <lane>: remove one lane (~/bb-src/vecnet4/<lane>) from the
# build box(es). Shared .toolchain and .cache are never touched here.
# Hosts: BB_HOST, else every reachable host in BB_HOSTS.
set -euo pipefail
lane=${1:?usage: just bb-clean <lane>}
[[ $lane =~ ^[a-z0-9][a-z0-9._-]{0,63}$ ]] || { echo "bb: bad lane name '$lane'" >&2; exit 64; }
ssh_opts=(-o BatchMode=yes -o ConnectTimeout=4 -a -x)
read -r -a hosts <<<"${BB_HOST:-${BB_HOSTS:-x-tall x-p35 x-tuf x-19}}"
for host in "${hosts[@]}"; do
  home=$(timeout 6 ssh "${ssh_opts[@]}" "$host" 'printf %s "$HOME"' </dev/null 2>/dev/null) || { echo "bb: $host unreachable, skipped" >&2; continue; }
  [[ $home == /home/* && $home != *"'"* ]] || { echo "bb: unexpected remote HOME '$home' on $host" >&2; continue; }
  path="$home/bb-src/vecnet4/$lane"
  # Full path only, never a glob; a live run holds no remote lock, so check
  # for processes whose cwd is inside the lane first.
  ssh "${ssh_opts[@]}" "$host" "
    [ -d '$path' ] && [ ! -L '$path' ] || { echo 'bb: $host: no lane $lane'; exit 0; }
    for p in /proc/[0-9]*; do
      case \"\$(readlink \"\$p/cwd\" 2>/dev/null)\" in '$path'|'$path'/*) echo 'bb: $host: lane $lane has a live process ('\${p#/proc/}'); not removing' >&2; exit 1;; esac
    done
    chmod -R u+w -- '$path' && rm -rf -- '$path' && echo 'bb: $host: removed $path'
  " </dev/null
done
rm -f -- "${XDG_RUNTIME_DIR:-/tmp}"/vecnet-bb/*-"$lane".lock
