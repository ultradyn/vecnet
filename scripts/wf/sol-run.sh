#!/usr/bin/env bash
# Start one Codex run for a workflow relay agent and return at once.
# usage: sol-run.sh <ccc-preset> <cwd> <base> <transcript-glob>
# The assignment comes from the relay's transcript (sol-extract.py) into <base>.md.
# The run writes <base>.log, then <base>.exit; the assignment tells Codex to write
# <base>.result.json. sol-wait.sh waits for it.
set -u
preset=$1
cwd=$2
base=$3
transcripts=$4
rm -f "$base.started"
python3 "$(dirname "$0")/sol-extract.py" "$base" "$transcripts" || exit 3
cd "$cwd" || { echo "no such directory: $cwd"; exit 2; }
rm -f "$base.exit" "$base.result.json"
printf '%s\n' "$cwd" > "$base.cwd"
touch "$base.started"
prompt="Read your full assignment in $base.md and carry it out exactly. Your final output is the file it names, not your reply."
setsid nohup bash -c 'ccc "@$0" --permission-mode yolo -- "$1" > "$2.log" 2>&1; echo $? > "$2.exit"' \
	"$preset" "$prompt" "$base" < /dev/null > /dev/null 2>&1 &
echo "started $preset in $cwd"
