#!/usr/bin/env bash
# Runs ON the build box. Installs the pinned user-level toolchain under
# ~/bb-src/vecnet4/.toolchain once; later calls only check stamps. No sudo,
# no system packages, no dotfile edits. Usage: bootstrap.sh <lane>
set -euo pipefail
umask 022

BASE="$HOME/bb-src/vecnet4"
TC="$BASE/.toolchain"
lane=${1:?lane}
SRC="$BASE/$lane/bb"

GO_VER=1.27.1
GO_SHA=63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445
NODE_VER=22.23.3
NODE_SHA=df450af89261115ef9f9e3830c3eeb2cc9213b63c720b1af623cb5dcbe2e02de
UV_VER=0.9.29
UV_SHA=1ce5212f8f42dc7427a1bd3db4168d6d1abcf81b38d8c82a5b9d0ddc54ceebfc
PW_VER=1.61.1
INTER_VER=4.1
INTER_SHA=9883fdd4a49d4fb66bd8177ba6625ef9a64aa45899767dde3d36aa425756b11e
JBM_VER=2.304
JBM_SHA=6f6376c6ed2960ea8a963cd7387ec9d76e3f629125bc33d1fdcd7eb7012f7bbf
# Matches xsm's `pip freeze` for the validate-protocol example check.
PY_PINS=(jsonschema==4.26.0 jsonschema-specifications==2025.9.1 referencing==0.37.0 rpds-py==2026.9.1 attrs==26.1.0 typing_extensions==4.16.0)

mkdir -p "$TC/dl" "$BASE/.cache"
exec 8>"$TC/.bootstrap.lock"
flock -w 900 8 || { echo "bb: another lane is bootstrapping the toolchain; retry" >&2; exit 75; }

for tool in curl tar xz sha256sum tmux; do
  command -v "$tool" >/dev/null || { echo "bb: box lacks required tool: $tool (install is out of scope; pick another BB_HOST)" >&2; exit 69; }
done
command -v just >/dev/null || echo "bb: warning: box lacks just; recipes will not run" >&2
command -v gcc >/dev/null || echo "bb: warning: box lacks gcc; -race (cgo) runs will fail" >&2

# fetch <url> <sha256> <dest>: download to a temp name, verify, then rename.
fetch() {
  local url=$1 sha=$2 dest=$3
  [ -f "$dest" ] && echo "$sha  $dest" | sha256sum -c --status && return 0
  curl -fsSL --retry 3 -o "$dest.part" "$url"
  echo "$sha  $dest.part" | sha256sum -c --status || { echo "bb: checksum mismatch for $url" >&2; rm -f -- "$dest.part"; exit 70; }
  mv -- "$dest.part" "$dest"
}

# unpack <archive> <final-dir>: extract into a staging dir, then rename.
unpack() {
  local archive=$1 final=$2
  rm -rf -- "$final.part"
  mkdir -p "$final.part"
  tar -xf "$archive" -C "$final.part" --strip-components=1
  mv -- "$final.part" "$final"
}

if [ ! -x "$TC/go-$GO_VER/bin/go" ]; then
  echo "bb: installing go $GO_VER" >&2
  fetch "https://dl.google.com/go/go$GO_VER.linux-amd64.tar.gz" "$GO_SHA" "$TC/dl/go$GO_VER.tgz"
  unpack "$TC/dl/go$GO_VER.tgz" "$TC/go-$GO_VER"
fi
ln -sfn "go-$GO_VER" "$TC/go"

if [ ! -x "$TC/node-v$NODE_VER/bin/node" ]; then
  echo "bb: installing node $NODE_VER" >&2
  fetch "https://nodejs.org/dist/v$NODE_VER/node-v$NODE_VER-linux-x64.tar.xz" "$NODE_SHA" "$TC/dl/node-v$NODE_VER.tar.xz"
  unpack "$TC/dl/node-v$NODE_VER.tar.xz" "$TC/node-v$NODE_VER"
