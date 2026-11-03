import { bootDemo } from '../lib.mjs';
const { pid, dir, buildDir } = await bootDemo();
console.log(JSON.stringify({ pid, dir, buildDir }));
// bootDemo's open stdio keeps this process alive until the test signals it.
