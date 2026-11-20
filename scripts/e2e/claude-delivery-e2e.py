#!/usr/bin/env python3
"""End-to-end canary proof of Vecnet's native Claude delivery chain (Phase 2).

One disposable run, entirely inside a loopback-only network namespace
(bubblewrap), proves every link of the pointer-only native delivery chain
against a real Claude Code interactive session, with no test-env provision:

  per-profile `claude canary enable` -> daemon admission and a real
  `inbox --since head` baseline -> hub send receipt -> reserved/attempting/
  disposition log lines -> the native pointer arriving in the live session
  (tmux pane frames) -> the model's fetch of the message through the packaged
  CLI -> transcript rows -> the hub showing NO ack for the delivered cursor.

Controls in the same run: no CLAUDE_CODE_VERSION in the disposable Claude
process and status `build_check: "disabled"` (no provision); a wake sent
before the baseline is never pointered; a hold-priority message stays
inbox-only; a stored cursor from a foreign epoch rebaselines once at the
head; a SIGKILL of the daemon between `attempting` and the write (canary
crash seam) restarts to `uncertain` with no resend; the 129th staged pending
message latches `blocked_pending_limit` with no send; canary disable drains
reserved rows to `fenced_scope_not_admitted` with no new claim; and the
`VECNET_CLAUDE_DELIVERY_CANARY=0` kill switch constructs no adapter.

Nothing here touches a user hub identity, a user session or a paid provider:
the hub is a fresh loopback child, credentials are freshly enrolled, the
model backend is a scripted loopback stub reached through a lab CA proxy,
and the whole chain has no route off the loopback interface. The only gate
is the durable per-profile canary record; the daemon env never enables it.
Real in-session enrollment stages the runtime via a private scheduler
fixture, not the host systemd manager.

Teardown is verified: the vecnet-test-<nonce> tmux session is killed on its
private server, the server is asserted gone, hub/daemon/stub stop, and the
default server is checked for stray vecnet-test-* sessions.

Usage: python3 scripts/e2e/claude-delivery-e2e.py [--repo SRC] [--keep-evidence]
The gallery is built separately from the finished run directory. By default
only the private evidence/ and frames/ exports remain. --keep-evidence
retains raw diagnostics and credentials in the mode-0700 run directory
under /tmp/ve2e-runs; do not publish that directory.
"""

import argparse
import json
import os
import re
import secrets
import shlex
import shutil
import signal
import subprocess
import sys
import threading
import time
import tomllib
import uuid
from pathlib import Path

HOLD = 60.0  # generic poll ceiling

STUB_PORT = 8137
PROXY_PORT = 8138

# ---------------------------------------------------------------- helpers ---


class Fail(Exception):
    pass


def log(msg: str) -> None:
    print(f"[e2e {time.strftime('%H:%M:%S')}] {msg}", flush=True)


def sh(cmd, **kw) -> subprocess.CompletedProcess:
    return subprocess.run(cmd, shell=isinstance(cmd, str), check=False,
                          stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                          text=True, **kw)


def must(cmd: str, what: str) -> str:
    r = sh(cmd)
    if r.returncode != 0:
        raise Fail(f"{what} failed (rc={r.returncode}): {r.stdout.strip()[:800]}")
    return r.stdout


def wait_until(fn, what: str, timeout: float = HOLD, interval: float = 0.5):
    end = time.monotonic() + timeout
    last = None
    while time.monotonic() < end:
        try:
            last = fn()
        except Exception as e:  # noqa: BLE001
            last = f"error: {e}"
        else:
            if last:
                return last
        time.sleep(interval)
    raise Fail(f"timed out after {timeout:.0f}s waiting for {what}; last: {last!r}")


def parse_json_output(text: str) -> dict:
    """Last JSON object line of a CLI output (warnings may precede it)."""
    for line in reversed(text.strip().splitlines()):
        line = line.strip()
        if line.startswith("{"):
            return json.loads(line)
    raise Fail(f"no JSON object in output: {text[:300]!r}")


PREVIEW = "Message from @Vecnet: Vecnet message available."
ADMISSION = "claude_delivery_scope_admitted"
CLAIM_EVENTS = ("claude_delivery_claim_reserved", "claude_delivery_claim_attempting",
                "claude_delivery_disposition")


def ordered_lifecycle(events, scope, generation, delivery_id):
    # Admission is scope-level (it precedes staging a delivery ID); join it
    # to the runtime snapshot by binding and generation, then match claims by ID.
    claims = [(i, row) for i, row in enumerate(events)
              if row.get("deliveryId") == delivery_id and row.get("msg") in CLAIM_EVENTS]
    if (tuple(row["msg"] for _, row in claims) != CLAIM_EVENTS
            or any(row.get("scope") != scope for _, row in claims)):
        raise Fail(f"expected one ordered reserved/attempting/disposition for {delivery_id}: {claims}")
    admission = next((row for row in reversed(events[:claims[0][0]])
                      if row.get("msg") == ADMISSION and row.get("scope") == scope), None)
    if not admission or admission.get("generation") != generation:
        raise Fail(f"no preceding admission for {scope}/{generation} on {delivery_id}")
    return [admission, *(row for _, row in claims)]


def has_pointer(text, message_id):
    return isinstance(text, str) and re.search(
        rf"Vecnet message available\.\s+Find\s+{re.escape(message_id)}\s+in\s+thread\b", text
    ) is not None


def verify_control_frames(paths, message_id, marker):
    message_token = re.compile(rf"(?<![\w]){re.escape(message_id)}(?![\w])")
    for path in paths:
        text = path.read_text()
        if message_token.search(text) or marker in text:
            raise Fail(f"control {message_id} leaked into {path.name}")


def inbox_messages(content):
    """Extract message objects from CLI inbox JSON, if this is an inbox result.

    CLI warnings may precede the payload, so parse the last JSON object line
    (same convention as parse_json_output). Raises ValueError/KeyError/TypeError
    for outputs that are not inbox JSON; callers treat that as an unrelated call.
    """
    if isinstance(content, list):
        content = "\n".join(b["text"] for b in content if b.get("type") == "text")
    for line in reversed(content.strip().splitlines()):
        line = line.strip()
        if line.startswith("{"):
            return [event["data"]["message"] for event in json.loads(line)["events"]
                    if "message" in event.get("data", {})]
    raise ValueError(f"no JSON object in tool result: {content[:120]!r}")


def successful_fetches(results, message_id):
    """Identify successful inbox results by message ID, regardless of the command."""
    matches = []
    for i, block in results:
        if block.get("is_error"):
            continue
        try:
            messages = [m for m in inbox_messages(block.get("content")) if m.get("message_id") == message_id]
        except (AttributeError, KeyError, TypeError, ValueError):
            continue  # unrelated tool output
        if messages:
            matches.append((i, block.get("tool_use_id"), messages))
    return matches


def transcript_proof(rows, message_id, fetch_cmd, marker):
    enqueues, peers, results = [], [], []
    calls = {}
    for i, row in enumerate(rows):
        if row.get("type") == "queue-operation" and row.get("operation") == "enqueue" \
                and has_pointer(row.get("content"), message_id):
            enqueues.append(i)
        origin = row.get("origin") or {}
        if row.get("type") == "user" and origin.get("kind") == "peer" \
                and has_pointer(origin.get("body"), message_id):
            peers.append(i)
        blocks = row.get("message", {}).get("content")
        for block in blocks if isinstance(blocks, list) else []:
            if row.get("type") == "assistant" and block.get("type") == "tool_use" and block.get("id"):
                calls.setdefault(block["id"], []).append((i, block))
            if row.get("type") == "user" and block.get("type") == "tool_result":
                results.append((i, block))
    if len(enqueues) != 1 or len(peers) != 1:
        raise Fail(f"expected one enqueue and one peer turn for {message_id}: "
                   f"found {len(enqueues)}, {len(peers)}")
    fetches = successful_fetches(results, message_id)
    if len(fetches) != 1:
        raise Fail(f"expected exactly one successful fetch result for {message_id}, found {len(fetches)}")
    result_i, tool_id, matches = fetches[0]
    if len(calls.get(tool_id, ())) != 1 or sum(b.get("tool_use_id") == tool_id for _, b in results) != 1:
        raise Fail(f"expected one call and one result for {tool_id}")
    (call_i, call), = calls[tool_id]
    if (not enqueues[0] < peers[0] < call_i < result_i or call.get("name") != "Bash"
            or call.get("input", {}).get("command") != fetch_cmd):
        raise Fail(f"expected an ordered enqueue, peer turn and packaged CLI fetch for {message_id}")
    if len(matches) != 1 or not isinstance(matches[0].get("content"), dict) \
            or marker not in matches[0]["content"].get("text", ""):
        raise Fail(f"fetch result did not contain exactly one matching message {message_id} with its marker")
    return [rows[i] for i in (enqueues[0], peers[0], call_i, result_i)], tool_id


def dispose_run(run, keep_evidence):
    if keep_evidence:
        return "retained privately"
    for path in run.iterdir():
        if path.name in ("evidence", "frames"):
            continue
        if path.is_dir() and not path.is_symlink():
            shutil.rmtree(path)
        else:
            path.unlink()
    return "disposed"


def _ulid(n: int) -> str:
    digits = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
    s = ""
    for _ in range(26):
        s = digits[n % 32] + s
        n //= 32
    return s


PEER_ID = re.compile(r"^peer_[0-9A-HJKMNP-TV-Z]{26}$")
CANARY_ENV = "VECNET_CLAUDE_DELIVERY_CANARY"
CANARY_ENABLE = "vecnet --net claude-rx claude canary enable"
SCOPE_KEY = "claude-claims:scope:"
CLAIM_KEY = "claude-claims:claim:"
FOREIGN_CURSOR = "e999999.0"  # another epoch: what a rebuilt hub log leaves behind


def run_meta(nonce, session, commit, observed, started):
    """The run's provenance: no test-env provision, the canary record is the gate."""
    return {"nonce": nonce, "tmux_session": session, "commit": commit,
            "claude_version_observed": observed,
            "provisions": [],
            "canary_opt_in": CANARY_ENABLE,
            "build_check": "disabled (build gating parked: any Claude Code build is admitted; "
                           "the observed version is recorded, never exported or checked)",
            "started": started}


def claude_launch_env(home, cconfig, sockdir, run, cfg):
    """The disposable Claude session env. Never CLAUDE_CODE_VERSION."""
    return [f"HOME={home}", f"CLAUDE_CONFIG_DIR={cconfig}",
            f"CLAUDE_CODE_MESSAGING_SOCKET={sockdir}/claude.sock",
            f"ANTHROPIC_BASE_URL=http://127.0.0.1:{STUB_PORT}", "ANTHROPIC_API_KEY=dummy-e2e-key",
            f"HTTPS_PROXY=http://127.0.0.1:{PROXY_PORT}", f"HTTP_PROXY=http://127.0.0.1:{PROXY_PORT}",
            "NO_PROXY=127.0.0.1,localhost", f"NODE_EXTRA_CA_CERTS={run}/ca.pem",
            "DISABLE_TELEMETRY=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
            "DISABLE_AUTOUPDATER=1", "TERM=xterm-256color", f"VECNET_CONFIG={cfg}"]