fi
ln -sfn "node-v$NODE_VER" "$TC/node"

if [ ! -x "$TC/uv-$UV_VER/uv" ]; then
  echo "bb: installing uv $UV_VER" >&2
  fetch "https://github.com/astral-sh/uv/releases/download/$UV_VER/uv-x86_64-unknown-linux-gnu.tar.gz" "$UV_SHA" "$TC/dl/uv-$UV_VER.tgz"
  unpack "$TC/dl/uv-$UV_VER.tgz" "$TC/uv-$UV_VER"
fi

py_stamp="$TC/py/.pins-$(printf '%s\n' "${PY_PINS[@]}" | sha256sum | cut -c1-12)"
if command -v python3 >/dev/null && [ ! -f "$py_stamp" ]; then
  echo "bb: building python venv (jsonschema for validate-protocol)" >&2
  rm -rf -- "$TC/py"
  UV_CACHE_DIR="$BASE/.cache/uv" "$TC/uv-$UV_VER/uv" venv --quiet --python "$(command -v python3)" "$TC/py"
  UV_CACHE_DIR="$BASE/.cache/uv" "$TC/uv-$UV_VER/uv" pip install --quiet --python "$TC/py/bin/python" "${PY_PINS[@]}"
  touch "$py_stamp"
fi

# The UI's --sans/--mono stacks name Inter and JetBrains Mono; these are the
# same bytes as xsm's inter-font 4.1 / ttf-jetbrains-mono 2.304 packages, so
# box-rendered shots match xsm's typography. remote.sh links them into the
# lane HOME, where fontconfig looks ($XDG_DATA_HOME/fonts).
if [ ! -f "$TC/fonts/.ok" ] && command -v python3 >/dev/null; then
  echo "bb: installing Inter $INTER_VER and JetBrains Mono $JBM_VER" >&2
  fetch "https://github.com/rsms/inter/releases/download/v$INTER_VER/Inter-$INTER_VER.zip" "$INTER_SHA" "$TC/dl/inter-$INTER_VER.zip"
  fetch "https://github.com/JetBrains/JetBrainsMono/releases/download/v$JBM_VER/JetBrainsMono-$JBM_VER.zip" "$JBM_SHA" "$TC/dl/jbm-$JBM_VER.zip"
  rm -rf -- "$TC/fonts.part"
  mkdir -p "$TC/fonts.part"
  python3 -I - "$TC/dl/inter-$INTER_VER.zip" "$TC/dl/jbm-$JBM_VER.zip" "$TC/fonts.part" <<'PY'
import sys, zipfile, os
inter, jbm, out = sys.argv[1:]
with zipfile.ZipFile(inter) as z:
    open(os.path.join(out, "Inter.ttc"), "wb").write(z.read("Inter.ttc"))
with zipfile.ZipFile(jbm) as z:
    for n in z.namelist():
        if n.startswith("fonts/ttf/JetBrainsMono-") and n.endswith(".ttf"):
            open(os.path.join(out, os.path.basename(n)), "wb").write(z.read(n))
PY
  touch "$TC/fonts.part/.ok"
  rm -rf -- "$TC/fonts"
  mv -- "$TC/fonts.part" "$TC/fonts"
fi

pw="$TC/playwright-$PW_VER"
if [ ! -f "$pw/.ok" ]; then
  echo "bb: installing playwright $PW_VER and chromium headless shell" >&2
  rm -rf -- "$pw"
  mkdir -p "$pw"
  cp -- "$SRC/playwright/package.json" "$SRC/playwright/package-lock.json" "$pw/"
  (
    cd "$pw"
    export PATH="$TC/node/bin:/usr/local/bin:/usr/bin:/bin" npm_config_cache="$BASE/.cache/npm"
    npm ci --ignore-scripts --no-audit --no-fund --loglevel=error
    PLAYWRIGHT_BROWSERS_PATH="$TC/ms-playwright" node node_modules/playwright/cli.js install --only-shell chromium
  )
  touch "$pw/.ok"
fi
