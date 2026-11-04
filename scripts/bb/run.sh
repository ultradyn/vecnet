#!/usr/bin/env bash
# just bb [NAME=VALUE...] <command...>
# Sync this worktree to a build box, run the command there with output
# streamed, pull back SHOTS_DIR and any source files the command wrote, and
# exit with the command's status. See docs/BUILDBOX.md.
set -euo pipefail

PROJECT=vecnet4
# The wrapper ships its own scripts/bb to the lane, so the worktree being
# tested need not contain them: ROOT is the checkout of the caller's cwd
# (BB_ROOT overrides), SELF is where this script lives.
SELF=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
say() { printf 'bb: %s\n' "$*" >&2; }
die() { say "$1"; exit "${2:-2}"; }
ROOT=${BB_ROOT:-$(git rev-parse --show-toplevel 2>/dev/null || true)}
ROOT=$(cd "${ROOT:-$SELF/../..}" && pwd -P)
[[ -f $ROOT/go.mod && $(head -1 "$ROOT/go.mod") == "module github.com/XertroV/vecnet" ]] \
  || die "$ROOT is not a vecnet checkout; run from one or set BB_ROOT"
now() { date +%s.%N; }
secs() { awk -v a="$1" -v b="$2" 'BEGIN { printf "%.1f", b - a }'; }

# Forwarded from the caller's environment when set. Inline NAME=VALUE
# arguments may add other names, except anything that looks like a secret.
ALLOW=(CGO_ENABLED CC CXX GOFLAGS GORACE GOEXPERIMENT GODEBUG UPDATE_GOLDEN SHOTS_DIR REAP_MIN_AGE BB_JOBS BB_TIMEOUT NODE_OPTIONS)
read -r -a extra <<<"${BB_ENV:-}"
ALLOW+=("${extra[@]}")
secretish='(KEY|TOKEN|SECRET|PASS|CRED|AUTH|COOKIE|SESSION)'

lane=${BB_LANE:-$(basename "$ROOT" | tr 'A-Z' 'a-z')}
[[ $lane =~ ^[a-z0-9][a-z0-9._-]{0,63}$ ]] || die "lane '$lane' is not [a-z0-9][a-z0-9._-]*; set BB_LANE"