def daemon_env(home, cfg, vecnet_bin, seam=False, kill_switch=False):
    """The disposable daemon env. The canary env var is never an enable: it is
    absent, or exactly `0` for the kill-switch control."""
    env = [f"HOME={home}", f"VECNET_CONFIG={cfg}", f"VECNET_BIN={vecnet_bin}", "VECNET_LOCAL_MAINTENANCE=0"]
    if kill_switch:
        env.append(f"{CANARY_ENV}=0")
    if seam:
        env.append("VECNET_CLAUDE_DELIVERY_CRASH_BEFORE_WRITE=1")
    return env


def check_no_provision(environ: bytes, status: dict) -> dict:
    """No provision: the disposable Claude process carries no
    CLAUDE_CODE_VERSION, and status honestly reports the parked build check
    with the per-profile record as the only gate."""
    keys = [entry.split(b"=", 1)[0] for entry in environ.split(b"\0") if entry]
    if b"CLAUDE_CODE_VERSION" in keys:
        raise Fail("CLAUDE_CODE_VERSION is present in the disposable Claude process environment")
    if (status.get("mode"), status.get("canary_opt_in"), status.get("build_check")) != ("canary", True, "disabled"):
        raise Fail(f"status is not canary/opted-in/build_check disabled: {status}")
    return {"claude_code_version_present": False, "mode": status["mode"],
            "canary_opt_in": True, "build_check": status["build_check"]}


def check_canary_off(deliveries: dict, status: dict, off_delivery: str, fenced: str) -> dict:
    """Canary off: the wake made no claim, status is legacy and not opted in,
    and the seeded reserved row drained to fenced_scope_not_admitted."""
    rows = {c.get("delivery_id"): c for c in deliveries.get("deliveries", [])}
    if off_delivery in rows:
        raise Fail(f"canary-off wake {off_delivery} was claimed: {rows[off_delivery]}")
    row = rows.get(fenced)
    if not row or (row.get("state"), row.get("reason")) != ("released", "fenced_scope_not_admitted"):
        raise Fail(f"reserved row {fenced} was not drained to fenced_scope_not_admitted: {row}")
    if any(c.get("state") in ("reserved", "attempting") for c in rows.values()):
        raise Fail("a reserved or attempting claim survived the disable drain")
    if (status.get("mode"), status.get("canary_opt_in"), status.get("build_check")) != ("legacy", False, "disabled"):
        raise Fail(f"canary-off status is not legacy/not opted in: {status}")
    return {"mode": "legacy", "canary_opt_in": False, "fenced": fenced, "claims_for_wake": 0}


def check_rebaseline(events, scope, generation):
    """A hub-rejected stored cursor rebaselines exactly once at the head: one
    `cursor_rejected` followed by one `scope_baselined`, then no retry loop."""
    picked = [e.get("msg") for e in events if e.get("scope") == scope and e.get("msg") in
              ("claude_delivery_cursor_rejected", "claude_delivery_scope_baselined")]
    if picked != ["claude_delivery_cursor_rejected", "claude_delivery_scope_baselined"]:
        raise Fail(f"expected one rejected cursor then one baseline, got {picked}")
    base = next(e for e in events if e.get("msg") == "claude_delivery_scope_baselined" and e.get("scope") == scope)
    if base.get("generation") != generation:
        raise Fail("rebaseline under the wrong generation")
    if any(e.get("msg") == "claude_delivery_reconcile_failed" for e in events):
        raise Fail("rebaseline pass reported a reconcile failure (retry loop)")
    return picked


def check_backlog(frames, rows, events, message_id, delivery_id):
    """A wake that arrived before the baseline is never pointered: no pointer
    for it in any frame or transcript row and no claim for it in the daemon
    log. (Its body may appear in the positive inbox fetch, which reads the
    whole unacked inbox; that is the legacy inbox path, not a pointer.)"""
    for path in frames:
        if has_pointer(path.read_text(), message_id):
            raise Fail(f"backlog {message_id} was pointered in {path.name}")
    for row in rows:
        if has_pointer(row.get("content"), message_id) or has_pointer((row.get("origin") or {}).get("body"), message_id):
            raise Fail(f"backlog {message_id} was pointered in the transcript")
        content = (row.get("message") or {}).get("content")
        if isinstance(content, str) and has_pointer(content, message_id):
            raise Fail(f"backlog {message_id} was pointered in the transcript")
    if any(delivery_id in json.dumps(e) for e in events):
        raise Fail(f"backlog {delivery_id} entered the claim lifecycle")


def deliver_window_ms(attempting_at: str, disposition_at: str) -> int:
    """Upper bound on the deliver helper's bindings-lock hold: attempting to disposition."""
    import datetime
    a, b = (datetime.datetime.fromisoformat(t.replace("Z", "+00:00")) for t in (attempting_at, disposition_at))
    return round((b - a).total_seconds() * 1000)


def cursor_seq(cursor: str):
    m = re.fullmatch(r"e(\d+)\.(\d+)", cursor or "")
    if not m:
        raise Fail(f"not a hub cursor: {cursor!r}")
    return int(m[1]), int(m[2])


# ------------------------------------------------------ embedded lab scripts ---
# The model stub: a Messages-API SSE server whose scripted replies drive the
# disposable session through the fetch the pointer asks for.

STUB_PY = r'''#!/usr/bin/env python3
import json, sys, uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(sys.argv[1]); LOG = open(sys.argv[2], "a", buffering=1)
FETCH_CMD, ENROL_CMD = sys.argv[3:5]


def block_text(messages):
    out = []
    for m in messages or []:
        c = m.get("content")
        if isinstance(c, str):
            out.append(c)
        elif isinstance(c, list):
            for b in c:
                if not isinstance(b, dict):
                    continue
                if b.get("type") == "text":
                    out.append(b.get("text", ""))
                elif b.get("type") == "tool_result":
                    cc = b.get("content")
                    if isinstance(cc, str):
                        out.append(cc)
                    elif isinstance(cc, list):
                        for x in cc:
                            if isinstance(x, dict) and x.get("type") == "text":
                                out.append(x.get("text", ""))
    return "\n".join(out)


def has_tool_result(messages):
    for m in messages or []:
        c = m.get("content")
        if isinstance(c, list) and any(isinstance(b, dict) and b.get("type") == "tool_result" for b in c):
            return True
    return False


class H(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0) or 0)
        raw = self.rfile.read(n)
        try:
            req = json.loads(raw)
        except Exception:
            req = {}
        LOG.write(json.dumps({"path": self.path, "body": raw.decode("utf8", "replace")}) + "\n")
        if self.path.endswith("/count_tokens"):
            out = json.dumps({"input_tokens": 1}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(out)))
            self.end_headers()
            self.wfile.write(out)
            return
        messages = req.get("messages") or []
        text = block_text(messages)
        if "Vecnet message available." in text and not any(
            isinstance(b, dict) and b.get("tool_use_id") == "toolu_e2e_fetch_1"
            for m in messages for b in m.get("content", [])
        ):
            blocks = [{"type": "text", "text": "A Vecnet pointer arrived. Fetching the inbox now."},
                      {"type": "tool_use", "id": "toolu_e2e_fetch_1", "name": "Bash",
                       "input": {"command": FETCH_CMD,
                                 "description": "Read this session's Vecnet inbox"}}]
            stop = "tool_use"
        elif has_tool_result(messages) and "Vecnet message available." in text:
            # Echo the inbox result only, never the startup context or prompt.
            result = next((b.get("content", "") for m in reversed(messages)
                           for b in m.get("content", []) if isinstance(b, dict)
                           and b.get("type") == "tool_result"), "")
            body = result if isinstance(result, str) else json.dumps(result)
            blocks = [{"type": "text",
                       "text": "Vecnet inbox fetch completed. The tool output was:\n" + body +
                               "\n(end of fetched inbox data)"}]
            stop = "end_turn"
        elif "SETUP: enroll the disposable receiver." in text and not has_tool_result(messages):
            blocks = [{"type": "tool_use", "id": "toolu_e2e_enrol_1", "name": "Bash",
                       "input": {"command": ENROL_CMD, "description": "Enroll the disposable receiver"}}]
            stop = "tool_use"
        elif "SETUP: enroll the disposable receiver." in text and "READY: acknowledge" not in text:
            blocks = [{"type": "text", "text": "Disposable receiver enrollment finished."}]
            stop = "end_turn"
        elif "READY" in text:
            blocks = [{"type": "text", "text": "READY: e2e receiver idle; stub model online."}]
            stop = "end_turn"
        else:
            blocks = [{"type": "text", "text": "unexpected-stub-request: " + text[:400]}]
            stop = "end_turn"
        if req.get("stream"):
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()

            def ev(name, obj):
                self.wfile.write(f"event: {name}\ndata: {json.dumps(obj)}\n\n".encode())
            ev("message_start", {"type": "message_start", "message": {
                "id": "msg_stub_" + uuid.uuid4().hex[:8], "type": "message",
                "role": "assistant", "model": req.get("model", "stub"),
                "content": [], "stop_reason": None,
                "usage": {"input_tokens": 10, "output_tokens": 1}}})
            for i, b in enumerate(blocks):
                if b["type"] == "text":
                    ev("content_block_start", {"type": "content_block_start", "index": i,
                                               "content_block": {"type": "text", "text": ""}})
                    ev("content_block_delta", {"type": "content_block_delta", "index": i,
                                               "delta": {"type": "text_delta", "text": b["text"]}})
                    ev("content_block_stop", {"type": "content_block_stop", "index": i})
                else:
                    ev("content_block_start", {"type": "content_block_start", "index": i,
                                               "content_block": {"type": "tool_use", "id": b["id"],
                                                                 "name": b["name"], "input": {}}})
                    ev("content_block_delta", {"type": "content_block_delta", "index": i,
                                               "delta": {"type": "input_json_delta",
                                                         "partial_json": json.dumps(b["input"])}})
                    ev("content_block_stop", {"type": "content_block_stop", "index": i})
            ev("message_delta", {"type": "message_delta", "delta": {"stop_reason": stop},
                                 "usage": {"output_tokens": 10}})
            ev("message_stop", {"type": "message_stop"})
        else:
            out = json.dumps({"id": "msg_stub_1", "type": "message", "role": "assistant",
                              "model": req.get("model", "stub"), "content": blocks,
                              "stop_reason": stop,
                              "usage": {"input_tokens": 10, "output_tokens": 10}}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(out)))
            self.end_headers()
            self.wfile.write(out)

    def do_GET(self):
        self.send_response(404)
        self.end_headers()


ThreadingHTTPServer(("127.0.0.1", PORT), H).serve_forever()
'''

