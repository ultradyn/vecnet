#!/usr/bin/env bash
# Backstop for interrupted screen runs. All matches below have literal roots
# and strict suffixes; no caller-controlled path is interpolated into a regex.
set -u
reaped=0 servers=0 removed=0
trap 'printf "reaped %d demo(s), %d tmux server(s), %d dir(s)\n" "$reaped" "$servers" "$removed"' EXIT
uid=$(id -u) || exit 0
min_age=${REAP_MIN_AGE:-7200}
case "$min_age" in ''|*[!0-9]*) exit 0 ;; esac
declare -a roots=() candidates=()
declare -A used=() old=()
if [[ ${REAP_ROOTS+x} ]]; then
    IFS=: read -r -a candidates <<< "$REAP_ROOTS"
else
    [[ -z ${TMPDIR:-} ]] || candidates+=("$TMPDIR")
    candidates+=(/tmp "/tmp/build2-$(id -un)")
fi
for root in "${candidates[@]}"; do
    root=${root%/}
    [[ -n $root ]] || continue
    duplicate=false
    for seen in "${roots[@]}"; do [[ $seen != "$root" ]] || duplicate=true; done
    $duplicate || roots+=("$root")
done

while read -r pid age args; do
    [[ $pid =~ ^[0-9]+$ && $age =~ ^[0-9]+$ ]] || continue
    read -r -a argv <<< "$args"
    first=${argv[0]:-}
    for root in "${roots[@]}"; do
        case "$first" in "$root"/vecnet-ui-build-*/demo) ;; *) continue ;; esac
        suffix=${first#"$root"/vecnet-ui-build-}
        suffix=${suffix%/demo}
        case "$suffix" in ''|*[!A-Za-z0-9]*) continue ;; esac
        used[${first%/demo}]=1
        for ((i=1; i<${#argv[@]}; i++)); do
            case "${argv[i]}" in
                --data-dir) if ((i+1 < ${#argv[@]})); then used[${argv[i+1]}]=1; fi ;;
                --data-dir=*) data=${argv[i]#--data-dir=}; [[ -z $data ]] || used[$data]=1 ;;
            esac
        done
        if ((age >= min_age)); then old[$pid]=$first; fi
        break
    done
done < <(ps -u "$uid" -o pid=,etimes=,args= 2>/dev/null)

# Recheck uid and command before each signal so a recycled pid cannot cause
# us to signal an unrelated process during the grace period.
same_demo() {
    local pid=$1 expected=$2 owner args
    owner=$(ps -p "$pid" -o uid= 2>/dev/null) || return 1
    [[ ${owner// /} == "$uid" ]] || return 1
    args=$(ps -p "$pid" -o args= 2>/dev/null) || return 1
    [[ ${args%% *} == "$expected" ]]
}
for pid in "${!old[@]}"; do
    if same_demo "$pid" "${old[$pid]}" && kill -TERM -- "$pid" 2>/dev/null; then
        reaped=$((reaped+1))
    else
        unset 'old[$pid]'
    fi
done
if ((${#old[@]})); then
    # Poll readiness, ending early when all signalled demos have exited.
    for ((attempt=0; attempt<30; attempt++)); do
        pending=false
        for pid in "${!old[@]}"; do same_demo "$pid" "${old[$pid]}" && pending=true; done
        $pending || break
        sleep 0.1
    done
    for pid in "${!old[@]}"; do
        if same_demo "$pid" "${old[$pid]}"; then kill -KILL -- "$pid" 2>/dev/null || :; fi
    done
fi

shopt -s nullglob
for socket in "${TMUX_TMPDIR:-/tmp}/tmux-$uid"/oui-*; do
    [[ -S $socket ]] || continue
    name=${socket##*/}
    owner=${name#oui-}
    case "$owner" in ''|*[!0-9]*) continue ;; esac
    [[ $owner != 0 && $owner != 0* ]] || continue
    # ps also sees a live pid belonging to another uid, where kill -0 would
    # fail with EPERM and incorrectly call its socket abandoned.
    if ! ps -p "$owner" -o pid= >/dev/null 2>&1; then
        tmux -L "$name" kill-server >/dev/null 2>&1 || :
        if rm -f -- "$socket" 2>/dev/null; then servers=$((servers+1)); fi
    fi
done

now=$(date +%s) || exit 0
for root in "${roots[@]}"; do
    for dir in "$root"/vecnet-ui-build-* "$root"/vecnet-ui-data-*; do
        [[ -d $dir && ! -L $dir ]] || continue
        name=${dir##*/}
        case "$name" in
            vecnet-ui-build-*) suffix=${name#vecnet-ui-build-} ;;
            vecnet-ui-data-*) suffix=${name#vecnet-ui-data-} ;;
            *) continue ;;
        esac
        case "$suffix" in ''|*[!A-Za-z0-9]*) continue ;; esac
        [[ ! ${used[$dir]+yes} ]] || continue
        mtime=$(stat -c %Y -- "$dir" 2>/dev/null) || continue
        ((now-mtime >= min_age)) || continue
        if rm -rf -- "$dir" 2>/dev/null; then removed=$((removed+1)); fi
    done
done
exit 0
