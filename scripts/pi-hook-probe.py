#!/usr/bin/env python3
"""End-to-end probe: real pi (RPC mode) + the extension `vecnet hook install
--client pi` writes + this checkout's vecnet against a disposable loopback hub.

Everything is disposable and isolated: a temp lab directory holds the built
binaries, the hub data, every vecnet config, and pi's agent directory
(PI_CODING_AGENT_DIR), so the operator's own pi settings, extensions and
credentials are never read. pi talks to a loopback OpenAI-compatible stub
model that records every request; the probe checks what the model was sent.

Steps:
  1. build vecnet + vecnet-hub, start the hub, enrol a receiver and a sender
  2. install the pi extension (project-local) for the receiver
  3. sender sends message A; start `pi --mode rpc` in the workspace and
     prompt it. The extension's before_agent_start hook must bind the profile
     and the first model request must carry message A in the vecnet frame.
     (The first-ever run of a fresh profile only reports a backlog count, so
     the probe primes the cursor with one prompt before sending A.)
  4. during a turn whose stub reply is a bash tool call, the sender sends
     message B; the post-tool hook must steer B into the next model request
     of the same turn.
  5. the bash tool sees PI_SESSION_ID / PI_SESSION_FILE equal to the session
     the hook bound (confirms the env vars and the identity agree).

Usage: python3 scripts/pi-hook-probe.py [--keep]
Exit 0 when every step passes. Requires go, git, node and pi on PATH.
"""
import http.server
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import threading
import time

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
KEEP = "--keep" in sys.argv


class Fail(Exception):
    pass


results = []


def check(name, ok, detail=""):
    results.append((name, bool(ok), detail))
    print(("PASS " if ok else "FAIL ") + name + (("  " + detail) if detail else ""), flush=True)
    if not ok:
        raise Fail(name)