declare -A env=()
for name in "${ALLOW[@]}"; do
  [[ $name =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || die "bad BB_ENV name '$name'"
  [[ ${!name+set} ]] && env[$name]=${!name}
done
while (($#)) && [[ $1 =~ ^([A-Za-z_][A-Za-z0-9_]*)=(.*)$ ]]; do
  env[${BASH_REMATCH[1]}]=${BASH_REMATCH[2]}
  shift
done
(($#)) || die "usage: just bb [NAME=VALUE...] <command...>" 64
for name in "${!env[@]}"; do
  [[ ${name^^} =~ $secretish ]] && die "refusing to forward $name to a build box"
done
env[BB_JOBS]=${env[BB_JOBS]:-8}

# --- host: BB_HOST pins one; otherwise the first reachable in BB_HOSTS ---
ssh_opts=(-o BatchMode=yes -o ConnectTimeout=8 -o ServerAliveInterval=15 -o ServerAliveCountMax=8 -a -x)
reachable() { timeout 6 ssh "${ssh_opts[@]}" -o ConnectTimeout=4 "$1" true </dev/null >/dev/null 2>&1; }
cache_dir=${XDG_RUNTIME_DIR:-/tmp}/vecnet-bb
mkdir -p "$cache_dir"
if [[ -n ${BB_HOST:-} ]]; then
  host=$BB_HOST
  reachable "$host" || die "BB_HOST=$host is unreachable" 255
else
  # x-alpha (48 threads) is preferred once it is back; until then it is left
  # out so no run pays its failed probe. BB_HOSTS="x-alpha x-tall …" restores it.
  read -r -a hosts <<<"${BB_HOSTS:-x-boss x-tall x-p35 x-tuf x-19}"
  host=""
  # A failed probe of the first box costs up to 6 s (mDNS); remember the
  # fallback for 10 minutes so a run of checks does not pay it each time.
  cached="$cache_dir/host"
  if [[ -f $cached && $(find "$cached" -mmin -10 2>/dev/null) ]]; then
    h=$(<"$cached")
    for c in "${hosts[@]}"; do [[ $c == "$h" ]] && reachable "$h" && host=$h && break; done
  fi
  if [[ -z $host ]]; then
    for h in "${hosts[@]}"; do
      if reachable "$h"; then host=$h; break; fi
      say "$h unreachable, trying next"
    done
  fi
  [[ -n $host ]] || die "no build box reachable (tried ${hosts[*]}); set BB_HOST" 255
  if [[ $host != "${hosts[0]}" ]]; then printf '%s' "$host" >"$cached"; else rm -f -- "$cached"; fi
fi

# One run per lane at a time: a second sync would rewrite the tree under a
# running command. Runs from other worktrees use other lanes and do not wait.
exec 9>"$cache_dir/$host-$lane.lock"
flock -w "${BB_LOCK_WAIT:-3600}" 9 || die "lane $lane busy on $host" 75

t0=$(now)
remote_home=$(ssh "${ssh_opts[@]}" "$host" 'printf %s "$HOME"' </dev/null)
[[ $remote_home == /* ]] || die "could not read remote HOME on $host" 255
RL="$remote_home/bb-src/$PROJECT/$lane"
owner="$(hostname):$ROOT"
[[ $ROOT != *"'"* ]] || die "worktree path contains a single quote"
# Lanes are named by worktree basename; refuse a lane another checkout owns.
ssh "${ssh_opts[@]}" "$host" "mkdir -p '$RL/src' && cd '$RL' && if [ -f owner ] && [ \"\$(cat owner)\" != '$owner' ] && [ '${BB_FORCE_LANE:-}' != 1 ]; then echo \"bb: lane $lane belongs to \$(cat owner); set BB_LANE\" >&2; exit 3; fi; printf %s '$owner' >owner" </dev/null

# Local stat snapshot, so pull-back never overwrites a file edited during the run.
declare -A pre=()
prunes=(\( -name .git -o -name node_modules -o -name .worktrees -o -name __pycache__ -o -path ./.data -o -path ./.demo-data -o -path ./.scratch -o -path ./bin \) -prune)
while IFS= read -r -d '' rec; do
  rest=${rec#* * }; pre[$rest]=${rec%" $rest"}
done < <(cd "$ROOT" && find . "${prunes[@]}" -o -type f -printf '%P\0' | xargs -0 -r stat --printf '%s %.9Y %n\0' --)

excludes=(--exclude=.git --exclude=node_modules/ --exclude=.worktrees/ --exclude=__pycache__/ --exclude='*.pyc'
  --exclude=/.data/ --exclude=/.demo-data/ --exclude=/.scratch/ --exclude=/bin/ --exclude=/backups/
  --exclude=.env --exclude='.env.*' --exclude='*.pem' --exclude='*.key' --exclude=auth.json --exclude=.netrc --exclude='credentials*')
# --delete is confined to the lane's own src/; excluded paths (box-side
# node_modules, bin/) are left alone so installs persist between runs.
rsync -rlptz --delete --safe-links "${excludes[@]}" -e "ssh ${ssh_opts[*]}" "$ROOT/" "$host:$RL/src/"
# The wrapper's own files must all arrive: without bin/build2 every recipe and
# screen script that calls build2 dies on the box with exit 127. (It was once
# lost to the repo's `bin/` gitignore; .gitignore now re-includes it.)
for f in remote.sh bootstrap.sh bin/build2; do
  [[ -f $SELF/$f ]] || die "wrapper file $SELF/$f is missing (is scripts/bb/bin/build2 committed? .gitignore must keep !scripts/bb/bin/)" 78
done
rsync -rlptz --delete -e "ssh ${ssh_opts[*]}" "$SELF/" "$host:$RL/bb/"
ssh "${ssh_opts[@]}" "$host" "for f in remote.sh bootstrap.sh; do [ -f '$RL/bb/'\$f ] || { echo \"bb: $host:$RL/bb/\$f missing after sync\" >&2; exit 78; }; done; [ -x '$RL/bb/bin/build2' ] || { echo 'bb: $host:$RL/bb/bin/build2 missing or not executable after sync; recipes calling build2 would exit 127' >&2; exit 78; }; bash '$RL/bb/bootstrap.sh' '$lane'" </dev/null \
  || die "preflight or bootstrap failed on $host (see above)" 78
t1=$(now)

# SHOTS_DIR: run against a fresh lane-local dir, then copy it to the
# requested local path.
local_shots=""
if [[ -n ${env[SHOTS_DIR]:-} ]]; then
  local_shots=$(cd "$ROOT" && realpath -m -- "${env[SHOTS_DIR]}")
  env[SHOTS_DIR]="$RL/shots"
  ssh "${ssh_opts[@]}" "$host" "rm -rf -- '$RL/shots' && mkdir -p '$RL/shots'" </dev/null
fi

b64env=$(for k in "${!env[@]}"; do printf '%s=%s\0' "$k" "${env[$k]}"; done | base64 -w0)
b64argv=$(printf '%s\0' "$@" | base64 -w0)
say "host=$host lane=$lane jobs=${env[BB_JOBS]} sync+bootstrap=$(secs "$t0" "$t1")s; running: $*"
# No ControlMaster for the run: remote.sh stops the command when its ssh
# session ends, which only works when the session owns its connection.
set +e
ssh "${ssh_opts[@]}" -T -o ControlMaster=no -o ControlPath=none "$host" \
  "bash '$RL/bb/remote.sh' '$lane' '$b64env' '$b64argv'" </dev/null
rc=$?
set -e
t2=$(now)
((rc == 255)) && say "ssh failed or the link dropped (status 255); the remote run was stopped"

if [[ -n $local_shots ]]; then
  mkdir -p "$local_shots"
  rsync -rlt -e "ssh ${ssh_opts[*]}" "$host:$RL/shots/" "$local_shots/" || say "shots pull failed"
  say "shots: $(find "$local_shots" -maxdepth 1 -name '*.png' | wc -l) PNG(s) in $local_shots"
fi

if [[ ${BB_PULLBACK:-1} == 1 ]] && ((rc != 255)); then
  declare -a changed=() take=()
  mapfile -d '' -t changed < <(ssh "${ssh_opts[@]}" "$host" "cat '$RL/changed.list' 2>/dev/null" </dev/null)
  for p in "${changed[@]}"; do
    [[ -n $p && $p != /* && $p != *..* ]] || continue
    cur=$(cd "$ROOT" && stat --printf '%s %.9Y' -- "$p" 2>/dev/null || true)
    if [[ ${pre[$p]+x} ]]; then
      [[ $cur == "${pre[$p]}" ]] || { say "not pulling $p: changed locally during the run"; continue; }
    elif [[ -n $cur ]]; then
      say "not pulling $p: created locally during the run"; continue
    fi
    take+=("$p")
  done
  if ((${#take[@]})); then
    printf '%s\0' "${take[@]}" | rsync -rlt --checksum --from0 --files-from=- -e "ssh ${ssh_opts[*]}" "$host:$RL/src/" "$ROOT/" \
      && say "pulled back ${#take[@]} file(s) the command wrote: ${take[*]:0:8}$( ((${#take[@]} > 8)) && echo ' ...')" \
      || say "pull-back failed"
  fi
fi

say "exit=$rc sync=$(secs "$t0" "$t1")s run=$(secs "$t1" "$t2")s total=$(secs "$t0" "$(now)")s"
exit "$rc"
