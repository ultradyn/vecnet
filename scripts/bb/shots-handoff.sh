#!/usr/bin/env bash
# Copy a completed remote generation to a new local destination. No merging,
# no deletion of caller data, and no completed destination on transfer failure.
set -euo pipefail
destination=$1
source=$2
ssh_command=$3
[[ ! -e $destination && ! -L $destination ]] || { printf 'bb: capture destination already exists\n' >&2; exit 73; }
mkdir -p -- "$(dirname "$destination")"
staging=$(mktemp -d -- "$(dirname "$destination")/.shots-transfer.XXXXXXXX")
# Failed staging is retained for inspection, never promoted or advertised.
rsync -rlt -e "$ssh_command" "$source" "$staging/"
mv -T --no-clobber -- "$staging" "$destination"
[[ ! -d $staging ]] || { printf 'bb: capture destination appeared during transfer\n' >&2; exit 73; }
