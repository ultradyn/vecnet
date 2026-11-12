import importlib.util
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).with_name('install-codex-delivery.py')
spec = importlib.util.spec_from_file_location('installer', SCRIPT)
installer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(installer)


class Installation(unittest.TestCase):
    def test_systemd_escaping(self):
        self.assertEqual(installer.unit_quote('/home/a%u/"x"'), '"/home/a%%u/\\"x\\""')

    def test_refuses_distribution_node_without_typescript_before_installing(self):
        with tempfile.TemporaryDirectory() as temporary:
            home = Path(temporary)
            node = home / 'node'
            node.write_text('#!/bin/sh\nif [ "$1" = "--version" ]; then echo v22.22.1; else exit 1; fi\n')
            node.chmod(0o700)
            result = subprocess.run([
                'python3', str(SCRIPT), '--binary', str(node), '--node', str(node),
            ], env={**os.environ, 'HOME': str(home)}, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('TypeScript and SQLite support', result.stderr)
            self.assertFalse((home / '.local').exists())

    def test_staging_preserves_state_and_saves_previous_cli(self):
        with tempfile.TemporaryDirectory(prefix='vecnet install % ') as temporary:
            home = Path(temporary)
            binary = home / 'new-vecnet'
            binary.write_text('#!/bin/sh\nexit 0\n')
            binary.chmod(0o700)
            installed = home / '.local/bin/vecnet'
            installed.parent.mkdir(parents=True)
            installed.write_text('previous binary')
            authority = home / '.local/state/vecnet/codex/bindings.json'
            authority.parent.mkdir(parents=True)
            authority.write_text('preserve even corrupt state')
            result = subprocess.run([
                'python3', str(SCRIPT), '--binary', str(binary),
                '--node', shutil.which('node'), '--install-cli',
            ], env={**os.environ, 'HOME': str(home)}, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(authority.read_text(), 'preserve even corrupt state')
            self.assertEqual(installed.read_bytes(), binary.read_bytes())
            backups = list(installed.parent.glob('vecnet.before-codex-*'))
            self.assertEqual(len(backups), 1)
            self.assertEqual(backups[0].read_text(), 'previous binary')
            unit = home / '.config/systemd/user/vecnet-codex-delivery.service'
            self.assertIn('%%', unit.read_text())
            self.assertEqual(unit.stat().st_mode & 0o777, 0o600)
            self.assertFalse((unit.parent / 'default.target.wants').exists())
            self.assertEqual(len(list((home / '.local/share/vecnet-codex').glob('*/connectors/launcher/src/codex/delivery-main.ts'))), 1)


if __name__ == '__main__':
    unittest.main()
