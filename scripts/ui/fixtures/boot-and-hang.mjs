import { bootDemo } from '../lib.mjs';
import { withTui } from '../tmux.mjs';
const env = await bootDemo();
const { pid, dir, buildDir } = env;
if (process.argv.includes('--tui')) {
  await withTui({ env }, async ({ waitFor }) => {
    await waitFor('vecnet | 1:inbox 2:chat 3:dash 4:subs');
    console.log(JSON.stringify({ pid, dir, buildDir }));
    await new Promise(() => {});
  });
} else {
  console.log(JSON.stringify({ pid, dir, buildDir }));
}
// bootDemo's open stdio keeps this process alive until the test signals it.
