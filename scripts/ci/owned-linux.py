#!/usr/bin/env python3
"""CI command owner: kernel subreaper, not a sampled process ancestry tree."""
import ctypes
import os
import selectors
import signal
import subprocess
import sys
import time

libc = ctypes.CDLL(None, use_errno=True)
if libc.prctl(36, 1, 0, 0, 0) != 0:  # PR_SET_CHILD_SUBREAPER
    raise OSError(ctypes.get_errno(), 'subreaper unavailable')
stop = False

def cancel(_signum, _frame):
    global stop
    stop = True

signal.signal(signal.SIGTERM, cancel)
signal.signal(signal.SIGINT, cancel)
ms, *argv = sys.argv[1:]
child = subprocess.Popen(argv, stdin=subprocess.DEVNULL, start_new_session=True)
selector = selectors.DefaultSelector()
selector.register(sys.stdin, selectors.EVENT_READ)
end = time.monotonic() + int(ms) / 1000
code = None
try:
    while code is None:
        # Wait only for our leader. Orphans are durably adopted by this owner
        # even if it exits before we are scheduled again.
        code = child.poll()
        if code is not None:
            break
        if stop:
            code = 143
            break
        if time.monotonic() >= end:
            code = 124
            break
        if selector.select(min(0.02, max(0, end - time.monotonic()))):
            # EOF also covers abrupt death of the Node wrapper. No daemon can
            # retain this non-inherited control pipe and hide its closure.
            os.read(sys.stdin.fileno(), 4096)
            code = 143
            break
finally:
    try:
        os.killpg(child.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    # Kill direct owned children, reap, repeat. Killing an intermediate causes
    # the kernel to reparent its children here, including detached sessions.
    # Never enumerate or signal unrelated host processes.
    while True:
        children = open(f'/proc/self/task/{os.getpid()}/children').read().split()
        for pid in children:
            try:
                os.kill(int(pid), signal.SIGKILL)
            except ProcessLookupError:
                pass
        try:
            os.waitpid(-1, 0)
        except ChildProcessError:
            break
        except InterruptedError:
            continue
    selector.close()
sys.exit(code if code is not None and code >= 0 else 1)
