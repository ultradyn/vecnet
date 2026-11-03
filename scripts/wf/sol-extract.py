#!/usr/bin/env python3
"""Write a relay agent's assignment to <base>.md, byte for byte.

A workflow can't write files, and a relay agent can't be trusted to copy a long
prompt. So the prompt carries the assignment between two marker lines, and this
reads it back from the agent's own transcript. The harness indents every line of
a computed prompt, so the indent is read from the marker line and taken off again.

usage: sol-extract.py <base> <transcript-glob>
"""
import glob
import json
import os
import re
import sys

base, pattern = sys.argv[1], sys.argv[2]
begin = re.compile(r'^([ \t]*)ASSIGNMENT-BEGIN ' + re.escape(base) + r'$', re.M)
end = re.compile(r'^[ \t]*ASSIGNMENT-END ' + re.escape(base) + r'$', re.M)


def texts(line):
    try:
        content = (json.loads(line).get('message') or {}).get('content')
    except ValueError:
        return []
    if isinstance(content, str):
        return [content]
    if isinstance(content, list):
        return [b.get('text', '') for b in content if isinstance(b, dict)]
    return []


def scan(path):
    with open(path, errors='surrogatepass') as f:
        for i, line in enumerate(f):
            if i > 8:  # the prompt is in the first few lines
                return None
            for t in texts(line):
                # The relay's own later quoting can't match: only its prompt has both markers.
                b, e = begin.search(t), None
                if b:
                    e = end.search(t, b.end())
                if e:
                    indent = b.group(1)
                    lines = t[b.end() + 1:e.start()].split('\n')
                    return '\n'.join(ln[len(indent):] if ln.startswith(indent) else ln.lstrip(' \t') for ln in lines).rstrip('\n') + '\n'
    return None


for path in sorted(glob.glob(pattern), key=os.path.getmtime, reverse=True)[:400]:
    found = scan(path)
    if found:
        os.makedirs(os.path.dirname(base) or '.', exist_ok=True)  # a fresh run's ccc/ dir may not exist yet
        with open(base + '.md', 'w') as f:
            f.write(found)
        print(f'assignment: {len(found.encode())} bytes')
        sys.exit(0)
print('no transcript holds this assignment')
sys.exit(3)
