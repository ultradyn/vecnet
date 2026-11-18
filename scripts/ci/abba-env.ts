// Disposable native diagnostic; never a release setting.
import { appendFileSync, mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { makePrivateDirectory } from '../../connectors/launcher/test/fixtures/platform.ts';
const probeRoot = mkdtempSync(join(tmpdir(), 'ci80-abba-'));
for (const dir of ['home', 'config', 'data', 'cache', 'state', 'local', 'tmp', 'bin']) makePrivateDirectory(join(probeRoot, dir));
makePrivateDirectory(probeRoot);
const entries = { HOME: join(probeRoot, 'home'), USERPROFILE: join(probeRoot, 'home'), APPDATA: join(probeRoot, 'config'), LOCALAPPDATA: join(probeRoot, 'local'), XDG_CONFIG_HOME: join(probeRoot, 'config'), XDG_DATA_HOME: join(probeRoot, 'data'), XDG_CACHE_HOME: join(probeRoot, 'cache'), XDG_STATE_HOME: join(probeRoot, 'state'), TMP: join(probeRoot, 'tmp'), TEMP: join(probeRoot, 'tmp'), TMPDIR: join(probeRoot, 'tmp'), TMUX_TMPDIR: join(probeRoot, 'tmp'), CI80_ABBA_ROOT: probeRoot, ACP_TEST_ROOT: '' };
appendFileSync(process.env.GITHUB_ENV!, Object.entries(entries).map(([k,v])=>`${k}=${v}\n`).join(''));
console.log('ABBA_ENV disposable private paths initialized');
