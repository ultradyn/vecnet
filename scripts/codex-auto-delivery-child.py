"""Linux-only private acceptance child containment; no delivery dependency.

The caller journals intent before launch. An undrained result or loss of this
protocol is a retained refusal, including SIGKILL and uninterruptible-I/O cases.
Only actually adopted children are inspected; never scan unrelated processes.
"""
import base64
import ctypes
import json
import os
import selectors
import signal
import subprocess
import sys
import time


def identity(pid):
    try:
        with open('/proc/%d/stat' % pid) as f:
            raw = f.read()
        fields = raw[raw.rfind(')') + 2:].split()
        return dict(pid=pid, birth=fields[19], parent=int(fields[1]),
                    group=int(fields[2]), state=fields[0], uid=os.stat('/proc/%d' % pid).st_uid)
    except (FileNotFoundError, ProcessLookupError):
        return None


def main():
    os.umask(0o077)
    request = sys.stdin.buffer.readline(1048577)
    if len(request) > 1048576 or not request.endswith(b'\n'):
        raise ValueError('control_limit')
    r = json.loads(request)
    timeout = r['timeoutMs'] / 1000
    limit = r['maxBytes']
    if not 0 < timeout <= 600 or not 0 < limit <= 8388608:
        raise ValueError('control_limit')
    if sys.version_info < (3, 9):
        raise ValueError('containment_unavailable')
    libc = ctypes.CDLL(None, use_errno=True)
    value = ctypes.c_int()
    if (libc.prctl(36, 1, 0, 0, 0) != 0 or
            libc.prctl(37, ctypes.byref(value), 0, 0, 0) != 0 or value.value != 1 or
            not hasattr(os, 'pidfd_open') or not hasattr(signal, 'pidfd_send_signal')):
        raise ValueError('containment_unavailable')
    deadline = time.monotonic() + timeout
    os.set_inheritable(3, False)
    p = subprocess.Popen(r['argv'], env=r['env'], cwd=r.get('cwd'), start_new_session=True,
                         stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                         close_fds=True)
    pins = {}
    try:
        pins[p.pid] = (os.pidfd_open(p.pid), identity(p.pid))
    except ProcessLookupError:
        pass  # waitpid still has to prove this spawned child actually exited
    sel = selectors.DefaultSelector()
    for stream, name in [(p.stdout, 'stdout'), (p.stderr, 'stderr')]:
        os.set_blocking(stream.fileno(), False)
        sel.register(stream, selectors.EVENT_READ, name)
    os.set_blocking(p.stdin.fileno(), False)
    data = base64.b64decode(r['input'], validate=True)
    if len(data) > 1048576:
        raise ValueError('control_limit')
    if data:
        sel.register(p.stdin, selectors.EVENT_WRITE, 'input')
    else:
        p.stdin.close()
    os.set_blocking(0, False)
    sel.register(0, selectors.EVENT_READ, 'control')
    output = {'stdout': bytearray(), 'stderr': bytearray()}
    total = 0
    code = None
    error = None
    control = bytearray()
    offered = False
    released = False
    drained = False
    settling = None

    def roots():
        with open('/proc/self/task/%d/children' % os.getpid()) as f:
            raw = f.read(65537)
        if len(raw) > 65536:
            raise ValueError('control_limit')
        children = [int(pid) for pid in raw.split()]
        if len(children) > 256:
            raise ValueError('control_limit')
        rows = []
        for pid in children:
            if pid not in pins:
                try:
                    pin = os.pidfd_open(pid)
                except ProcessLookupError:
                    continue
                row = identity(pid)
                if not row or row['parent'] != os.getpid() or row['uid'] != os.geteuid():
                    os.close(pin)
                    raise ValueError('child_drain_unproved')
                pins[pid] = (pin, row)
            row = identity(pid)
            prior = pins[pid][1]
            if row and (not prior or row['birth'] != prior['birth'] or row['parent'] != os.getpid() or row['uid'] != os.geteuid()):
                raise ValueError('child_drain_unproved')
            if row and row['state'] not in ['Z', 'X']:
                rows.append(row)
        return rows

    def terminate_temporary(rows):
        # Separate-session candidates can be intentional durable owners. Never
        # signal them on a heuristic. Same-session adopted writers and the
        # directly spawned command have explicit temporary ancestry and pidfds.
        for row in rows:
            if row['pid'] == p.pid or row['group'] == p.pid:
                try:
                    signal.pidfd_send_signal(pins[row['pid']][0], signal.SIGKILL)
                except ProcessLookupError:
                    pass

    try:
        while True:
            p.poll()
            if p.returncode is not None:
                code = p.returncode
            empty = False
            while True:
                try:
                    child, status = os.waitpid(-1, os.WNOHANG)
                except ChildProcessError:
                    empty = True
                    break
                if not child:
                    break
                if child == p.pid:
                    code = p.returncode = os.waitstatus_to_exitcode(status)
            live = roots()
            pipes_done = not any(key.data in ['stdout', 'stderr'] for key in sel.get_map().values())
            if empty and code is not None and pipes_done:
                drained = True
                break
            if code is not None and live and pipes_done and r.get('handoff') and not offered and not error:
                os.write(3, (json.dumps({'event': 'handoff', 'children': live}) + '\n').encode())
                offered = True
            now = time.monotonic()
            if now >= deadline and not error:
                error = 'child_timeout' if code is None else 'child_drain_unproved'
            if error:
                if settling is None:
                    settling = now + 0.4
                terminate_temporary(live)
                if now >= settling:
                    break
            for key, event in sel.select(0.01):
                name = key.data
                if name == 'input':
                    try:
                        written = os.write(p.stdin.fileno(), data[:65536])
                        data = data[written:]
                    except BrokenPipeError:
                        data = b''
                    if not data:
                        sel.unregister(p.stdin)
                        p.stdin.close()
                    continue
                chunk = os.read(key.fd, 65536)
                if not chunk:
                    sel.unregister(key.fileobj)
                    if name == 'control':
                        error = error or 'child_drain_unproved'
                    continue
                if name == 'control':
                    control.extend(chunk)
                    if len(control) > 65536:
                        raise ValueError('control_limit')
                    if b'\n' not in control:
                        continue
                    ack = json.loads(bytes(control).split(b'\n')[0])
                    current = roots()
                    wanted = sorted((row['pid'], row['birth']) for row in ack.get('release', []))
                    actual = sorted((row['pid'], row['birth']) for row in current)
                    if not offered or not wanted or wanted != actual or not pipes_done:
                        error = 'child_drain_unproved'
                        continue
                    for row in current:
                        signal.pidfd_send_signal(pins[row['pid']][0], 0)
                    released = True
                    break
                total += len(chunk)
                if total > limit:
                    error = error or 'child_output_limit'
                else:
                    output[name].extend(chunk)
            if released:
                break
    finally:
        sel.close()
        for pin, row in pins.values():
            os.close(pin)
        for stream in [p.stdin, p.stdout, p.stderr]:
            stream.close()
    result = dict(code=code, drained=drained, released=released, error=error,
                  stdout=base64.b64encode(output['stdout']).decode(),
                  stderr=base64.b64encode(output['stderr']).decode())
    if not drained and not released:
        result['error'] = error if error == 'child_output_limit' else 'child_drain_unproved'
    sys.stdout.write(json.dumps(result) + '\n')


try:
    main()
except Exception:
    # Raw exceptions may include command output, input or paths. The parent
    # retains its pending intent on all undecodable/failed supervisor outcomes.
    sys.stdout.write('{"error":"child_drain_unproved","drained":false,"released":false}\n')
    sys.exit(1)
