// Node 22 treats the directory in `node --test internal/ui/jstest/` as a
// module path, not a folder to search, so `just test-ui` and the plan's
// command land here. This loads every *.test.mjs beside it, so a new test
// file needs no registration.
import { readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const here = new URL('./', import.meta.url);
const files = readdirSync(fileURLToPath(here)).filter(name => name.endsWith('.test.mjs')).sort();
if (!files.length) throw new Error('no *.test.mjs files in internal/ui/jstest');
for (const name of files) await import(new URL(name, here).href);