# ---------------------------------------------------------------- stub model
class Stub:
    """OpenAI chat-completions stub. Each request is recorded. The reply is
    scripted per turn: when the last message is a user/custom message and a
    `tool` command is queued, reply with one bash tool call; otherwise reply
    with plain text."""

    def __init__(self):
        self.requests = []
        self.tool_queue = []  # bash commands to issue, one per turn start
        # Seconds to hold the reply to a tool result: model latency, which
        # lets the post-tool hook's steered message land inside the turn.
        self.tool_reply_delay = 0
        self.lock = threading.Lock()
        stub = self

        class H(http.server.BaseHTTPRequestHandler):
            def log_message(self, *a):
                pass

            def do_POST(self):
                n = int(self.headers.get("content-length", "0"))
                body = json.loads(self.rfile.read(n) or b"{}")
                with stub.lock:
                    stub.requests.append(body)
                msgs = body.get("messages", [])
                last = msgs[-1] if msgs else {}
                reply = {"role": "assistant", "content": "ok"}
                finish = "stop"
                if last.get("role") != "tool":
                    with stub.lock:
                        cmd = stub.tool_queue.pop(0) if stub.tool_queue else None
                    if cmd:
                        reply = {"role": "assistant", "content": None, "tool_calls": [{
                            "id": "call_%d" % len(stub.requests), "type": "function",
                            "function": {"name": "bash", "arguments": json.dumps({"command": cmd})}}]}
                        finish = "tool_calls"
                elif stub.tool_reply_delay:
                    time.sleep(stub.tool_reply_delay)
                if body.get("stream"):
                    self.send_response(200)
                    self.send_header("content-type", "text/event-stream")
                    self.end_headers()
                    delta = dict(reply)
                    if delta.get("tool_calls"):
                        delta["tool_calls"] = [dict(tc, index=0) for tc in delta["tool_calls"]]
                    chunks = [
                        {"id": "x", "object": "chat.completion.chunk", "model": "stub",
                         "choices": [{"index": 0, "delta": delta, "finish_reason": None}]},
                        {"id": "x", "object": "chat.completion.chunk", "model": "stub",
                         "choices": [{"index": 0, "delta": {}, "finish_reason": finish}],
                         "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}},
                    ]
                    for c in chunks:
                        self.wfile.write(b"data: " + json.dumps(c).encode() + b"\n\n")
                    self.wfile.write(b"data: [DONE]\n\n")
                else:
                    out = json.dumps({"id": "x", "object": "chat.completion", "model": "stub",
                                      "choices": [{"index": 0, "message": reply, "finish_reason": finish}],
                                      "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}}).encode()
                    self.send_response(200)
                    self.send_header("content-type", "application/json")
                    self.send_header("content-length", str(len(out)))
                    self.end_headers()
                    self.wfile.write(out)

        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), H)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.url = "http://127.0.0.1:%d/v1" % self.server.server_address[1]

    def text_of(self, i):
        """All message text of request i, flattened."""
        out = []
        for m in self.requests[i].get("messages", []):
            c = m.get("content")
            if isinstance(c, str):
                out.append(c)
            elif isinstance(c, list):
                out.extend(p.get("text", "") for p in c if isinstance(p, dict))
        return "\n".join(out)


# ---------------------------------------------------------------- pi RPC
class Pi:
    def __init__(self, env, cwd, log):
        self.proc = subprocess.Popen(
            ["pi", "--mode", "rpc", "--offline", "--approve", "--provider", "stub", "--model", "stub-model",
             "--session-dir", os.path.join(env["PI_CODING_AGENT_DIR"], "sessions")],
            cwd=cwd, env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=open(log, "w"))
        self.events = []
        self.cv = threading.Condition()
        threading.Thread(target=self._read, daemon=True).start()
        self.n = 0

    def _read(self):
        buf = b""
        while True:
            chunk = self.proc.stdout.read1(65536)
            if not chunk:
                break
            buf += chunk
            while b"\n" in buf:
                line, buf = buf.split(b"\n", 1)
                try:
                    ev = json.loads(line.rstrip(b"\r"))
                except ValueError:
                    continue
                with self.cv:
                    self.events.append(ev)
                    self.cv.notify_all()

    def send(self, cmd):
        self.n += 1
        cmd = dict(cmd, id="c%d" % self.n)
        self.proc.stdin.write((json.dumps(cmd) + "\n").encode())
        self.proc.stdin.flush()
        return self.wait(lambda e: e.get("type") == "response" and e.get("id") == cmd["id"], 30)

    def wait(self, pred, timeout, start=0):
        end = time.time() + timeout
        with self.cv:
            while True:
                for e in self.events[start:]:
                    if pred(e):
                        return e
                left = end - time.time()
                if left <= 0:
                    return None
                self.cv.wait(left)

    def prompt(self, text, timeout=60):
        mark = len(self.events)
        r = self.send({"type": "prompt", "message": text})
        if not r or not r.get("success"):
            raise Fail("prompt refused: %r" % r)
        return self.wait(lambda e: e.get("type") == "agent_settled", timeout, mark)

    def close(self):
        try:
            self.proc.stdin.close()
            self.proc.terminate()
            self.proc.wait(10)
        except Exception:
            self.proc.kill()


def vrun(vec, env, *args, stdin=None, cwd=None):
    return subprocess.run([vec, *args], env=env, input=stdin, capture_output=True, text=True, timeout=60, cwd=cwd)


def main():
    for tool in ("go", "git", "node", "pi"):
        if not shutil.which(tool):
            print("missing tool: " + tool)
            return 2
    root = tempfile.mkdtemp(prefix="vecnet-pi-probe-")
    print("lab " + root)
    hub = pi = None
    stub = Stub()
    try:
        bindir = os.path.join(root, "bin")
        subprocess.run(["go", "build", "-o", bindir + os.sep, "./cmd/vecnet", "./cmd/vecnet-hub"],
                       cwd=REPO, check=True, capture_output=True, timeout=600)
        vec = os.path.join(bindir, "vecnet")
        hubdir = os.path.join(root, "hubdata")
        init = subprocess.run([os.path.join(bindir, "vecnet-hub"), "init", "--data-dir", hubdir, "--operator-name", "e2e"],
                              capture_output=True, text=True, timeout=60)
        secret = re.search(r"^operator secret:\s+(\S+)$", init.stdout + init.stderr, re.M)
        hub_log = os.path.join(root, "hub.log")
        hub = subprocess.Popen([os.path.join(bindir, "vecnet-hub"), "--listen", "127.0.0.1:0", "--data-dir", hubdir],
                               stdout=open(hub_log, "w"), stderr=subprocess.STDOUT)
        addr = ""
        end = time.time() + 30
        while time.time() < end and not addr:
            m = re.search(r"addr=(127\.0\.0\.1:\d+)", open(hub_log).read())
            addr = m.group(1) if m else ""
            time.sleep(0.1)
        check("1_hub", secret and addr)
        hub_url = "http://" + addr

        def env_for(name):
            home = os.path.join(root, name + "-home")
            os.makedirs(home, exist_ok=True)
            return {"PATH": os.environ["PATH"], "HOME": home, "VECNET_CONFIG": os.path.join(root, name + ".toml"),
                    "LANG": "C.UTF-8", "GIT_CONFIG_NOSYSTEM": "1"}
        op, rx, tx = env_for("op"), env_for("rx"), env_for("tx")
        with open(op["VECNET_CONFIG"], "w") as f:
            f.write('operator_profile = "op"\n[profiles.op]\nhub = "%s"\nsecret = "%s"\n' % (hub_url, secret.group(1)))
        for name, env in (("pirx", rx), ("pitx", tx)):
            r = vrun(vec, op, "--net", "op", "--json", "peer", "invite", "--name", name)
            token = json.loads(r.stdout.strip().splitlines()[-1])["enrol_token"]
            r = vrun(vec, env, "enrol", "--hub", hub_url, "--token", "-", "--profile", name, "--allow-insecure-http", stdin=token)
            if r.returncode != 0:
                raise Fail("enrol %s: %s" % (name, r.stderr))
        r = vrun(vec, rx, "--net", "pirx", "--json", "whoami")
        rx_id = (re.search(r'"(peer_[0-9A-Z]{26})"', r.stdout) or [None, ""])[1] if r.returncode == 0 else ""
        check("1_peers", rx_id, r.stdout.strip()[:200])

        # ---- workspace + install
        ws = os.path.join(root, "ws")
        os.makedirs(ws)
        subprocess.run(["git", "init", "-q", ws], check=True, env=rx)
        # The extension runs vecnet through a wrapper that can delay the hook
        # (step 6) and then execs it in place, so pi stays the hook's parent.
        slow_flag = os.path.join(root, "slow-hook")
        hookbin = os.path.join(bindir, "vecnet-hook")
        with open(hookbin, "w") as f:
            f.write('#!/bin/sh\n[ -e %s ] && sleep 1\nexec %s "$@"\n' % (slow_flag, vec))
        os.chmod(hookbin, 0o755)
        r = vrun(vec, rx, "hook", "install", "--client", "pi", "--profile", "pirx", "--bin", hookbin, "--write", cwd=ws)
        ext = os.path.join(ws, ".pi", "extensions", "vecnet-pirx.ts")
        check("2_install", r.returncode == 0 and os.path.exists(ext), r.stderr.strip().replace("\n", " | "))

        # ---- isolated pi agent dir: stub provider, project trusted
        agent = os.path.join(root, "pi-agent")
        os.makedirs(os.path.join(agent, "sessions"))
        json.dump({"providers": {"stub": {"baseUrl": stub.url, "api": "openai-completions", "apiKey": "stub",
                                          "models": [{"id": "stub-model", "name": "stub", "contextWindow": 100000,
                                                      "maxTokens": 1000, "input": ["text"], "reasoning": False}]}}},
                  open(os.path.join(agent, "models.json"), "w"))
        json.dump({"defaultProvider": "stub", "defaultModel": "stub-model"}, open(os.path.join(agent, "settings.json"), "w"))
        pienv = dict(rx, PI_CODING_AGENT_DIR=agent, PI_OFFLINE="1")
        # vecnet's hook must find the receiver's config through the baked
        # VECNET_CONFIG, not this env: drop it from pi's environment.
        del pienv["VECNET_CONFIG"]
        pi = Pi(pienv, ws, os.path.join(root, "pi.log"))
        r = pi.send({"type": "get_state"})
        check("3_pi_rpc", r and r.get("success"), json.dumps(r)[:200] if r else "no response")

        # Prime: the first run of a fresh profile only reports a backlog count.
        settled = pi.prompt("prime")
        check("3_prime_settled", settled is not None)
        binding_path = os.path.join(root, "state", "pirx+session.json")
        binding = json.load(open(binding_path)) if os.path.exists(binding_path) else {}
        check("3_bound", binding.get("session") and binding.get("claude_proc", {}).get("pid") == pi.proc.pid,
              "binding=%s" % json.dumps(binding))

        # ---- message A arrives with the next prompt
        r = vrun(vec, tx, "--net", "pitx", "send", rx_id, "probe message A", "--wake")
        check("3_send_A", r.returncode == 0, r.stderr.strip())
        before = len(stub.requests)
        pi.prompt("second prompt")
        first = stub.text_of(before)
        check("3_prompt_delivery", "probe message A" in first and 'untrusted="true"' in first, first[-400:])

        # ---- message B arrives mid-turn, after a tool call
        sid_file = os.path.join(root, "sid.txt")
        stub.tool_queue.append('printf "%s %s" "$PI_SESSION_ID" "$PI_SESSION_FILE" > ' + sid_file)
        r = vrun(vec, tx, "--net", "pitx", "send", rx_id, "probe message B", "--wake")
        check("4_send_B", r.returncode == 0, r.stderr.strip())
        before = len(stub.requests)
        # The tool sends C as the sender after the post-tool rate limit has
        # passed, so only the post-tool hook can deliver C within this turn.
        wrapper = os.path.join(bindir, "send-c.sh")
        with open(wrapper, "w") as f:
            f.write("#!/bin/sh\nVECNET_CONFIG=%s HOME=%s exec %s --net pitx send %s 'probe message C' --wake\n" % (tx["VECNET_CONFIG"], tx["HOME"], vec, rx_id))
        os.chmod(wrapper, 0o755)
        stub.tool_queue[-1] = 'printf "%s %s" "$PI_SESSION_ID" "$PI_SESSION_FILE" > ' + sid_file + " && sleep 6 && " + wrapper
        stub.tool_reply_delay = 3
        pi.prompt("third prompt, run the tool", timeout=90)
        texts = [stub.text_of(i) for i in range(before, len(stub.requests))]
        check("4_turn_requests", len(texts) >= 2, "requests=%d" % len(texts))
        check("4_prompt_gets_B", "probe message B" in texts[0], texts[0][-300:])
        # C was sent by the tool; it reaches the model via post-tool steering
        # in this same turn (a request after the tool result).
        later = "\n".join(texts[1:])
        deadline = time.time() + 15
        while "probe message C" not in later and time.time() < deadline:
            time.sleep(0.5)
            texts = [stub.text_of(i) for i in range(before, len(stub.requests))]
            later = "\n".join(texts[1:])
        check("4_post_tool_gets_C", "probe message C" in later, "requests=%d tail=%r" % (len(texts), later[-300:]))

        # ---- Esc mid-turn: a steered delivery discarded by clear_queue +
        # abort (what the TUI's Esc does) is stored again on settle and
        # reaches the model with the next prompt, never lost. The hook is
        # slowed by 1 s so its steer is queued after pi has already sent the
        # request carrying the tool result (held 8 s by the stub), which is
        # exactly the request the abort cancels.
        wrapper_d = os.path.join(bindir, "send-d.sh")
        with open(wrapper_d, "w") as f:
            f.write("#!/bin/sh\nVECNET_CONFIG=%s HOME=%s exec %s --net pitx send %s 'probe message D' --wake\n" % (tx["VECNET_CONFIG"], tx["HOME"], vec, rx_id))
        os.chmod(wrapper_d, 0o755)
        stub.tool_queue.append("sleep 6 && " + wrapper_d)
        stub.tool_reply_delay = 8
        open(slow_flag, "w").close()
        mark = len(pi.events)
        r = pi.send({"type": "prompt", "message": "fourth prompt, then Esc"})
        check("6_prompt_started", r and r.get("success"))
        # The tool takes ~6 s, then the slowed hook ~1 s; abort after both.
        queued = pi.wait(lambda e: e.get("type") == "tool_execution_end", 30, mark)
        time.sleep(3)
        aborted_at = len(stub.requests)
        pi.send({"type": "clear_queue"})
        pi.send({"type": "abort"})
        pi.wait(lambda e: e.get("type") == "agent_settled", 30, mark)
        stub.tool_reply_delay = 0
        os.remove(slow_flag)
        before = len(stub.requests)
        pi.prompt("fifth prompt")
        later = "\n".join(stub.text_of(i) for i in range(before, len(stub.requests)))
        seen_in_turn = any("probe message D" in stub.text_of(i) for i in range(0, aborted_at))
        check("6_steer_was_discarded", queued and not seen_in_turn, "tool_end=%s in_aborted_turn=%s" % (bool(queued), seen_in_turn))
        check("6_abort_not_lost", "probe message D" in later, later[-300:])

        sid = open(sid_file).read().split(" ", 1) if os.path.exists(sid_file) else ["", ""]
        binding = json.load(open(binding_path))
        check("5_env_matches_binding", sid[0] and sid[0] == binding.get("session") and sid[1].endswith(".jsonl"),
              "PI_SESSION_ID=%r PI_SESSION_FILE=%r bound=%r" % (sid[0], sid[1], binding.get("session")))
        return 0
    except Fail:
        return 1
    finally:
        if pi:
            pi.close()
        if hub:
            hub.terminate()
        stub.server.shutdown()
        if KEEP or any(not ok for _, ok, _ in results):
            print("kept lab " + root)
        else:
            shutil.rmtree(root, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
