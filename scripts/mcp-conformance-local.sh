#!/usr/bin/env bash
# Build and run on an isolated build-box lane. Never uses real config.
set -euo pipefail
just build
proof_dir=$(mktemp -d "${TMPDIR:-/tmp}/dot-events-conformance.XXXXXX")
hub_pid=
cleanup() {
  if [[ -n $hub_pid ]]; then
    kill "$hub_pid" 2>/dev/null || true
    wait "$hub_pid" 2>/dev/null || true
  fi
  rm -f -- "$proof_dir/init.txt"
}
trap cleanup EXIT
bin/vecnet-hub init --data-dir "$proof_dir/data" --operator-name conformance > "$proof_dir/init.txt"
export VECNET_MCP_TOKEN
VECNET_MCP_TOKEN=$(awk '/^operator secret:/ {print $3}' "$proof_dir/init.txt")
test -n "$VECNET_MCP_TOKEN"
bin/vecnet-hub --listen 127.0.0.1:0 --data-dir "$proof_dir/data" > "$proof_dir/hub.log" 2>&1 &
hub_pid=$!
origin=
for ((attempt=0; attempt<100; attempt++)); do
  addr=$(sed -n 's/.*addr=\(127\.0\.0\.1:[0-9]*\).*/\1/p' "$proof_dir/hub.log" | head -1)
  if [[ -n $addr ]]; then origin="http://$addr"; break; fi
  kill -0 "$hub_pid" 2>/dev/null || { echo 'FAIL temporary hub startup'; exit 1; }
  sleep 0.1
done
test -n "$origin"
node scripts/mcp-conformance.mjs "$origin"
node --input-type=module - "$origin" <<'JS'
const response = await fetch(process.argv[2] + '/mcp', {
  method: 'POST',
  headers: {
    Authorization: `Bearer ${process.env.VECNET_MCP_TOKEN}`,
    'Content-Type': 'application/json', Accept: 'application/json, text/event-stream',
    'MCP-Protocol-Version': '2026-07-28',
  },
  body: JSON.stringify({jsonrpc: '2.0', id: 1, method: 'tools/call', params: {name: 'vecnet_subscriptions'}}),
});
const message = await response.json();
if (response.status !== 200 || message.error || message.result?.isError) throw new Error('status smoke failed');
console.log('SAMPLE_STATUS_OUTPUT=' + JSON.stringify(message.result.structuredContent));
JS
