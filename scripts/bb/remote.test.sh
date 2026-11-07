#!/usr/bin/env bash
set -euo pipefail
wrapper=$(realpath "$(dirname "$0")/remote.sh")
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/physical/vecnet4/probe/src" "$fixture/home"
ln -s "$fixture/physical" "$fixture/home/bb-src"
cat >"$fixture/physical/vecnet4/probe/src/probe.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
[[ $HOME == "$1/probe/home" ]] || { echo 'FAIL: HOME retains symlinked build base'; exit 1; }
for name in TMPDIR XDG_CONFIG_HOME XDG_CACHE_HOME XDG_DATA_HOME GOCACHE GOMODCACHE GOPATH npm_config_cache; do
    [[ ${!name} == "$1/"* ]] || { echo "FAIL: $name is not physical"; exit 1; }
done
[[ $TMPDIR == "$1/probe/tmp" && $GOFLAGS == '-p=2' && $2 == 'framed argument' ]]
echo 'PASS: symlinked base yields physical private paths and preserved arguments'
SH
args=$(printf '%s\0' bash probe.sh "$fixture/physical/vecnet4" 'framed argument' | base64 -w0)
forwarded=$(printf '%s\0' BB_JOBS=2 | base64 -w0)
HOME="$fixture/home" bash "$wrapper" probe "$forwarded" "$args"
