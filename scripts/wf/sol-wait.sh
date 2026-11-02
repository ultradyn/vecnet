#!/usr/bin/env bash
# Wait for a run started by sol-run.sh, for at most <limit> seconds (default 540).
# usage: sol-wait.sh <base> [limit]
# Prints one of:
#   RUNNING                  still going; call again
#   DONE <exit> RESULT_OK    <base>.result.json parsed, and its head_sha, if any, is the real HEAD
#   DONE <exit> RESULT_MISSING | RESULT_INVALID | RESULT_HEAD_MISMATCH <detail>
set -u
base=$1
limit=${2:-540}
SECONDS=0
while [ ! -f "$base.exit" ]; do
	if [ "$SECONDS" -ge "$limit" ]; then
		echo RUNNING
		exit 0
	fi
	sleep 5
done
code=$(cat "$base.exit")
verdict=$(python3 - "$base" <<'PY'
import json, subprocess, sys
base = sys.argv[1]
try:
    with open(base + '.result.json') as f:
        r = json.load(f)
except FileNotFoundError:
    print('RESULT_MISSING')
    sys.exit()
except ValueError as e:
    print('RESULT_INVALID', e)
    sys.exit()
if not isinstance(r, dict):
    print('RESULT_INVALID not an object')
    sys.exit()
sha = r.get('head_sha')
if sha:
    cwd = open(base + '.cwd').read().strip()
    head = subprocess.run(['git', '-C', cwd, 'rev-parse', 'HEAD'], capture_output=True, text=True).stdout.strip()
    if not head.startswith(sha) and not sha.startswith(head):
        print('RESULT_HEAD_MISMATCH', 'reported', sha, 'actual', head)
        sys.exit()
print('RESULT_OK')
PY
)
echo "DONE $code $verdict"
