import { isAbsolute } from 'node:path';
import { probeCodexDelivery, startDeliveryRunner } from './delivery.ts';

const usage = 'usage: delivery-main.ts probe --codex-home PATH --thread UUID [--previous-thread UUID] | run --hub NORMALIZED_URL --state-dir PATH --registry PATH';
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
async function main(argv: string[]): Promise<number> {
  const [command, ...rest] = argv, options = new Map<string, string>();
  const allowed = command === 'probe' ? ['--codex-home', '--thread', '--previous-thread'] :
    command === 'run' ? ['--hub', '--state-dir', '--registry'] : [];
  for (let i = 0; i < rest.length; i += 2) {
    if (!allowed.includes(rest[i]) || options.has(rest[i]) || !rest[i + 1] || rest[i + 1].startsWith('--')) throw new Error(usage);
    options.set(rest[i], rest[i + 1]);
  }
  if (command === 'probe') {
    const codexHome = options.get('--codex-home'), thread = options.get('--thread'), previousThread = options.get('--previous-thread');
    if (!codexHome || !isAbsolute(codexHome) || !thread || !uuid.test(thread) || (previousThread !== undefined && !uuid.test(previousThread))) throw new Error(usage);
    process.stdout.write(JSON.stringify(await probeCodexDelivery({ codexHome, thread, previousThread })) + '\n');
    return 0;
  }
  if (command === 'run') {
    const hub = options.get('--hub'), stateDir = options.get('--state-dir'), registry = options.get('--registry');
    if (!hub || !stateDir || !registry) throw new Error(usage);
    const runner = startDeliveryRunner({ hub, stateDir, registry });
    await runner.stopped;
    return 0;
  }
  throw new Error(usage);
}
main(process.argv.slice(2)).then(code => { process.exitCode = code; }, () => {
  // Errors contain no server replies, credentials or supplied argv/path values.
  process.stderr.write('codex delivery refused or failed\n');
  process.exitCode = 2;
});