# The catch-all proxy: Claude Code's startup gate probes vendor endpoints
# even with a custom base URL. This proxy answers CONNECT with local TLS
# (lab CA) and 200 {} for everything, so no packet ever leaves loopback.
PROXY_PY = r'''#!/usr/bin/env python3
import json, socket, ssl, sys, threading

PORT = int(sys.argv[1]); DIR = sys.argv[2]; LOG = open(sys.argv[3], "a", buffering=1)
ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain(DIR + "/srv.pem", DIR + "/srv.key")


def read_request(f):
    line = f.readline().decode("utf8", "replace").strip()
    headers = {}
    while True:
        h = f.readline().decode("utf8", "replace").strip()
        if not h:
            break
        k, _, v = h.partition(":")
        headers[k.strip().lower()] = v.strip()
    return line, headers


def reply(sock, body=b"{}"):
    sock.sendall(b"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: "
                 + str(len(body)).encode() + b"\r\n\r\n" + body)


def handle(conn):
    try:
        conn.settimeout(30)
        f = conn.makefile("rb")
        line, headers = read_request(f)
        if not line:
            conn.close()
            return
        method, _, rest = line.partition(" ")
        if method == "CONNECT":
            LOG.write(json.dumps({"connect": rest}) + "\n")
            conn.sendall(b"HTTP/1.1 200 Connection established\r\n\r\n")
            tls = ctx.wrap_socket(conn, server_side=True)
            tf = tls.makefile("rb")
            while True:
                try:
                    tline, theaders = read_request(tf)
                except Exception:
                    break
                if not tline:
                    break
                n = int(theaders.get("content-length", 0) or 0)
                if n:
                    tf.read(n)
                LOG.write(json.dumps({"tunneled": tline}) + "\n")
                reply(tls)
            tls.close()
        else:
            n = int(headers.get("content-length", 0) or 0)
            if n:
                f.read(n)
            LOG.write(json.dumps({"proxied": line}) + "\n")
            reply(conn)
            conn.close()
    except Exception:
        try:
            conn.close()
        except Exception:
            pass


srv = socket.socket()
srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
srv.bind(("127.0.0.1", PORT))
srv.listen(64)
while True:
    conn, _ = srv.accept()
    threading.Thread(target=handle, args=(conn,), daemon=True).start()
'''

HOSTS = """127.0.0.1 localhost
127.0.0.1 api.anthropic.com statsig.anthropic.com www.anthropic.com anthropic.com status.anthropic.com claude.ai claude.com platform.claude.com sentry.io
::1 localhost
"""

SAN_CNF = """[req]
distinguished_name=dn
req_extensions=v3
[dn]
CN=api.anthropic.com
[v3]
subjectAltName=DNS:api.anthropic.com,DNS:*.anthropic.com,DNS:anthropic.com,DNS:*.claude.com,DNS:claude.com,DNS:claude.ai,DNS:*.claude.ai,DNS:statsig.com,DNS:*.statsig.com,DNS:*.sentry.io,DNS:sentry.io,DNS:localhost,DNS:*.localhost,IP:127.0.0.1
"""


