#!/usr/bin/env python3
"""Stage the standalone Codex connector for this user; never start it implicitly."""
import argparse
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import time


def unit_quote(value):
    # systemd quoting is not shell quoting. Disable specifier expansion as well.
    return '"' + str(value).replace('\\', '\\\\').replace('"', '\\"').replace('%', '%%') + '"'


def atomic_copy(source, target, mode):
    target.parent.mkdir(parents=True, exist_ok=True)
    if target.exists() or target.is_symlink():
        backup = target.with_name(target.name + '.before-codex-' + str(time.time_ns()))
        shutil.copy2(target, backup)
    fd, temporary = tempfile.mkstemp(dir=target.parent)
    try:
        with os.fdopen(fd, 'wb') as output:
            output.write(source.read_bytes())
            output.flush()
            os.fsync(output.fileno())
        os.chmod(temporary, mode)
        os.replace(temporary, target)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, required=True, help='build-box-produced vecnet executable')
    parser.add_argument('--node', type=Path, required=True, help='absolute Node >=22.18 executable')
    parser.add_argument('--install-cli', action='store_true', help='also replace ~/.local/bin/vecnet, saving a backup')
    args = parser.parse_args()
    root = Path(__file__).resolve().parent.parent
    home = Path.home().resolve()
    binary, node = args.binary.resolve(), args.node.resolve()
    for path in (binary, node):
        if not path.is_file() or not os.access(path, os.X_OK):
            parser.error('binary and node must be executable files')
    version = subprocess.check_output([node, '--version'], text=True).strip().lstrip('v').split('.')
    if tuple(map(int, version[:2])) < (22, 18):
        parser.error('Node >=22.18 is required')
    probe = subprocess.run([
        node, '--disable-warning=ExperimentalWarning', '--experimental-strip-types', '--input-type=module', '-e',
        "import {stripTypeScriptTypes} from 'node:module'; import {DatabaseSync} from 'node:sqlite'; "
        "stripTypeScriptTypes('const n: number = 1;'); const db = new DatabaseSync(':memory:'); db.close();",
    ], capture_output=True)
    if probe.returncode:
        parser.error('Node must include built-in TypeScript and SQLite support; this distribution build does not')
    sources = [p for package in ('launcher', 'hub-client')
               for p in sorted((root / 'connectors' / package).rglob('*'))
               if p.is_file() and not any(part in ('node_modules', 'test') for part in p.parts)]
    entry = root / 'connectors/launcher/src/codex/delivery-main.ts'
    if entry not in sources:
        parser.error('standalone delivery entry is missing')
    digest = hashlib.sha256(binary.read_bytes())
    for path in sources:
        digest.update(str(path.relative_to(root)).encode())
        digest.update(path.read_bytes())
    release = home / '.local/share/vecnet-codex' / digest.hexdigest()[:20]
    release.mkdir(parents=True, exist_ok=True, mode=0o700)
    for path in sources:
        target = release / path.relative_to(root)
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(path, target)
    shutil.copy2(binary, release / 'vecnet')
    os.chmod(release / 'vecnet', 0o700)
    env = {
        'HOME': str(home), 'VECNET_BIN': str(release / 'vecnet'),
        'PATH': str(node.parent) + ':/usr/local/bin:/usr/bin:/bin',
    }
    unit = '\n'.join([
        '[Unit]', 'Description=vecnet isolated Codex delivery', 'After=network-online.target',
        '[Service]', 'Type=simple', 'UMask=0077',
        *['Environment=' + unit_quote(key + '=' + value) for key, value in env.items()],
        'ExecStart=' + unit_quote(node) + ' --disable-warning=ExperimentalWarning --experimental-strip-types ' +
        unit_quote(release / 'connectors/launcher/src/codex/delivery-main.ts'),
        'Restart=on-failure', 'RestartSec=5', 'TimeoutStopSec=45',
        '[Install]', 'WantedBy=default.target', '',
    ])
    with tempfile.TemporaryDirectory() as temporary:
        source = Path(temporary) / 'unit'
        source.write_text(unit)
        atomic_copy(source, home / '.config/systemd/user/vecnet-codex-delivery.service', 0o600)
    if args.install_cli:
        atomic_copy(binary, home / '.local/bin/vecnet', 0o755)
    print('Installed', release)
    print('Bindings, hooks and recovery state preserved. Review/stop legacy delivery services before activation.')
    print('Activate: systemctl --user daemon-reload && systemctl --user enable --now vecnet-codex-delivery.service')
    print('Rollback: stop this service; restore the .before-codex-* unit/CLI backups and the previous service.')


if __name__ == '__main__':
    main()
