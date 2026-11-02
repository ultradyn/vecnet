// Run after every screen script: validate the actual output against the index.
import assert from 'node:assert/strict';
import { readFile, readdir } from 'node:fs/promises';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

export async function checkShotsIndex({
  directory = resolve(process.env.SHOTS_DIR || fileURLToPath(new URL('../../docs/plans/shots/oui/', import.meta.url))),
  index = new URL('../../docs/plans/shots/oui/README.md', import.meta.url),
} = {}) {
  const markdown = await readFile(index, 'utf8');
  const pngs = (await readdir(directory)).filter(name => name.endsWith('.png')).sort();
  const listed = [...markdown.matchAll(/\]\(([^)]+\.png)\)/g)].map(match => match[1]).sort();
  assert.equal(new Set(listed).size, listed.length, 'screenshot index lists a PNG more than once');
  const extra = pngs.filter(name => !listed.includes(name));
  const missing = listed.filter(name => !pngs.includes(name));
  assert.deepEqual(extra, [], `screenshots missing from README in ${directory}: ${extra.join(', ')}`);
  assert.deepEqual(missing, [], `indexed screenshots missing from ${directory}: ${missing.join(', ')}`);
  return pngs.length;
}

if (process.argv[1] && fileURLToPath(import.meta.url) === resolve(process.argv[1])) {
  const count = await checkShotsIndex();
  console.log(`ok - screenshot index lists all ${count} generated PNGs exactly once`);
}