class Harness:
    def __init__(self, repo: Path, run: Path):
        self.repo = repo
        self.run = run
        self.nonce = secrets.token_hex(4)
        self.session = f"vecnet-test-{self.nonce}"
        self.home = run / "home"
        self.cconfig = run / "claude-config"
        self.workdir = run / "work"
        self.hubdir = run / "hub"
        self.bin = run / "bin"
        self.logs = run / "logs"
        self.frames = run / "frames"
        self.evidence = run / "evidence"
        self.tdir = run / "tdir"
        self.sockdir = run / "sockdir"
        self.cfg = self.home / ".config/vecnet/config.toml"
        self.sock_name = f"ve2e{self.nonce}"
        self.claude_bin = Path("/home/xertrov/.local/bin/claude")
        self.node_bin = Path("/home/xertrov/.local/bin/node").resolve()
        self.fetch_cmd = f"{run}/vec.sh inbox --json --limit 8"
        self.enrol_cmd = f"{run}/enrol-receiver.sh"
        self.unit = "vecnet-codex-delivery.service"
        self.runtime = None
        self.frame_thread = None
        self.frame_error = None
        self.transcript = None
        self.hub_url = None
        self.operator_secret = None
        self.claude_peer = None
        self.sender_peer = None
        self.binding_key = None
        self.generation = None
        self.daemon_window = None
        self.frame_index = []
        self.log_marks = {}
        self.frame_stop = threading.Event()
        self.results = {}
        self.cmd_seq = 0
        self.started = time.time()

    # ----------------------------------------------------------- plumbing ---

    def tmux(self, *args, check=True) -> str:
        env = dict(os.environ, TMUX_TMPDIR=str(self.tdir))
        r = subprocess.run(["tmux", "-L", self.sock_name, *args],
                           capture_output=True, text=True, env=env)
        if check and r.returncode != 0:
            raise Fail(f"tmux {' '.join(args)}: {r.stderr.strip()}")
        return r.stdout

    def pane_text(self) -> str:
        return self.tmux("capture-pane", "-t", self.session, "-p", "-S", "-3000", check=False)

    def in_netns(self, snippet: str, timeout: float = 90.0, name: str = None):
        """Run a shell snippet inside the netns (a tmux window on the private
        server); returns (rc, combined output)."""
        self.cmd_seq += 1
        tag = name or f"cmd{self.cmd_seq}"
        script = self.run / f"cmd-{tag}.sh"
        out = self.logs / f"{tag}.out"
        rcfile = self.logs / f"{tag}.rc"
        script.write_text(f"{{ {snippet}\n}} > {out} 2>&1\nprintf '%s' $? > {rcfile}\n")
        script.chmod(0o755)
        rcfile.unlink(missing_ok=True)
        self.tmux("new-window", "-d", "-n", tag[:40], f"sh {script}")
        wait_until(lambda: rcfile.exists(), f"netns command {tag}", timeout=timeout, interval=0.2)
        rc = int((rcfile.read_text().strip() or "1"))
        text = out.read_text() if out.exists() else ""
        return rc, text

    def cli(self, argv, timeout: float = 60.0, name: str = None):
        argv = [str(a) for a in argv]
        line = " ".join(shlex.quote(a) for a in argv)
        env = f"HOME={shlex.quote(str(self.home))} VECNET_CONFIG={shlex.quote(str(self.cfg))}"
        return self.in_netns(f"env {env} {shlex.quote(str(self.bin / 'vecnet'))} {line}",
                             timeout=timeout, name=name)

    def evidence_write(self, name: str, text: str) -> None:
        (self.evidence / name).write_text(text)
        log(f"evidence: {name} ({len(text)} bytes)")

    def record(self, key: str, value) -> None:
        self.results[key] = value

    # -------------------------------------------------------------- setup ---

    def preflight(self) -> None:
        log("phase 0: preflight + build")
        ver = must(f"{self.claude_bin} --version", "claude --version")
        m = re.search(r"(\d+\.\d+\.\d+)", ver)
        # Build gating is parked, so no build is qualified or hashed here. The
        # dialog automation and stub are scripted against this lab build.
        if not m or m.group(1) != "2.1.322":
            raise Fail(f"the scripted lab session targets Claude Code 2.1.322, found: {ver!r}")
        self.claude_version = m.group(1)
        for tool, label in ((shutil.which("bwrap"), "bwrap"), (shutil.which("tmux"), "tmux"),
                            (shutil.which("sqlite3"), "sqlite3"), (shutil.which("openssl"), "openssl"),
                            (self.node_bin, "node")):
            if not tool:
                raise Fail(f"missing required tool: {label}")
        noderun = sh(f"{self.node_bin} --version")
        if noderun.returncode != 0 or tuple(int(x) for x in noderun.stdout.strip().lstrip("v").split(".")[:2]) < (22, 18):
            raise Fail(f"node >= 22.18 required, found {noderun.stdout!r}")
        for d in (self.home, self.cconfig, self.workdir, self.hubdir, self.bin,
                  self.logs, self.frames, self.evidence, self.tdir, self.sockdir):
            d.mkdir(parents=True, exist_ok=True)
        self.sockdir.chmod(0o700)
        self.cconfig.chmod(0o700)
        self.home.chmod(0o700)  # the local registry requires an owner-only HOME
        commit = must(f"git -C {self.repo} rev-parse HEAD", "git rev-parse").strip()
        must(f"cd {self.repo} && CGO_ENABLED=0 go build -o {self.bin / 'vecnet'} ./cmd/vecnet && "
             f"CGO_ENABLED=0 go build -o {self.bin / 'vecnet-hub'} ./cmd/vecnet-hub", "go build")
        (self.bin / "vecnet").chmod(0o755)
        self.record("build", {"commit": commit, "claude": self.claude_version,
                              "node": noderun.stdout.strip()})
        self.evidence_write("00-run-meta.json", json.dumps(run_meta(
            self.nonce, self.session, commit, self.claude_version,
            time.strftime("%Y-%m-%dT%H:%M:%S%z")), indent=2))

    def lab_stack(self) -> None:
        log("phase 1: loopback netns, tmux server, certs, stub + proxy")
        d = self.run
        (d / "stub.py").write_text(STUB_PY)
        (d / "proxy.py").write_text(PROXY_PY)
        (d / "hosts").write_text(HOSTS)
        (d / "san.cnf").write_text(SAN_CNF)
        cfg_rel = "home/.config/vecnet/config.toml"
        (d / "vec.sh").write_text(
            "#!/bin/sh\n"
            f"exec env HOME={d}/home VECNET_CONFIG={d}/{cfg_rel} "
            f"{d}/bin/vecnet --net claude-rx \"$@\"\n")
        (d / "vec.sh").chmod(0o755)
        must(f"cd {d} && openssl req -x509 -newkey rsa:2048 -keyout ca.key -out ca.pem -days 2 -nodes "
             f"-subj /CN=ve2e-lab-ca 2>/dev/null && "
             f"openssl req -new -newkey rsa:2048 -keyout srv.key -out srv.csr -nodes "
             f"-subj /CN=api.anthropic.com -config san.cnf 2>/dev/null && "
             f"openssl x509 -req -in srv.csr -CA ca.pem -CAkey ca.key -CAcreateserial -out srv.pem "
             f"-days 2 -extfile san.cnf -extensions v3 2>/dev/null", "lab CA")
        # Use the existing acceptance fixture for scheduler commands, never
        # the host systemd user bus. The fixture starts the real staged runtime.
        (d / ".automatic-session-fixture").write_text("fixture-v1")
        adapter = (self.repo / 'scripts/automatic-session-smoke.mjs').as_uri()
        fixture = json.dumps({"root": str(d), "home": str(self.home)})
        (self.bin / "systemctl").write_text(
            f"#!{self.node_bin}\n"
            f"import {{ fixtureSystemctl }} from {json.dumps(adapter)};\n"
            f"process.stdout.write(await fixtureSystemctl({fixture}, process.argv.slice(2)));\n")
        # An extensionless launcher outside a package needs explicit ES-module mode.
        (self.bin / "package.json").write_text('{"type":"module"}\n')
        (self.bin / "systemctl").chmod(0o700)
        entry = (f"bwrap --ro-bind / / --dev /dev --proc /proc --ro-bind {d}/hosts /etc/hosts "
                 f"--tmpfs /tmp --bind {d} {d} --unshare-net -- "
                 f"env -i HOME={self.home} PATH={self.bin}:/usr/bin:/bin TERM=xterm-256color "
                 f"TMUX_TMPDIR={d}/tdir LC_ALL=C.UTF-8 "
                 f"sh -c 'umask 022; exec tmux -f /dev/null -L {self.sock_name} "
                 f"new-session -d -s svc -x 200 -y 50'")
        subprocess.Popen(entry, shell=True, start_new_session=True,
                         stdout=open(d / "logs/tmux-entry.out", "w"), stderr=subprocess.STDOUT)
        wait_until(lambda: self.tmux("list-sessions", check=False).startswith("svc"),
                   "private tmux server inside netns", timeout=20)
        self.tmux("new-window", "-d", "-n", "proxy",
                  f"python3 {d}/proxy.py {PROXY_PORT} {d} {d}/logs/proxy-observed.jsonl > {d}/logs/proxy.out 2>&1")
        self.tmux("new-window", "-d", "-n", "stub",
                  f"python3 {d}/stub.py {STUB_PORT} {d}/logs/stub-observed.jsonl "
                  f"{shlex.quote(self.fetch_cmd)} {shlex.quote(self.enrol_cmd)} > {d}/logs/stub.out 2>&1")
        rc, out = 1, ""
        for _ in range(6):
            rc, out = self.in_netns(
                f"curl -s -o /dev/null -w %{{http_code}} -X POST -H content-type:application/json "
                f"-d '{json.dumps({'stream': False})}' http://127.0.0.1:{STUB_PORT}/v1/messages",
                name="loopback-check")
            if rc == 0 and "200" in out:
                break
            time.sleep(1)
        if rc != 0 or "200" not in out:
            raise Fail(f"stub unreachable inside netns: rc={rc} out={out!r}")
        log("netns stack up (private tmux server, stub, catch-all proxy)")

    def hub_up(self) -> None:
        log("phase 2: disposable loopback hub")
        rc, out = self.in_netns(f"{self.bin / 'vecnet-hub'} init --data-dir {self.hubdir} --operator-name e2e-op",
                                name="hub-init")
        if rc != 0:
            raise Fail(f"hub init failed: {out}")
        peer = re.search(r"operator peer:\s+(\S+)", out)
        secret = re.search(r"operator secret:\s+(\S+)", out)
        if not peer or not secret:
            raise Fail(f"cannot parse hub init output: {out!r}")
        self.operator_secret = secret.group(1)
        self.evidence_write("10-hub-init.txt", out.replace(self.operator_secret, "<redacted>"))
        self.tmux("new-window", "-d", "-n", "hub",
                  f"sh -c '{self.bin}/vecnet-hub --listen 127.0.0.1:0 --data-dir {self.hubdir} "
                  f"> {self.logs}/hub.log 2>&1; echo $? > {self.logs}/hub.rc'")
        self.hub_url = wait_until(self._hub_url, "hub listening", timeout=30)
        log(f"hub up at {self.hub_url}")

    def _hub_url(self):
        try:
            text = (self.logs / "hub.log").read_text()
        except FileNotFoundError:
            return None
        m = re.search(r"listening on http://(\S+)", text)
        return f"http://{m.group(1)}" if m else None

    def api(self, path: str, body: dict, bearer: str = None) -> dict:
        auth = f"-H 'Authorization: Bearer {bearer}'" if bearer else ""
        rc, out = self.in_netns(
            f"curl -s -X POST -H content-type:application/json {auth} "
            f"-d {shlex.quote(json.dumps(body))} {self.hub_url}{path}",
            name="api" + re.sub(r"[/]", "-", path))
        if rc != 0:
            raise Fail(f"hub api {path} failed: {out}")
        try:
            return json.loads(out)
        except json.JSONDecodeError:
            raise Fail(f"hub api {path} non-JSON: {out!r}")

    def enrol(self) -> None:
        log("phase 3: fresh profiles + credentials (never a user identity)")
        sender_tok = self.api("/v1/admin/peers", {"kind": "session", "name": "e2e-sender"},
                              self.operator_secret)["enrol_token"]
        claude_tok = self.api("/v1/admin/peers", {"kind": "session", "name": "claude-receiver"},
                              self.operator_secret)["enrol_token"]
        rc, out = self.cli(["enrol", "--hub", self.hub_url, "--token", sender_tok,
                            "--profile", "sender", "--allow-insecure-http"], name="enrol-sender")
        if rc != 0:
            raise Fail(f"sender enrol failed: {out}")
        self.evidence_write("20-enrol-sender.txt", out.replace(sender_tok, "<redacted-token>"))
        token_file = self.run / "receiver-invite"
        token_file.write_text(claude_tok + "\n")
        token_file.chmod(0o600)
        # The Bash tool is a real descendant of the disposable Claude process.
        # If Bash does not expose the session ID, read Claude's own live row
        # for the host-provided PID; registerClaudeRoot verifies ancestry itself.
        enrol = Path(self.enrol_cmd)
        enrol.write_text(f"""#!/bin/sh
set -eu
output={self.logs / 'enrol-claude-rx.out'}
exec >"$output" 2>&1
trap 'printf "%s" "$?" > {self.logs / 'enrol-claude-rx.rc'}' EXIT
: "${{CLAUDE_PID:?missing live Claude PID}}"
printf '%s' "$CLAUDE_PID" > {self.logs / 'claude.pid'}
if [ -z "${{CLAUDE_CODE_SESSION_ID:-}}" ]; then
  CLAUDE_CODE_SESSION_ID=$(/usr/bin/python3 -I -c 'import json,sys; print(json.load(open(sys.argv[1]))["sessionId"])' "$CLAUDE_CONFIG_DIR/sessions/$CLAUDE_PID.json")
  printf '%s\n' 'Session ID source: live Claude session registry'
else
  printf '%s\n' 'Session ID source: Claude Bash environment'
fi
export CLAUDE_CODE_SESSION_ID
export PATH={self.bin}:/usr/bin:/bin VECNET_NODE={self.node_bin}
{self.bin / 'vecnet'} enrol --hub {self.hub_url} --token - --profile claude-rx --allow-insecure-http < {token_file}
""")
        enrol.chmod(0o700)

    def claude_session_up(self) -> None:
        log("phase 4: Claude session + automatic in-session enrollment")
        settings = {
            "crossSessionInbound": "accept",
            "permissions": {"allow": [f"Bash({self.run}/vec.sh:*)", f"Bash({self.enrol_cmd})"]},
            "theme": "dark",
        }
        (self.cconfig / "settings.json").write_text(json.dumps(settings, indent=2))
        (self.cconfig / ".claude.json").write_text(json.dumps({
            "userID": str(uuid.uuid4()), "machineID": str(uuid.uuid4()),
            "firstStartTime": "2026-11-01T00:00:00.000Z", "firstStartVersion": "2.1.300",
            "hasCompletedOnboarding": True, "lastOnboardingVersion": "2.1.322",
            "numStartups": 3, "installMethod": "unknown", "autoUpdates": False}))
        self.launch_claude()
        self.send_prompt("SETUP: enroll the disposable receiver.")
        rcfile = self.logs / "enrol-claude-rx.rc"
        wait_until(lambda: rcfile.exists() and rcfile.read_text(), "in-session enrol completion", timeout=90)
        out = (self.logs / "enrol-claude-rx.out").read_text()
        self.evidence_write("21-enrol-claude-rx.txt", out)
        phase = re.search(r"credentials saved; claude setup (registered|armed):", out)
        if rcfile.read_text() != "0" or not phase:
            raise Fail(f"automatic receiver enrollment failed: {out}")
        manifest = json.loads((self.home / ".local/share/vecnet-codex/manifest.json").read_text())
        self.runtime = Path(manifest["release"])
        if self.runtime.parent != self.home / ".local/share/vecnet-codex":
            raise Fail("staged runtime is outside the disposable HOME")
        self.record("enrollment", {"phase": phase[1], "scheduler": "private systemctl fixture",
                                   "runtime": "CLI-staged embedded release"})
        # This fixture activation is passive. Stop it before reusing the exact
        # staged runtime under tmux (the crash seam needs its own launch).
        self.stop_managed_runtime()
        profiles = tomllib.loads(self.cfg.read_text())["profiles"]
        self.sender_peer = profiles["sender"]["peer_id"]
        self.claude_peer = profiles["claude-rx"]["peer_id"]
        if not (PEER_ID.match(self.sender_peer) and PEER_ID.match(self.claude_peer)):
            raise Fail(f"hub-issued identities are not peer_<ULID>: {self.sender_peer!r} {self.claude_peer!r}")
        public = ["[profiles]"]
        for name in ("claude-rx", "sender"):
            public.append(f"  [profiles.{name}]")
            for key in ("hub", "hub_pubkey", "peer_id", "allow_insecure_http"):
                public.append(f"    {key} = {json.dumps(profiles[name][key])}")
            public.append('    secret = "<redacted>"')
        self.evidence_write("22-config.toml.redacted", "\n".join(public) + "\n")
        self.evidence_write("23-runtime-stage.txt",
                            "Automatic enrollment staged the embedded delivery runtime via the private "
                            "systemctl acceptance fixture, not the host manager. The passive fixture "
                            "process was stopped before the staged release was relaunched in canary mode.\n"
                            f"release digest: {self.runtime.name}\n")
        self.provision_check()
        self.backlog_send()
        rc, out = self.cli(["--net", "claude-rx", "claude", "canary", "enable"], name="canary-enable")
        if rc != 0:
            raise Fail(f"claude canary enable failed: {out}")
        self.evidence_write("25-canary-enable.txt", f"$ {CANARY_ENABLE}\n{out}")
        self.start_daemon()
        env = daemon_env(self.home, self.cfg, self.runtime / "vecnet")
        if any(e.startswith(CANARY_ENV + "=") for e in env):
            raise Fail("the daemon env must not carry the canary variable")
        self.evidence_write("30-daemon-env.txt", "\n".join(env) + "\nnode_flags=--experimental-strip-types\n"
                            f"# {CANARY_ENV} is absent: the per-profile canary record is the only gate\n")
        wait_until(lambda: "Disposable receiver enrollment finished." in self.pane_text(),
                   "stub enrollment reply", timeout=45)
        self.drive_first_prompt()
        self.baseline_check()

    def provision_check(self) -> None:
        log("phase 4b: no provision in the disposable Claude process")
        pid = int((self.logs / "claude.pid").read_text().strip())
        environ = Path(f"/proc/{pid}/environ").read_bytes()
        names = sorted({e.split(b"=", 1)[0].decode() for e in environ.split(b"\0") if e})
        config = [e.split(b"=", 1)[1].decode() for e in environ.split(b"\0") if e.startswith(b"CLAUDE_CONFIG_DIR=")]
        if config != [str(self.cconfig)]:
            raise Fail(f"pid {pid} is not the disposable Claude process")
        if "CLAUDE_CODE_VERSION" in names:
            raise Fail("CLAUDE_CODE_VERSION is present in the disposable Claude process environment")
        self.claude_environ = environ
        # Names only: values carry lab paths and the dummy key.
        self.evidence_write("24-claude-environ-names.txt",
                            f"/proc/<disposable claude pid>/environ variable names (values withheld)\n"
                            f"CLAUDE_CONFIG_DIR is the disposable config root: true\n"
                            f"CLAUDE_CODE_VERSION present: false\n" + "".join(n + "\n" for n in names))

    def backlog_send(self) -> None:
        log("phase 4c: pre-admission backlog wake (must never be pointered)")
        self.backlog_marker = "BACKLOG-E2E-" + secrets.token_hex(8)
        rc, out = self.cli(["--net", "sender", "send", self.claude_peer,
                            f"{self.backlog_marker}: sent before the canary baseline.", "--wake", "--json"],
                           name="send-backlog")
        if rc != 0:
            raise Fail(f"backlog send failed: {out}")
        self.evidence_write("26-backlog-send.json", out)
        receipt = parse_json_output(out)
        self.backlog_delivery = receipt["deliveries"][0]["delivery_id"]
        self.backlog_message = receipt["message_id"]

    @property
    def ledger(self) -> Path:
        return self.home / ".local/state/vecnet/codex/delivery.sqlite"

    def ledger_sql(self, sql: str, tag: str, readonly: bool = False) -> str:
        """Run SQL against the disposable delivery ledger. Writes only with the
        daemon stopped. A read-write sqlite3 CLI close unlinks a live host's
        -wal/-shm (the host then writes to an orphaned WAL no reader sees), so
        a read while a daemon may be live must be -readonly."""
        if not readonly and self.daemon_window:
            raise Fail(f"ledger write {tag} while a daemon is live")
        script = self.run / f"{tag}.sql"
        script.write_text(sql + "\n")
        rc, out = self.in_netns(f"sqlite3 {'-readonly ' if readonly else ''}{self.ledger} < {script}", name=tag)
        if rc != 0:
            raise Fail(f"ledger {tag} failed: {out[:400]}")
        if not readonly:
            os.chmod(self.ledger, 0o600)
        return out

    def kv_get(self, key: str):
        out = self.ledger_sql(f"SELECT value FROM kv WHERE ns='claude' AND key='{key}';", "kvget", readonly=True)
        return json.loads(out) if out.strip() else None

    def daemon_events(self, since: int = 0):
        text = (self.logs / "daemon.log").read_text()[since:]
        return [json.loads(line) for line in text.splitlines() if line.startswith("{")]

    def baseline_check(self) -> None:
        log("phase 5b: baseline at a real head read before any send")
        wait_until(lambda: any(e.get("msg") == "claude_delivery_scope_baselined" and e.get("scope") == self.binding_key
                               and e.get("generation") == self.generation for e in self.daemon_events()),
                   "claude_delivery_scope_baselined", timeout=60)
        stored = self.kv_get(SCOPE_KEY + self.binding_key)
        if not stored or stored.get("generation") != self.generation:
            raise Fail(f"no stored baseline for the admitted scope: {stored}")
        # The same real CLI read the adapter runs, against the real hub.
        rc, out = self.cli(["--net", "claude-rx", "inbox", "--json", "--no-display-proof", "--since", "head"],
                           name="head-read")
        if rc != 0:
            raise Fail(f"real inbox --since head failed: {out}")
        head = parse_json_output(out)
        rc, out2 = self.cli(["--net", "claude-rx", "inbox", "--json", "--no-display-proof", "--limit", "8"],
                            name="backlog-read")
        if rc != 0:
            raise Fail(f"backlog inbox read failed: {out2}")
        backlog = [e for e in parse_json_output(out2)["events"]
                   if e.get("data", {}).get("delivery_id") == self.backlog_delivery]
        if len(backlog) != 1:
            raise Fail("the pre-admission backlog is not readable in the inbox")
        b, s_, h = (cursor_seq(c) for c in (backlog[0]["cursor"], stored["cursor"], head["next_cursor"]))
        if head.get("events") != [] or not (b[0] == s_[0] == h[0] and b[1] <= s_[1] <= h[1]):
            raise Fail(f"baseline cursor {stored['cursor']} is not a real head past the backlog "
                       f"{backlog[0]['cursor']} (head now {head['next_cursor']})")
        if any(self.backlog_delivery in json.dumps(e) for e in self.daemon_events()):
            raise Fail("the pre-admission backlog delivery reached the daemon")
        self.evidence_write("33-baseline.json", json.dumps({
            "baselined": next(e for e in self.daemon_events() if e.get("msg") == "claude_delivery_scope_baselined"),
            "stored_cursor": stored["cursor"], "cli_head_next_cursor": head["next_cursor"],
            "cli_head_events": head["events"], "backlog_cursor": backlog[0]["cursor"],
            "backlog_delivery_id": self.backlog_delivery}, indent=2))
        status = wait_until(lambda: (lambda d: d if d.get("mode") == "canary" else None)(
            parse_json_output(self.cli(["--net", "claude-rx", "claude", "status", "--json"], timeout=30)[1])),
            "claude status mode canary", timeout=40, interval=2.0)
        self.record("no_provision", check_no_provision(self.claude_environ, status))
        rc, out = self.cli(["--net", "claude-rx", "claude", "status", "--json"], name="status-canary")
        self.evidence_write("34-status-canary.json", out)
        self.record("baseline", {"stored_cursor": stored["cursor"], "head_cursor": head["next_cursor"],
                                 "backlog_cursor": backlog[0]["cursor"]})
        log(f"baselined at {stored['cursor']} (backlog {backlog[0]['cursor']}); status canary")

    def stop_managed_runtime(self) -> None:
        if (self.run / "runtime-pid.json").exists():
            rc, out = self.in_netns(f"{self.bin}/systemctl --user stop -- {self.unit}", name="stop-managed")
            if rc or (self.run / "runtime-pid.json").exists():
                raise Fail(f"private scheduler runtime did not stop: {out}")

    def start_daemon(self, seam: bool = False, tag: str = "daemon", kill_switch: bool = False) -> None:
        if self.daemon_window:
            self.stop_daemon()
        env = " ".join(daemon_env(self.home, self.cfg, self.runtime / "vecnet", seam=seam, kill_switch=kill_switch))
        script = self.run / f"{tag}.sh"
        script.write_text(f"cd {self.runtime}/connectors/launcher\n"
                          f"echo $$ > {self.logs / (tag + '.pid')}\n"
                          f"exec env {env} {self.node_bin} --disable-warning=ExperimentalWarning "
                          f"--experimental-strip-types src/codex/delivery-main.ts\n")
        script.chmod(0o755)
        rcfile = self.logs / f"{tag}.rc"
        rcfile.unlink(missing_ok=True)
        log_path = self.logs / "daemon.log"
        self.log_marks[tag] = len(log_path.read_text()) if log_path.exists() else 0
        self.tmux("new-window", "-d", "-n", tag,
                  f"sh -c 'sh {script} >> {self.logs}/daemon.log 2>&1; echo $? > {rcfile}'")
        self.daemon_window = tag
        if not seam:
            wait_until(lambda: (self.logs / "daemon.log").exists(), "daemon log", timeout=20)

    def stop_daemon(self) -> None:
        if not self.daemon_window:
            return
        tag, self.daemon_window = self.daemon_window, None
        pidfile = self.logs / f"{tag}.pid"
        pid = None
        # An exit receipt (e.g. from the crash seam) means the pid is stale.
        if pidfile.exists() and not (self.logs / f"{tag}.rc").exists():
            pid = int(pidfile.read_text().strip() or 0)
        # The window kill alone is not enough: node survives the pane. Stop
        # the recorded process itself (SIGTERM drains the host and closes the
        # sqlite ledger cleanly), then force if needed.
        self.tmux("kill-window", "-t", f"svc:{tag}", check=False)
        if pid and pid > 1:
            try:
                os.kill(pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            deadline = time.monotonic() + 10
            while time.monotonic() < deadline:
                try:
                    os.kill(pid, 0)
                except ProcessLookupError:
                    break
                time.sleep(0.3)
            else:
                try:
                    os.kill(pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
        time.sleep(1.0)

    def launch_claude(self) -> None:
        env = " ".join(claude_launch_env(self.home, self.cconfig, self.sockdir, self.run, self.cfg))
        cmd = (f"sh -c 'env {env} {self.claude_bin} --messaging-socket-path "
               f"{self.sockdir}/claude.sock; echo EXIT=$?; sleep 100000'")
        self.tmux("new-session", "-d", "-s", self.session, "-x", "200", "-y", "50",
                  "-c", str(self.workdir), "-e", f"TMUX_TMPDIR={self.tdir}", cmd)
        self.start_frame_capture()
        wait_until(lambda: "Yes, I trust this folder" in self.pane_text(),
                   "folder trust dialog", timeout=120)
        self.select_option("Down", r"❯\s*Yes, I trust this folder", "folder trust selection")
        wait_until(lambda: "Do you want to use this API key" in self.pane_text(),
                   "api key dialog", timeout=40)
        self.select_option("Up", r"❯\s*Yes\b", "api key selection")
        wait_until(self._prompt_ready, "idle prompt box", timeout=60)
        log(f"claude session {self.session} up (cwd {self.workdir})")

    def select_option(self, key: str, selected: str, what: str) -> None:
        """Move a dialog cursor, and confirm only once the selection is drawn:
        a keypress sent before the dialog is interactive is otherwise lost."""
        pattern = re.compile(selected)
        def moved():
            if pattern.search(self.pane_text()):
                return True
            self.tmux("send-keys", "-t", self.session, key)
            time.sleep(0.6)
            return bool(pattern.search(self.pane_text()))
        wait_until(moved, what, timeout=20, interval=0.4)
        self.tmux("send-keys", "-t", self.session, "Enter")

    def _prompt_ready(self) -> bool:
        text = self.pane_text()
        return ("❯" in text and "trust this folder" not in text
                and "Do you want to use this API key" not in text
                and "Select login method" not in text)

    def send_prompt(self, prompt):
        self.tmux("send-keys", "-t", self.session, "-l", prompt)
        time.sleep(0.4)
        self.tmux("send-keys", "-t", self.session, "Enter")

    def drive_first_prompt(self) -> None:
        log("phase 5: ready prompt -> verify automatic registration and admission")
        self.send_prompt("READY: acknowledge and stay idle.")
        wait_until(lambda: self._stub_request_matching("READY"), "stub READY request", timeout=60)
        wait_until(lambda: "READY: e2e receiver idle" in self.pane_text(),
                   "stub READY reply in pane", timeout=40)
        binding = wait_until(self._admitted_binding, "registered + admitted binding",
                             timeout=90, interval=2.0)
        self.binding_key = binding["binding_key"]
        self.generation = binding["generation"]
        self.record("admission", {k: binding.get(k) for k in
                                  ("binding_key", "generation", "state", "admission")})
        rc, snap = self.cli(["claude", "runtime-snapshot", "--json"], name="snapshot")
        self.evidence_write("32-runtime-snapshot.json", snap)
        files = sorted((self.cconfig / "projects").rglob("*.jsonl"),
                       key=lambda p: p.stat().st_mtime)
        if not files:
            raise Fail("no transcript .jsonl under the config root")
        self.transcript = files[-1]
        log(f"binding admitted: {self.binding_key[:12]}… gen {self.generation[:8]}…")
        log(f"transcript: {self.transcript}")

    def _stub_request_matching(self, needle: str):
        try:
            for line in (self.logs / "stub-observed.jsonl").read_text().splitlines():
                if needle in line:
                    return line
        except FileNotFoundError:
            pass
        return None

    def _admitted_binding(self):
        rc, out = self.cli(["claude", "runtime-snapshot", "--json"], timeout=30)
        if rc != 0:
            return None
        try:
            snap = json.loads(out)
        except json.JSONDecodeError:
            return None
        for b in snap.get("bindings", []):
            a = b.get("admission") or {}
            if b.get("state") == "registered" and a.get("live") and a.get("policy") \
                    and a.get("version_qualified") and a.get("canary") and a.get("witness") \
                    and a.get("build_check") == "disabled" and b.get("scope", {}).get("caller") == self.claude_peer:
                return b
        return None

    # ------------------------------------------------------ frame capture ---

    def start_frame_capture(self) -> None:
        def loop():
            i = 0
            while not self.frame_stop.is_set():
                text = self.tmux("capture-pane", "-t", self.session, "-p", "-S", "-2000")
                (self.frames / f"frame-{i:04d}.txt").write_text(text)
                self.frame_index.append({"n": i, "t": round(time.time() - self.started, 1),
                                         "file": f"frame-{i:04d}.txt"})
                i += 1
                time.sleep(0.6)
        def capture():
            try:
                loop()
            except Exception as e:
                self.frame_error = e
        self.frame_thread = threading.Thread(target=capture, daemon=True)
        self.frame_thread.start()

    def stop_frame_capture(self):
        self.frame_stop.set()
        if self.frame_thread:
            self.frame_thread.join()
        if self.frame_error:
            raise Fail(f"frame capture failed: {self.frame_error}")

    # ---------------------------------------------------- the proof chain ---

    def positive_delivery(self) -> None:
        log("phase 6: positive delivery (wake pointer, fetch, transcript)")
        self.marker = "WAKE-E2E-" + secrets.token_hex(8)
        rc, out = self.cli(["--net", "sender", "send", self.claude_peer,
                            f"{self.marker}: the canary chain works end to end.",
                            "--wake", "--json"], name="send-wake")
        if rc != 0:
            raise Fail(f"send failed: {out}")
        self.evidence_write("40-send-wake.json", out)
        self.send_resp = parse_json_output(out)
        self.delivery_id = self.send_resp["deliveries"][0]["delivery_id"]
        self.message_id = self.send_resp["message_id"]
        self.hub_query("41-hub-delivery.json",
                       f"SELECT dly_id, recipient, state FROM deliveries "
                       f"WHERE dly_id = '{self.delivery_id}'")
        wait_until(lambda: self._daemon_line("claude_delivery_disposition", self.delivery_id),
                   "daemon disposition line", timeout=90)
        wait_until(lambda: PREVIEW in self.pane_text(),
                   "native pointer preview in pane", timeout=30)
        wait_until(lambda: self.marker in self.pane_text(), "fetched marker text in pane", timeout=60)
        wait_until(lambda: self._stub_request_matching(self.marker) is not None,
                   "stub received the fetched inbox content (tool result)", timeout=60)
        rc, out = self.cli(["--net", "claude-rx", "claude", "deliveries", "--json"],
                           name="deliveries-pos")
        self.evidence_write("62-claude-deliveries.json", out)
        doc = parse_json_output(out)
        claim = next(c for c in doc["deliveries"] if c["delivery_id"] == self.delivery_id)
        if claim["state"] != "released" or claim["disposition"] != "attempted":
            raise Fail(f"claim not released/attempted: {claim}")
        if doc.get("pending_count", 0) != 0:
            raise Fail(f"unexpected pending after clean delivery: {doc.get('pending_count')}")
        self.record("positive", {"delivery_id": self.delivery_id,
                                 "message_id": self.message_id,
                                 "state": claim["state"],
                                 "disposition": claim["disposition"]})
        log(f"positive chain complete: {self.delivery_id} released/attempted")

    def _daemon_line(self, event: str, delivery: str = None):
        try:
            text = (self.logs / "daemon.log").read_text()
        except FileNotFoundError:
            return None
        for line in text.splitlines():
            if f'"{event}"' in line and (delivery is None or delivery in line):
                return line
        return None

    def hub_query(self, name: str, sql: str) -> None:
        rc, out = self.in_netns(f"sqlite3 -json {self.hubdir}/hub.db {shlex.quote(sql)}",
                                name="hubq")
        if rc != 0:
            raise Fail(f"hub query {name} failed (rc={rc}): {out}")
        self.evidence_write(name, (out.strip() or "[]") + "\n")

    def _transcript_rows(self, needle: str) -> int:
        try:
            return self.transcript.read_text().count(needle)
        except (FileNotFoundError, AttributeError):
            return 0

    def finalize_proof(self):
        raw = self.logs.joinpath("daemon.log").read_text().splitlines()
        events = [json.loads(line) for line in raw if line.startswith("{")]
        lifecycle = ordered_lifecycle(events, self.binding_key, self.generation, self.delivery_id)
        if (lifecycle[-1].get("exit"), lifecycle[-1].get("state"),
                lifecycle[-1].get("disposition")) != (0, "released", "attempted"):
            raise Fail("wrong exit disposition in delivery lifecycle")
        rows = [json.loads(line) for line in self.transcript.read_text().splitlines()]
        selected, tool_id = transcript_proof(rows, self.message_id, self.fetch_cmd, self.marker)
        self.evidence_write("51-daemon-chain.txt", "".join(json.dumps(r) + "\n" for r in lifecycle))
        self.evidence_write("60-transcript-rows.json", json.dumps(selected, indent=2))
        frames = [self.frames / f["file"] for f in self.frame_index]
        preview_counts = [p.read_text().count(PREVIEW) for p in frames]
        if max(preview_counts, default=0) != 1:
            raise Fail("expected one collapsed pointer preview and no additional previews in any frame")
        backlog = self.results["backlog"] = {"delivery_id": self.backlog_delivery,
                                             "message_id": self.backlog_message}
        check_backlog(frames, rows, events, self.backlog_message, self.backlog_delivery)
        backlog["pointer_frames"] = 0
        attempt_at, done_at = (lifecycle[i]["at"] for i in (2, 3))
        # Observed only (not a gate): the helper holds claude-bindings for at
        # most its 1 s connect/write window; this is the host-side bound.
        window = deliver_window_ms(attempt_at, done_at)
        self.results["positive"]["deliver_window_ms"] = window
        self.results["positive"]["deliver_window_within_hook_budget"] = window < 1500
        for name in ("hold", "crash", "cap", "canary_off", "kill_switch"):
            control = self.results[name]
            message_id = control["message_id"]
            marker = getattr(self, {"canary_off": "off_marker", "kill_switch": "kill_marker"}.get(name, f"{name}_marker"))
            verify_control_frames(frames, message_id, marker)
            if any(marker in json.dumps(row) or has_pointer(row.get("content"), message_id)
                   or has_pointer((row.get("origin") or {}).get("body"), message_id)
                   for row in rows):
                raise Fail(f"control {message_id} reached the transcript")
            control["pointer_frames"] = 0
        self.results["positive"].update(enqueues=1, fetch_pairs=1, tool_use_id=tool_id,
                                        lifecycle=[r["msg"] for r in lifecycle])

    # ---------------------------------------------------- negative controls --

    def hold_control(self) -> None:
        log("phase 7: hold stays inbox-only, never acked")
        self.hold_marker = "HOLD-E2E-" + secrets.token_hex(8)
        rc, out = self.cli(["--net", "sender", "send", self.claude_peer,
                            f"{self.hold_marker}: must stay inbox only.", "--hold", "--json"],
                           name="send-hold")
        if rc != 0:
            raise Fail(f"hold send failed: {out}")
        self.evidence_write("70-hold-send.json", out)
        receipt = parse_json_output(out)
        self.hold_delivery = receipt["deliveries"][0]["delivery_id"]
        wait_until(lambda: self._daemon_line("claude_delivery_claim_held", self.hold_delivery),
                   "hold quarantined in ledger", timeout=90)
        time.sleep(2)
        if self.hold_marker in self.pane_text():
            raise Fail("hold marker leaked into the native pane")
        if self._transcript_rows(self.hold_marker) != 0:
            raise Fail("hold marker reached the transcript")
        rc, out = self.cli(["--net", "claude-rx", "claude", "deliveries", "--json"],
                           name="deliveries-hold")
        doc = parse_json_output(out)
        claim = next(c for c in doc["deliveries"] if c["delivery_id"] == self.hold_delivery)
        if claim["state"] != "held" or claim["disposition"] != "held":
            raise Fail(f"hold claim not held: {claim}")
        rc, out = self.cli(["--net", "claude-rx", "inbox", "--json", "--limit", "8"],
                           name="inbox-hold")
        if rc != 0 or self.hold_marker not in out:
            raise Fail(f"hold message not readable in inbox: rc={rc} out={out[:300]!r}")
        self.evidence_write("71-hold-inbox.json", out)
        self.record("hold", {"delivery_id": self.hold_delivery, "message_id": receipt["message_id"], "state": claim["state"],
                             "disposition": claim["disposition"]})
        log(f"hold control complete: {self.hold_delivery} held/inbox-only")

    def crash_control(self) -> None:
        log("phase 8: SIGKILL between attempting and write -> uncertain, no resend")
        # Stop the live daemon first: a running watch would deliver the crash
        # message before the seam daemon can take it.
        self.stop_daemon()
        self.crash_marker = "CRASH-E2E-" + secrets.token_hex(8)
        rc, out = self.cli(["--net", "sender", "send", self.claude_peer,
                            f"{self.crash_marker}: crash window control.", "--wake", "--json"],
                           name="send-crash")
        if rc != 0:
            raise Fail(f"crash send failed: {out}")
        self.evidence_write("80-crash-send.json", out)
        receipt = parse_json_output(out)
        self.crash_delivery = receipt["deliveries"][0]["delivery_id"]
        self.start_daemon(seam=True, tag="daemon-seam")
        rcfile = self.logs / "daemon-seam.rc"
        wait_until(lambda: rcfile.exists(), "seam daemon self-kill", timeout=120)
        rc_text = rcfile.read_text().strip()
        if rc_text != "137":
            raise Fail(f"crash seam expected exit 137, got {rc_text}")
        attempting = [l for l in (self.logs / "daemon.log").read_text().splitlines()
                      if "claude_delivery_claim_attempting" in l and self.crash_delivery in l]
        if not attempting:
            raise Fail("no attempting line for the crash delivery")
        self.evidence_write("81-crash-attempting.txt",
                            attempting[-1] + f"\ndaemon exit rc={rc_text}\n")
        rc, out = self.cli(["--net", "claude-rx", "claude", "deliveries", "--json"],
                           name="deliveries-crash1")
        doc = parse_json_output(out)
        claim = next(c for c in doc["deliveries"] if c["delivery_id"] == self.crash_delivery)
        if claim["state"] != "attempting":
            raise Fail(f"claim not attempting at crash: {claim}")
        self.start_daemon(tag="daemon2")
        wait_until(lambda: self._daemon_line("claude_delivery_uncertain_restart"),
                   "uncertain recovery line", timeout=90)
        wait_until(lambda: self._claim_state(self.crash_delivery) == "uncertain",
                   "claim uncertain after restart", timeout=60)
        if self._transcript_rows(self.crash_marker) != 0:
            raise Fail("crash marker reached the transcript despite the seam (bytes left)")
        rc, out = self.cli(["--net", "claude-rx", "claude", "deliveries", "--json"],
                           name="deliveries-crash2")
        claim1 = next(c for c in parse_json_output(out)["deliveries"]
                      if c["delivery_id"] == self.crash_delivery)
        time.sleep(13)  # one full reconcile tick: a resend would mutate the claim
        rc, out = self.cli(["--net", "claude-rx", "claude", "deliveries", "--json"],
                           name="deliveries-crash3")
        claim2 = next(c for c in parse_json_output(out)["deliveries"]
                      if c["delivery_id"] == self.crash_delivery)
        if claim2 != claim1:
            raise Fail(f"claim mutated across reconcile (resend?): {claim1} -> {claim2}")
        if self._transcript_rows(self.crash_marker) != 0:
            raise Fail("crash marker delivered after restart (resend)")
        self.evidence_write("82-crash-claim.json", json.dumps(
            {"at_crash": claim, "after_restart": claim2,
             "transcript_rows": self._transcript_rows(self.crash_marker)}, indent=2))
        self.record("crash", {"delivery_id": self.crash_delivery, "message_id": receipt["message_id"],
                              "final_state": claim2["state"],
                              "reason": claim2.get("reason")})
        log(f"crash control complete: {self.crash_delivery} uncertain, no resend")

    def _claim_state(self, delivery: str):
        rc, out = self.cli(["--net", "claude-rx", "claude", "deliveries", "--json"], timeout=30)
        if rc != 0:
            return None
        try:
            doc = json.loads(out)
        except json.JSONDecodeError:
            return None
        for c in doc.get("deliveries", []):
            if c["delivery_id"] == delivery:
                return c["state"]
        return None

    def cap_control(self) -> None:
        log("phase 9: 129th staged pending message latches blocked_pending_limit")
        self.stop_daemon()
        claim_rows = []
        now = int(time.time() * 1000)
        for i in range(128):
            claim_rows.append({
                "v": 1, "deliveryId": f"seed-{i:04d}", "messageId": "msg_" + _ulid(100 + i),
                "threadId": "thr_" + _ulid(900), "msgId": secrets.token_hex(16),
                "scope": self.binding_key, "generation": self.generation,
                "cursor": f"e1.{i + 1}", "predecessor": f"e1.{i}", "priority": "wake",
                "state": "uncertain", "attemptId": secrets.token_hex(16),
                "disposition": "uncertain", "dispositionReason": "restart_may_have_written",
                "hubAckObserved": False, "createdAt": now, "updatedAt": now})
        ledger = self.home / ".local/state/vecnet/codex/delivery.sqlite"
        if not ledger.exists():
            raise Fail(f"delivery ledger missing at {ledger}")
        values = ",".join(
            "('claude','claude-claims:claim:" + c["deliveryId"] + "','" +
            json.dumps(c).replace("'", "''") + "')" for c in claim_rows)
        seed_sql = self.run / "seed-cap.sql"
        seed_sql.write_text(f"INSERT INTO kv(ns,key,value) VALUES {values};\n")
        rc, out = self.in_netns(f"sqlite3 {ledger} < {seed_sql}", name="seed-cap")
        if rc != 0:
            raise Fail(f"seeding ledger failed: {out[:400]}")
        os.chmod(ledger, 0o600)
        self.evidence_write("91-cap-seed.json", json.dumps(
            {"seeded": len(claim_rows), "scope": self.binding_key,
             "generation": self.generation}, indent=2))
        self.cap_marker = "CAP-E2E-" + secrets.token_hex(8)
        rc, out = self.cli(["--net", "sender", "send", self.claude_peer,
                            f"{self.cap_marker}: must not be sent natively.", "--wake", "--json"],
                           name="send-cap")
        if rc != 0:
            raise Fail(f"cap send failed: {out}")
        self.evidence_write("90-cap-send.json", out)
        receipt = parse_json_output(out)
        self.cap_delivery = receipt["deliveries"][0]["delivery_id"]
        self.start_daemon(tag="daemon3")
        wait_until(lambda: self._daemon_line("claude_delivery_blocked_pending_limit"),
                   "blocked_pending_limit log line", timeout=120)
        time.sleep(3)
        rc, out = self.cli(["--net", "claude-rx", "claude", "deliveries", "--json"],
                           name="deliveries-cap")
        self.evidence_write("92-cap-deliveries.json", out)
        doc = parse_json_output(out)
        if doc.get("blocked_pending_limit") is not True:
            raise Fail(f"blocked_pending_limit not latched: {doc.get('blocked_pending_limit')}")
        if doc.get("pending_count") != 129:
            raise Fail(f"expected 129 unresolved claims, got: {doc.get('pending_count')}")
        if any(c["delivery_id"] == self.cap_delivery for c in doc.get("deliveries", [])):
            raise Fail("cap message was staged despite the latch")
        if self._transcript_rows(self.cap_marker) != 0:
            raise Fail("cap marker reached the transcript (native send happened)")
        rc, out = self.cli(["--net", "claude-rx", "claude", "status", "--json"], name="status-cap")
        self.evidence_write("93-cap-status.json", out)
        if parse_json_output(out).get("blocked_pending_limit") is not True:
            raise Fail("claude status does not report the cap latch")
        self.record("cap", {"delivery_id": self.cap_delivery, "message_id": receipt["message_id"],
                            "pending": doc.get("pending_count"),
                            "blocked": doc.get("blocked_pending_limit")})
        log(f"cap control complete: pending={doc.get('pending_count')} blocked, "
            f"{self.cap_delivery} unstaged")

    def rebaseline_control(self) -> None:
        log("phase 7b: a hub-rejected stored cursor rebaselines at the head")
        self.stop_daemon()
        before = self.kv_get(SCOPE_KEY + self.binding_key)
        if not before:
            raise Fail("no stored scope cursor to corrupt")
        rc, probe = self.cli(["--net", "claude-rx", "inbox", "--json", "--no-display-proof", "--since", FOREIGN_CURSOR],
                             name="foreign-cursor-read")
        if rc != 2 or "cursor_purged: " not in probe:
            raise Fail(f"the real hub did not refuse a foreign-epoch cursor: rc={rc} {probe[:200]!r}")
        corrupted = dict(before, cursor=FOREIGN_CURSOR)
        value = json.dumps(corrupted).replace("'", "''")
        self.ledger_sql(f"UPDATE kv SET value='{value}' WHERE ns='claude' AND key='{SCOPE_KEY}{self.binding_key}';",
                        "corrupt-cursor")
        if self.kv_get(SCOPE_KEY + self.binding_key).get("cursor") != FOREIGN_CURSOR:
            raise Fail("cursor corruption did not land")
        self.start_daemon(tag="daemon-rb")
        mark = self.log_marks["daemon-rb"]
        wait_until(lambda: any(e.get("msg") == "claude_delivery_scope_baselined" for e in self.daemon_events(mark)),
                   "rebaseline after a rejected cursor", timeout=60)
        time.sleep(13)  # one more reload tick: a retry loop would log again
        events = self.daemon_events(mark)
        picked = check_rebaseline(events, self.binding_key, self.generation)
        after = self.kv_get(SCOPE_KEY + self.binding_key)
        if after.get("cursor") == FOREIGN_CURSOR or cursor_seq(after["cursor"])[0] != cursor_seq(before["cursor"])[0]:
            raise Fail(f"rebaseline did not store a real head cursor: {after}")
        self.evidence_write("75-rebaseline.json", json.dumps({
            "stored_before": before["cursor"], "corrupted_to": FOREIGN_CURSOR,
            "real_cli_refusal": probe.strip().splitlines()[-1], "stored_after": after["cursor"],
            "events": [e for e in events if e.get("msg") in
                       ("claude_delivery_cursor_rejected", "claude_delivery_scope_baselined")]}, indent=2))
        self.record("rebaseline", {"events": picked, "stored_after": after["cursor"]})
        log(f"rebaseline control complete: {FOREIGN_CURSOR} -> {after['cursor']}")

    def canary_off_control(self) -> None:
        log("phase 9b: canary disable -> legacy, no claim, reserved rows drained")
        self.stop_daemon()
        # Drop the harness's own cap seeds so the drain and no-claim checks are
        # not vacuous behind the pending latch, then stage one reserved row.
        self.ledger_sql(f"DELETE FROM kv WHERE ns='claude' AND key LIKE '{CLAIM_KEY}seed-%';", "unseed-cap")
        now = int(time.time() * 1000)
        self.fence_delivery = "fence-0000"
        row = {"v": 1, "deliveryId": self.fence_delivery, "messageId": "msg_" + _ulid(5000),
               "threadId": "thr_" + _ulid(5001), "msgId": secrets.token_hex(16),
               "scope": self.binding_key, "generation": self.generation, "cursor": "e1.1",
               "predecessor": "e1.0", "priority": "wake", "state": "reserved",
               "attemptId": secrets.token_hex(16), "hubAckObserved": False, "createdAt": now, "updatedAt": now}
        value = json.dumps(row).replace("'", "''")
        rc, out = self.cli(["--net", "claude-rx", "claude", "canary", "disable"], name="canary-disable")
        if rc != 0:
            raise Fail(f"claude canary disable failed: {out}")
        self.ledger_sql(f"INSERT INTO kv(ns,key,value) VALUES ('claude','{CLAIM_KEY}{self.fence_delivery}','{value}');",
                        "seed-reserved")
        self.start_daemon(tag="daemon4")
        mark = self.log_marks["daemon4"]
        wait_until(lambda: any(e.get("msg") == "claude_delivery_claim_fenced" and e.get("deliveryId") == self.fence_delivery
                               for e in self.daemon_events(mark)), "reserved row fenced", timeout=60)
        self.off_marker = "OFF-E2E-" + secrets.token_hex(8)
        rc, out = self.cli(["--net", "sender", "send", self.claude_peer,
                            f"{self.off_marker}: canary is off.", "--wake", "--json"], name="send-off")
        if rc != 0:
            raise Fail(f"canary-off send failed: {out}")
        self.evidence_write("94-off-send.json", out)
        receipt = parse_json_output(out)
        self.off_delivery = receipt["deliveries"][0]["delivery_id"]
        time.sleep(13)  # a full reload tick after the wake
        events = self.daemon_events(mark)
        if any(self.off_delivery in json.dumps(e) for e in events):
            raise Fail("the canary-off wake reached the daemon's claim lifecycle")
        if any(e.get("msg") == ADMISSION for e in events):
            raise Fail("a scope was admitted with the canary disabled")
        rc, dout = self.cli(["--net", "claude-rx", "claude", "deliveries", "--json"], name="deliveries-off")
        rc2, sout = self.cli(["--net", "claude-rx", "claude", "status", "--json"], name="status-off")
        self.evidence_write("95-off-deliveries.json", dout)
        self.evidence_write("96-off-status.json", sout)
        result = check_canary_off(parse_json_output(dout), parse_json_output(sout), self.off_delivery, self.fence_delivery)
        self.evidence_write("97-off-fence.txt", "".join(json.dumps(e) + "\n" for e in events
                                                         if e.get("msg") == "claude_delivery_claim_fenced"))
        self.record("canary_off", result | {"delivery_id": self.off_delivery, "message_id": receipt["message_id"]})
        log(f"canary-off control complete: {self.off_delivery} unclaimed, {self.fence_delivery} fenced")

    def kill_switch_control(self) -> None:
        log("phase 9c: VECNET_CLAUDE_DELIVERY_CANARY=0 constructs no adapter")
        self.stop_daemon()
        rc, out = self.cli(["--net", "claude-rx", "claude", "canary", "enable"], name="canary-reenable")
        if rc != 0:
            raise Fail(f"canary re-enable failed: {out}")
        self.start_daemon(tag="daemon5", kill_switch=True)
        mark = self.log_marks["daemon5"]
        self.kill_marker = "KILL-E2E-" + secrets.token_hex(8)
        rc, out = self.cli(["--net", "sender", "send", self.claude_peer,
                            f"{self.kill_marker}: kill switch on.", "--wake", "--json"], name="send-kill")
        if rc != 0:
            raise Fail(f"kill-switch send failed: {out}")
        self.evidence_write("100-kill-send.json", out)
        receipt = parse_json_output(out)
        self.kill_delivery = receipt["deliveries"][0]["delivery_id"]
        status = wait_until(lambda: (lambda d: d if d.get("owner") == "registered" else None)(
            parse_json_output(self.cli(["--net", "claude-rx", "claude", "status", "--json"], timeout=30)[1])),
            "kill-switch host owns the status", timeout=40, interval=2.0)
        time.sleep(13)
        events = self.daemon_events(mark)
        native = [e for e in events if str(e.get("msg", "")).startswith("claude_delivery_")]
        if native:
            raise Fail(f"the kill-switch daemon ran a delivery adapter: {native[:3]}")
        rc, sout = self.cli(["--net", "claude-rx", "claude", "status", "--json"], name="status-kill")
        status = parse_json_output(sout)
        if (status.get("mode"), status.get("canary_opt_in"), status.get("build_check")) != ("legacy", True, "disabled") \
                or "has not admitted" not in status.get("reason", ""):
            raise Fail(f"kill-switch status is not legacy with an unadmitted opt-in: {status}")
        self.evidence_write("101-kill-status.json", sout)
        self.evidence_write("102-kill-env.txt", "\n".join(daemon_env(self.home, self.cfg, self.runtime / "vecnet",
                                                                      kill_switch=True)) + "\n")
        self.record("kill_switch", {"delivery_id": self.kill_delivery, "message_id": receipt["message_id"],
                                    "adapter_events": 0, "mode": status["mode"], "canary_opt_in": True})
        log(f"kill-switch control complete: {self.kill_delivery} untouched, mode legacy")

    def no_ack_evidence(self) -> None:
        log("phase 10: hub shows no ack for any delivered cursor")
        self.hub_query("73-hub-acks.json", "SELECT * FROM acks")
        acks = (self.evidence / "73-hub-acks.json").read_text().strip()
        if acks not in ("", "[]"):
            raise Fail(f"unexpected ack rows in hub: {acks}")
        expected = {self.backlog_delivery, self.delivery_id, self.hold_delivery, self.crash_delivery,
                    self.cap_delivery, self.off_delivery, self.kill_delivery}
        self.hub_query("74-hub-delivery-states.json",
                       "SELECT dly_id, recipient, state FROM deliveries WHERE dly_id IN ("
                       + ",".join(f"'{d}'" for d in sorted(expected)) + ")")
        states = json.loads((self.evidence / "74-hub-delivery-states.json").read_text())
        if (len(states) != len(expected) or {row.get("dly_id") for row in states} != expected
                or any(row.get("state") != "queued" for row in states)):
            raise Fail(f"expected all {len(expected)} hub deliveries to remain queued: {states}")
        self.record("no_ack", {"acks": 0, "deliveries": states})

    # ------------------------------------------------------------ teardown --

    def teardown(self, keep_evidence) -> None:
        log("phase 11: verified teardown")
        errors = []
        gone = ""
        # Stop only this run's tracked daemon; never sweep host processes.
        try:
            self.stop_daemon()
            self.stop_managed_runtime()
        except Exception as e:  # noqa: BLE001
            errors.append(f"daemon teardown: {e}")
        try:
            self.tmux("kill-session", "-t", self.session, check=False)
            time.sleep(1)
            listed = self.tmux("list-sessions", check=False)
            if self.session in listed:
                errors.append(f"session {self.session} still listed: {listed!r}")
            self.tmux("kill-server", check=False)
            time.sleep(1)
            env = dict(os.environ, TMUX_TMPDIR=str(self.tdir))
            probe = subprocess.run(["tmux", "-L", self.sock_name, "list-sessions"],
                                   capture_output=True, text=True, env=env)
            gone = (probe.stderr or probe.stdout).strip()
            if probe.returncode == 0 or "no server running" not in gone:
                errors.append(f"private tmux server still up: {gone!r}")
        except Exception as e:  # noqa: BLE001
            errors.append(f"tmux teardown: {e}")
        names = sh(["tmux", "list-sessions", "-F", "#{session_name}"]).stdout.splitlines()
        stray = [name for name in names if name.startswith("vecnet-test-")]
        if stray:
            errors.append("stray vecnet-test-* sessions on the default server")
        for src, dst in (("logs/daemon.log", "50-daemon.log"),
                         ("logs/hub.log", "11-hub-serve.log"),
                         ("logs/stub-observed.jsonl", "31-stub-observed.jsonl"),
                         ("logs/proxy-observed.jsonl", "12-proxy-observed.jsonl"),
                         ("logs/send-wake.out", "40b-send-wake-cli.txt"),
                         ("logs/head-read.out", "33b-head-read-cli.txt")):
            s = self.run / src
            if s.exists():
                shutil.copyfile(s, self.evidence / dst)
        if self.frames.exists():
            (self.frames / "index.json").write_text(json.dumps(self.frame_index))
        if errors:
            raise Fail("teardown errors: " + "; ".join(errors))
        disposition = dispose_run(self.run, keep_evidence)
        self.record("teardown", {"private_server_stopped": True, "default_server_strays": 0,
                                 "raw_disposition": disposition})
        self.evidence.mkdir(exist_ok=True)
        self.evidence_write("99-teardown.txt", f"private server: {gone}\n"
                            f"default server vecnet-test-* sessions: {len(stray)}\n"
                            f"raw run state (hub DB, profiles, credentials, cert keys): {disposition}\n")
        log(f"teardown verified; raw run state {disposition}")

    def finish(self) -> dict:
        self.record("finished", time.strftime("%Y-%m-%dT%H:%M:%S%z"))
        meta = {"run_dir": str(self.run), "nonce": self.nonce,
                "results": self.results, "frames": len(self.frame_index)}
        self.evidence.mkdir(exist_ok=True)
        (self.evidence / "98-summary.json").write_text(json.dumps(meta, indent=2))
        return meta


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--repo", default=str(Path(__file__).resolve().parents[2]))
    ap.add_argument("--keep-evidence", action="store_true",
                    help="retain raw lab state (contains credentials) under the private run directory; never publish it")
    args = ap.parse_args()
    repo = Path(args.repo).resolve()
    base = Path("/tmp/ve2e-runs")
    base.mkdir(parents=True, exist_ok=True)
    run = base / (time.strftime("%Y%m%d-%H%M%S") + "-" + secrets.token_hex(3))
    run.mkdir(mode=0o700)
    h = Harness(repo, run)
    exit_code = 0
    try:
        h.preflight()
        h.lab_stack()
        h.hub_up()
        h.enrol()
        h.claude_session_up()
        h.positive_delivery()
        h.hold_control()
        h.rebaseline_control()
        h.crash_control()
        h.cap_control()
        h.canary_off_control()
        h.kill_switch_control()
        h.no_ack_evidence()
        h.stop_daemon()
        h.stop_frame_capture()
        h.finalize_proof()
    except Exception as e:  # noqa: BLE001
        import traceback
        log(f"FAIL: {type(e).__name__}: {e}")
        traceback.print_exc()
        exit_code = 1
    finally:
        try:
            h.stop_frame_capture()
        except Fail as e:
            log(f"CAPTURE FAIL: {e}")
            exit_code = exit_code or 1
        try:
            h.teardown(args.keep_evidence)
        except Exception as e:
            log(f"TEARDOWN FAIL: {e}")
            exit_code = exit_code or 2
        meta = h.finish()
        print(json.dumps({"run_dir": meta["run_dir"], "exit": exit_code,
                          "results": meta["results"]}, indent=2))
    return exit_code


if __name__ == "__main__":
    sys.exit(main())
