import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import test from 'node:test';

test('installer refreshes active delivery generations', t => {
  const fixture = mkdtempSync(join(tmpdir(), 'delivery-install-'));
  t.after(() => rmSync(fixture, { recursive: true, force: true }));
  const home = join(fixture, 'home'), bin = join(fixture, 'bin');
  for (const dir of [home, bin, join(fixture, 'connectors/launcher'), join(fixture, 'connectors/hub-client')]) mkdirSync(dir, { recursive: true, mode: 0o700 });
  cpSync('connectors/launcher/systemd', join(fixture, 'connectors/launcher/systemd'), { recursive: true });
  mkdirSync(join(fixture, 'connectors/launcher/src/codex'), { recursive: true });
  writeFileSync(join(fixture, 'connectors/launcher/src/codex/delivery-main.ts'), '// fixture entry\n');
  mkdirSync(join(fixture, 'connectors/launcher/node_modules'), { recursive: true });
  writeFileSync(join(fixture, 'connectors/launcher/node_modules/unused'), 'excluded');
  writeFileSync(join(fixture, 'connectors/launcher/auth.json'), 'fixture secret-shaped file');
  mkdirSync(join(home, '.local/share/vecnet-launcher'), { recursive: true, mode: 0o755 });
  const active = 'a'.repeat(64), inactive = 'b'.repeat(64);
  for (const slug of [active, inactive]) {
    const dir = join(home, '.local/share/vecnet/codex-delivery', slug);
    mkdirSync(dir, { recursive: true, mode: 0o700 });
    writeFileSync(join(dir, 'descriptor.json'), JSON.stringify({ version: 1, hub: 'https://hub.invalid' }), { mode: 0o600 });
  }
  const writeBin = (name: string, body: string) => writeFileSync(join(bin, name), '#!/bin/sh\n' + body, { mode: 0o700 });
  writeBin('git', 'echo fixture-sha\n');
  writeBin('systemctl', 'echo "$*" >>"$HOME/manager-calls"\n[ "$*" = "--user daemon-reload" ]\n');
  const native = join(bin, 'private-vecnet');
  writeBin('private-vecnet', `echo "$*" >>"$HOME/native-calls"\ncase "$2" in\n delivery-status) if [ "$4" = '${active}' ]; then echo '{"running":true}'; else echo '{"running":false}'; fi ;;\n delivery-refresh) exit 0 ;;\n *) exit 1 ;;\nesac\n`);
  const result = spawnSync('just', ['--justfile', resolve('justfile'), '--working-directory', fixture, 'ln-install'], {
    env: { ...process.env, HOME: home, XDG_CONFIG_HOME: join(fixture, 'config'), CODEX_HOME: join(fixture, 'codex'),
      XDG_DATA_HOME: join(home, '.local/share'), XDG_RUNTIME_DIR: join(fixture, 'runtime'), PATH: bin + ':' + process.env.PATH, VECNET_BIN: native },
    encoding: 'utf8', timeout: 15_000,
  });
  assert.equal(result.status, 0, result.stderr);
  const unit = join(home, '.config/systemd/user/vecnet-delivery@.service');
  assert.ok(existsSync(unit), 'installer must install the delivery template');
  const text = readFileSync(unit, 'utf8');
  assert.ok(text.includes(`ExecStart=${native} codex deliver --hub-id %i`));
  for (const line of ['Environment="HOME=%h"', 'Environment="XDG_DATA_HOME=%h/.local/share"', 'Environment="XDG_RUNTIME_DIR=/run/user/%U"', 'UMask=0077', 'Restart=on-failure', 'StartLimitIntervalSec=0']) assert.ok(text.includes(line), line);
  const destination = join(home, '.local/share/vecnet-launcher/fixture-sha');
  const entry = join(destination, 'launcher/src/codex/delivery-main.ts');
  assert.ok(text.includes(`VECNET_CODEX_DELIVERY_ENTRY=${entry}`));
  assert.equal(realpathSync(join(home, '.local/share/vecnet-launcher/current')), destination);
  assert.equal(existsSync(join(destination, 'launcher/node_modules')), false);
  assert.equal(existsSync(join(destination, 'launcher/auth.json')), false);
  const manifestPath = join(home, '.local/share/vecnet-launcher/delivery.json');
  const manifest = JSON.parse(readFileSync(manifestPath, 'utf8'));
  assert.equal(statSync(manifestPath).mode & 0o777, 0o600);
  assert.equal(statSync(dirname(manifestPath)).mode & 0o777, 0o700, 'existing installation root becomes private');
  assert.deepEqual(manifest, { version: 1, binary: native, entry, node: realpathSync(process.execPath), path: `${dirname(realpathSync(process.execPath))}:/usr/local/bin:/usr/bin:/bin` });
  assert.equal(readFileSync(join(home, 'manager-calls'), 'utf8'), '--user daemon-reload\n');
  const calls = readFileSync(join(home, 'native-calls'), 'utf8').trim().split('\n').sort();
  assert.deepEqual(calls, [`codex delivery-refresh --hub-id ${active}`, `codex delivery-status --hub-id ${active}`, `codex delivery-status --hub-id ${inactive}`].sort());
});
