import { rm, writeFile } from 'node:fs/promises';
import { rmSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { join } from 'node:path';
import { ansiHTML, LINE } from './ansi.mjs';
import { assert, bootDemo, buildCLI, chromium, registerExitCleanup, run, shotPath } from './lib.mjs';

const quote = s => `'${s.replaceAll("'", "'\\''")}'`;
// COLOURS are the TUI's colour rungs (docs/design/tui-color.md §4.1, §10.3):
// the environment each one is started with. 256 and 16 also drop TMUX, so only
// TERM decides; tmux's own report would lift a TERM=xterm run to 256.
const COLOURS = {
  truecolor: 'env -u NO_COLOR COLORTERM=truecolor TERM=xterm-256color',
  256: 'env -u NO_COLOR -u COLORTERM -u TMUX TERM=xterm-256color',
  16: 'env -u NO_COLOR -u COLORTERM -u TMUX TERM=xterm',
  none: 'env NO_COLOR=1 COLORTERM=truecolor TERM=xterm-256color',
};

// colour picks the rung (truecolor by default); bg sets VECNET_TUI_BG and, for
// light, a white page; args extend `vecnet tui`; config is appended to the
// generated config.toml (another profile for the picker, say).
export async function withTui({ scale = false, command, env: shared, beatEvery, colour = 'truecolor', bg, args = [], config: extra = '' } = {}, fn) {
  const rung = COLOURS[colour];
  if (!rung) throw new Error(`withTui: unknown colour ${colour}`);
  if (bg !== undefined && bg !== 'dark' && bg !== 'light') throw new Error(`withTui: bg must be dark or light, not ${bg}`);
  const env = shared || await bootDemo({ scale });
  const socket = `oui-${process.pid}`;
  const tmux = (...args) => run('tmux', ['-L', socket, ...args], { env: { ...process.env, TMUX: '' } });
  let browser, started = false, unregister;
  try {
    const config = join(env.dir, 'config.toml');
    // VECNET_PROFILE names the profile: a bare run under an agent harness
    // (CLAUDECODE and friends inherited through tmux) would be refused.
    await writeFile(config, `[profiles.demo]\nhub = ${JSON.stringify(env.demo.url)}\nsecret = ${JSON.stringify(env.demo.operator_secret)}\npeer_id = ${JSON.stringify(env.demo.operator_peer)}\n${extra}`, { mode: 0o600 });
    const bin = await buildCLI(env.dir);
    const argv = command || [bin, 'tui', ...args];
    // An argv-based override lets the Task 2 smoke exercise tmux before TUI
    // lands in Task 17. Real screen scripts always use the default command.
    // Colour is set by the rung, never inherited: a harness that exports
    // NO_COLOR would otherwise strip it from every shot.
    const beat = beatEvery === undefined ? '' : ` VECNET_TEST_TUI_BEAT=${quote(beatEvery)}`;
    const tuiBg = bg === undefined ? '' : ` VECNET_TUI_BG=${bg}`;
    const shell = `${rung} VECNET_PROFILE=demo VECNET_CONFIG=${quote(config)} XDG_CACHE_HOME=${quote(join(env.dir, 'cache'))} XDG_CONFIG_HOME=${quote(join(env.dir, 'config'))}${tuiBg}${beat} ${argv.map(quote).join(' ')}`;
    await tmux('-f', '/dev/null', 'new-session', '-d', '-s', 'ui', '-x', '140', '-y', '42', shell);
    started = true;
    unregister = registerExitCleanup(() => {
      spawnSync('tmux', ['-L', socket, 'kill-server'], { env: { ...process.env, TMUX: '' } });
      rmSync(join(process.env.TMUX_TMPDIR || '/tmp', `tmux-${process.getuid()}`, socket), { force: true });
    });
    await tmux('set-option', '-g', 'status', 'off');
    browser = await (await chromium()).launch({ headless: true });
    const light = bg === 'light';
    const page = await browser.newPage({ viewport: { width: 1240, height: Math.ceil(42 * LINE) + 32 }, colorScheme: light ? 'light' : 'dark' });
    page.setDefaultTimeout(60000);
    const capture = () => tmux('capture-pane', '-t', 'ui:0.0', '-e', '-p', '-N');
    const plainText = output => output.replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, '');
    const matches = (plain, text) => Array.isArray(text) ? text.every(t => matches(plain, t))
      : text instanceof RegExp ? text.test(plain) : plain.includes(text);
    const waitFor = async text => {
      const until = Date.now() + 60000;
      let output = '';
      while (Date.now() < until) {
        output = await capture();
        if (matches(plainText(output), text)) return output;
        await new Promise(resolve => setTimeout(resolve, 100));
      }
      throw new Error(`tmux text did not appear: ${String(text)}\nFinal owned fixture pane:\n${plainText(output).slice(-12000)}`);
    };
    await fn({
      demo: env.demo, advance: env.advance, restart: env.restart, stall: env.stall, clear: env.clear, controlStatus: env.controlStatus, fail: env.fail, asPeer: env.asPeer,
      keys: (...keys) => tmux('send-keys', '-t', 'ui:0.0', '--', ...keys),
      waitFor,
      // Match all required state in one frame and rasterize that very frame.
      // A toast can expire between a separate wait and the next capture.
      shot: async (name, must) => {
        const output = must ? await waitFor(must) : await capture();
        await page.setContent(ansiHTML(output, { light }), { waitUntil: 'load' });
        await page.evaluate(() => document.fonts.ready);
        const size = await page.locator('pre').boundingBox();
        assert(size, 'missing terminal raster');
        await page.setViewportSize({ width: Math.ceil(size.width), height: Math.ceil(size.height) });
        await page.screenshot({ path: await shotPath(name) });
      },
    });
  } finally {
    try { await browser?.close(); } finally {
      try { if (started) await tmux('kill-server'); } finally {
        // tmux leaves the socket file behind after kill-server; remove ours so runs do not pile up in the tmux dir.
        await rm(join(process.env.TMUX_TMPDIR || '/tmp', `tmux-${process.getuid()}`, socket), { force: true }).catch(() => {});
        try { if (!shared) await env.close(); } finally { unregister?.(); }
      }
    }
  }
}
