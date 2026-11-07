"""CLI dependency absence gate; run only through the build-box wrapper."""
import json
import os
from pathlib import Path
import subprocess

assert os.environ.get('VECNET_BB') == '1', 'build box required'
result = subprocess.run(['go', 'list', '-deps', './cmd/vecnet'], check=True, text=True, capture_output=True)
deps = set(result.stdout.splitlines())
for suffix in ('modernc.org/sqlite', 'github.com/XertroV/vecnet/internal/store', 'github.com/XertroV/vecnet/internal/eventpayload'):
    assert suffix not in deps, suffix
out = {'command': 'go list -deps ./cmd/vecnet', 'host': os.uname().nodename,
       'dependency_count': len(deps), 'absent': ['modernc.org/sqlite', 'internal/store', 'internal/eventpayload']}
Path('bench/results/2026-11-07-compaction-cli-deps.json').write_text(json.dumps(out, indent=2)+'\n')
print(json.dumps(out, sort_keys=True))
