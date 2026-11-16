'use strict';

const { spawn } = require('node:child_process');
const { constants } = require('node:os');
const path = require('node:path');

module.exports = function run(command) {
  const args = process.argv.slice(2);
  if (args.length === 1 && args[0] === '--version') {
    console.log(command + ' ' + require('../package.json').version);
    return;
  }

  const platform = process.platform + '-' + process.arch;
  if (!['linux-x64', 'linux-arm64', 'win32-x64', 'win32-arm64'].includes(platform)) {
    console.error(command + ': unsupported platform ' + platform + '; this release supports Linux and Windows on x64 and arm64.');
    process.exitCode = 1;
    return;
  }

  const windows = process.platform === 'win32';
  const binary = path.join(__dirname, '..', 'native', platform, command + (windows ? '.exe' : ''));
  // Never use a shell: paths and user arguments may contain spaces or shell
  // metacharacters. The native child shares this console on Windows.
  const failed = error => {
    // Never echo error.message/spawnargs: they can contain caller credentials.
    const code = typeof error.code === 'string' && /^[A-Z0-9_]+$/.test(error.code) ? error.code : 'UNKNOWN';
    const errno = Number.isInteger(error.errno) ? '; errno=' + error.errno : '';
    console.error(command + ': could not start the bundled binary (' + code + errno + '). Check OS security/quarantine diagnostics or reinstall vecnet with npm.');
    process.exitCode = 1;
  };
  let child;
  try {
    child = spawn(binary, args, { stdio: 'inherit', windowsHide: true });
  } catch (error) {
    failed(error);
    return;
  }
  const handlers = new Map();
  for (const signal of windows ? ['SIGINT'] : ['SIGINT', 'SIGTERM', 'SIGHUP']) {
    // Windows delivers console Ctrl+C to both processes. child.kill('SIGINT')
    // would use TerminateProcess instead, preventing the Go child from draining
    // its connections and database. Wait for its ordinary exit in that case.
    const handler = windows ? () => {} : () => child.kill(signal);
    handlers.set(signal, handler);
    process.on(signal, handler);
  }
  function cleanup() {
    for (const [signal, handler] of handlers) process.off(signal, handler);
  }
  child.once('error', error => {
    cleanup();
    failed(error);
  });
  child.once('exit', (code, signal) => {
    cleanup();
    if (signal) {
      // Node ignores SIGPIPE by default. Keep a failure status if re-raising
      // that signal does not terminate this wrapper.
      process.exitCode = 128 + (constants.signals[signal] ?? 1);
      // Windows has no POSIX re-raise semantics; retain the failure status.
      if (!windows) process.kill(process.pid, signal);
    } else process.exitCode = code ?? 1;
  });
};
