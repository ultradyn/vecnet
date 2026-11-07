// PROTOTYPE screenshot runner: node .scratch/vecnet/prefs-proto/shots.mjs [list,policy] [states]
// Uses the repo's Playwright resolution (scripts/ui/lib.mjs chromium()). SIZES=desk,phone
import { chromium } from '../../../scripts/ui/lib.mjs';
import { mkdir } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
const BASE = process.env.BASE || 'http://xsm:10435/vecnet-prefs-proto/';
const out = join(dirname(fileURLToPath(import.meta.url)), 'shots');
await mkdir(out, { recursive: true });
const vs = (process.argv[2] || 'list,policy').split(',');
const sts = (process.argv[3] || 'initial,dirty,saving,saved,error,confirm-archive,confirm-trim,confirm-files').split(',');
const sizes = (process.env.SIZES || 'desk,phone').split(',');
const VP = { desk: { width: 1440, height: 900 }, phone: { width: 390, height: 844 } };
// where to look for each state: the first changed / errored row, or the save bar
const FOCUS = { list: { dirty: '[data-row=expiry]', saving: '[data-row=expiry]', saved: '[data-row=expiry]' }, policy: {} };
const browser = await (await chromium()).launch();
const problems = [];
try {
  for (const size of sizes) {
    const ctx = await browser.newContext({ viewport: VP[size], deviceScaleFactor: 1, colorScheme: 'dark' });
    const page = await ctx.newPage();
    page.on('pageerror', e => problems.push(`pageerror ${e.message}`));
    page.on('console', m => { if (m.type() === 'error') problems.push(`console ${m.text()}`); });
    for (const v of vs) for (const st of sts) {
      await page.goto(`${BASE}?view=${v}&state=${st}`, { waitUntil: 'networkidle' });
      await page.waitForTimeout(450);
      const sel = process.env.SCROLL || FOCUS[v][st];
      if (st === 'error') await page.click('[data-act=jump]');
      else if (sel) await page.evaluate(sel => { const e = [...document.querySelectorAll(sel)].find(n => !n.hidden); e?.scrollIntoView({ block: 'center' }); }, sel);
      await page.waitForTimeout(150);
      const ov = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth);
      if (ov > 0) problems.push(`${v} ${st} ${size}: horizontal overflow ${ov}px`);
      const name = `${v}-${st}-${size}${process.env.TAG ? '-' + process.env.TAG : ''}.png`;
      await page.screenshot({ path: join(out, name) });
      console.log(name);
    }
    await ctx.close();
  }
} finally { await browser.close(); }
if (problems.length) console.log('PROBLEMS\n' + problems.join('\n'));
