'use strict';

const { spawn } = require('node:child_process');
const { constants } = require('node:os');
const path = require('node:path');

module.exports = function run(command) {
  const args = process.argv.slice(2);
  if (args.length === 1 && args[0] === '--version') {
    console.log(`${command} ${require('../package.json').version}`);
    return;
  }

  const platform = `${process.platform}-${process.arch}`;
  if (!['linux-x64', 'linux-arm64'].includes(platform)) {
    console.error(`${command}: unsupported platform ${platform}; this release supports Linux on x64 and arm64.`);
    process.exitCode = 1;
    return;
  }

  const binary = path.join(__dirname, '..', 'native', platform, command);
  const child = spawn(binary, args, { stdio: 'inherit' });
  const handlers = new Map();
  for (const signal of ['SIGINT', 'SIGTERM', 'SIGHUP']) {
    const handler = () => child.kill(signal);
    handlers.set(signal, handler);
    process.on(signal, handler);
  }
  function cleanup() {
    for (const [signal, handler] of handlers) process.off(signal, handler);
  }
  child.once('error', error => {
    cleanup();
    console.error(`${command}: could not start the bundled binary (${error.code || error.message}). Reinstall vecnet with npm.`);
    process.exitCode = 1;
  });
  child.once('exit', (code, signal) => {
    cleanup();
    if (signal) {
      // Node ignores SIGPIPE by default. Keep a failure status if re-raising
      // that signal does not terminate this wrapper.
      process.exitCode = 128 + (constants.signals[signal] ?? 1);
      process.kill(process.pid, signal);
    } else process.exitCode = code ?? 1;
  });
};
