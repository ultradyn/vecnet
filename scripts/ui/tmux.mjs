import { writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { ansiHTML } from './ansi.mjs';
import { assert, bootDemo, buildCLI, chromium, run, shotPath } from './lib.mjs';

const quote = s => `'${s.replaceAll("'", "'\\''")}'`;
export async function withTui({ scale = false, command } = {}, fn) {
  const env = await bootDemo({ scale });
  const socket = `oui-${process.pid}`;
  const tmux = (...args) => run('tmux', ['-L', socket, ...args], { env: { ...process.env, TMUX: '' } });
  let browser, started = false;
  try {
    const config = join(env.dir, 'config.toml');
    await writeFile(config, `default = "demo"\n[profiles.demo]\nhub = ${JSON.stringify(env.demo.url)}\nsecret = ${JSON.stringify(env.demo.operator_secret)}\npeer_id = ${JSON.stringify(env.demo.operator_peer)}\n`, { mode: 0o600 });
    const bin = await buildCLI(env.dir);
    const argv = command || [bin, 'tui'];
    // An argv-based override lets the Task 2 smoke exercise tmux before TUI
    // lands in Task 17. Real screen scripts always use the default command.
    const shell = `env VECNET_CONFIG=${quote(config)} XDG_CACHE_HOME=${quote(join(env.dir, 'cache'))} XDG_CONFIG_HOME=${quote(join(env.dir, 'config'))} TERM=xterm-256color ${argv.map(quote).join(' ')}`;
    await tmux('-f', '/dev/null', 'new-session', '-d', '-s', 'ui', '-x', '140', '-y', '42', shell);
    started = true;
    await tmux('set-option', '-g', 'status', 'off');
    browser = await (await chromium()).launch({ headless: true });
    const page = await browser.newPage({ viewport: { width: 1240, height: 872 }, colorScheme: 'dark' });
    page.setDefaultTimeout(60000);
    const capture = () => tmux('capture-pane', '-t', 'ui:0.0', '-e', '-p', '-N');
    await fn({
      demo: env.demo, advance: env.advance, stall: env.stall, fail: env.fail, asPeer: env.asPeer,
      keys: (...keys) => tmux('send-keys', '-t', 'ui:0.0', '--', ...keys),
      waitFor: async text => {
        const until = Date.now() + 60000;
        while (Date.now() < until) {
          const output = await capture();
          const plain = output.replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, '');
          if (plain.includes(text)) return;
          await new Promise(resolve => setTimeout(resolve, 100));
        }
        throw new Error(`tmux text did not appear (length ${text.length})`);
      },
      shot: async name => {
        await page.setContent(ansiHTML(await capture()), { waitUntil: 'load' });
        await page.evaluate(() => document.fonts.ready);
        const size = await page.locator('pre').boundingBox();
        assert(size, 'missing terminal raster');
        await page.setViewportSize({ width: Math.ceil(size.width), height: Math.ceil(size.height) });
        await page.screenshot({ path: await shotPath(name) });
      },
    });
  } finally {
    try { await browser?.close(); } finally {
      try { if (started) await tmux('kill-server'); } finally { await env.close(); }
    }
  }
}
