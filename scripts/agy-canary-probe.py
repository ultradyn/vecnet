#!/usr/bin/env python3
"""Disposable Antigravity CLI (agy) canary probes for Vecnet delivery.

Every probe runs agy under a throwaway HOME with a dummy GEMINI_API_KEY and a
loopback Gemini API stub (GOOGLE_GEMINI_BASE_URL). Nothing touches the real
~/.gemini, no model tokens are spent, and no network traffic leaves loopback
for model calls. Output is a sanitized JSON evidence document: markers, roles,
field names and booleans only; no conversation IDs, paths, or tokens.

The `e2e` probe additionally builds this checkout's vecnet and vecnet-hub into
its own disposable directory, starts a loopback hub, invites and enrols two
throwaway peers, installs the agy hooks and MCP config with the built CLI, and
drives one stream-json session plus one TUI session against the real hook and
relay binaries (still through the loopback model stub).

Usage: python3 -I scripts/agy-canary-probe.py [--probe NAME ...] [--out FILE]
"""

from __future__ import annotations

import argparse
import http.server
import json
import os
import platform
import re
import shutil
import subprocess
import sys
import tempfile
import threading
import time

AGY = shutil.which("agy") or "agy"

_UUID = re.compile(r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}")
_TS = re.compile(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})?")
_PATH = re.compile(r"(?:file://)?/(?:tmp|home|run|var)/[^\s\"'\\]*")


def scrub(s: str) -> str:
    """Replace conversation/task IDs, timestamps and absolute paths so excerpts are safe to commit."""
    return _PATH.sub("<path>", _TS.sub("<ts>", _UUID.sub("<id>", s or "")))


# ---------------------------------------------------------------- model stub


class Stub:
    """Loopback Gemini API stub. `script(body, idx)` returns response parts."""

    def __init__(self, script):
        self.script = script
        self.requests: list[dict] = []
        self.lock = threading.Lock()
        stub = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *a):
                pass

            def do_POST(self):
                n = int(self.headers.get("Content-Length", 0) or 0)
                body = json.loads(self.rfile.read(n) or b"{}")
                with stub.lock:
                    idx = len(stub.requests)
                    stub.requests.append({"path": self.path, "body": body, "t": time.time()})
                parts = stub.script(body, idx)
                resp = {
                    "candidates": [{"content": {"role": "model", "parts": parts}, "finishReason": "STOP"}],
                    "usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 1, "totalTokenCount": 2},
                }
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                self.wfile.write(("data: " + json.dumps(resp) + "\r\n\r\n").encode())

        self.srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.url = f"http://127.0.0.1:{self.srv.server_address[1]}"
        threading.Thread(target=self.srv.serve_forever, daemon=True).start()

    def close(self):
        self.srv.shutdown()
        self.srv.server_close()


def text(s):
    return [{"text": s}]


def call(name, **args):
    return [{"functionCall": {"name": name, "args": args}}]


def last_user_text(body) -> str:
    for c in reversed(body.get("contents", [])):
        if c.get("role") == "user":
            return " ".join(p.get("text", "") for p in c.get("parts", []))
    return ""


def has_function_response(body) -> bool:
    c = body.get("contents", [])
    return bool(c) and any("functionResponse" in p for p in c[-1].get("parts", []))


def find_marker(body, marker):
    """Where does `marker` appear in one outbound request? Returns a list of locations."""
    hits = []
    if marker in json.dumps(body.get("systemInstruction", {})):
        hits.append({"where": "systemInstruction"})
    contents = body.get("contents", [])
    for i, c in enumerate(contents):
        for p in c.get("parts", []):
            where, t = "contents.text", p.get("text", "")
            if "functionResponse" in p:
                where, t = "contents.functionResponse", json.dumps(p["functionResponse"].get("response", {}))
            if marker in t:
                hits.append({
                    "where": where,
                    "role": c.get("role"),
                    "from_end": len(contents) - 1 - i,
                    "framing_prefix": t[: t.index(marker)][-160:],
                    "system_message_frame": "<SYSTEM_MESSAGE>" in t[: t.index(marker)],
                })
    return hits


# ------------------------------------------------------------------- sandbox


class Lab:
    def __init__(self, tag):
        self.root = tempfile.mkdtemp(prefix=f"vecnet-agy-{tag}-")
        self.home = os.path.join(self.root, "home")
        self.ws = os.path.join(self.root, "ws")
        os.makedirs(self.home, mode=0o700)
        os.makedirs(self.ws, mode=0o700)
        self.log = os.path.join(self.root, "events.jsonl")

    def env(self, stub, **extra):
        e = {
            "PATH": os.environ["PATH"],
            "HOME": self.home,
            "GEMINI_API_KEY": "dummy-loopback-key",
            "GOOGLE_GEMINI_BASE_URL": stub.url,
            "TERM": "dumb",
            "LANG": "C.UTF-8",
        }
        e.update(extra)
        return e

    def events(self):
        if not os.path.exists(self.log):
            return []
        with open(self.log) as f:
            return [json.loads(l) for l in f if l.strip()]

    def write_hook(self, inject: dict | None = None):
        """Hook recorder. `inject` maps "<event>:<invocationNum or *>" -> hook stdout object."""
        path = os.path.join(self.root, "hook.py")
        with open(path, "w") as f:
            f.write(HOOK_SRC.format(log=self.log, inject=json.dumps(inject or {})))
        os.chmod(path, 0o700)
        return path

    def hooks_json(self, dirpath, hook, name="vecnet-probe", events=("SessionStart", "PreInvocation", "PostInvocation", "Stop")):
        os.makedirs(os.path.join(dirpath, ".agents"), exist_ok=True)
        cfg = {name: {ev: [{"type": "command", "command": f"python3 -I {hook} {ev}", "timeout": 5}] for ev in events}}
        with open(os.path.join(dirpath, ".agents", "hooks.json"), "w") as f:
            json.dump(cfg, f)

    def cleanup(self):
        shutil.rmtree(self.root, ignore_errors=True)


HOOK_SRC = r'''#!/usr/bin/env python3
import json, os, sys
ev = sys.argv[1]
raw = sys.stdin.read()
p = json.loads(raw) if raw.strip() else {{}}
env_names = sorted(k for k in os.environ if k.startswith(("ANTIGRAVITY", "AGY", "GEMINI", "VECNET")))
def _ppid(pid):
    try:
        return int(open("/proc/%d/stat" % pid).read().rsplit(")", 1)[1].split()[1])
    except (OSError, ValueError, IndexError):
        return 0
def _ident(pid):
    """comm and exe basename of one ancestor; never reads environ."""
    comm, exe = "", ""
    try:
        comm = open("/proc/%d/comm" % pid).read().strip()
    except OSError:
        pass
    try:
        exe = os.path.basename(os.readlink("/proc/%d/exe" % pid))
    except OSError:
        pass
    return {{"pid": pid, "comm": comm, "exe": exe}}
parent = os.getppid()
grandparent = _ppid(parent)
ancestors = [_ident(pid) for pid in (parent, grandparent, _ppid(grandparent)) if pid > 0]
with open({log!r}, "a") as f:
    f.write(json.dumps({{"kind": "hook", "event": ev, "payload": p, "env_names": env_names,
                        "conv_env": os.environ.get("ANTIGRAVITY_CONVERSATION_ID", ""), "pid": os.getpid(),
                        "cwd_rel": os.path.relpath(os.getcwd(), p.get("cwd", os.getcwd())),
                        "parent": parent, "grandparent": grandparent, "ancestors": ancestors}}) + "\n")
inject = {inject}
key = ev + ":" + str(p.get("invocationNum", "*"))
out = inject.get(key, inject.get(ev + ":*", {{}}))
print(json.dumps(out))
'''


def run_agy(lab, stub, args, *, stdin=None, cwd=None, timeout=60, extra_env=None):
    res = subprocess.run(
        [AGY, *args],
        input=stdin,
        cwd=cwd or lab.ws,
        env=lab.env(stub, **(extra_env or {})),
        capture_output=True,
        text=True,
        timeout=timeout,
    )
    events = []
    for line in res.stdout.splitlines():
        try:
            events.append(json.loads(line))
        except json.JSONDecodeError:
            pass
    return res, events


def payload_shape(p):
    return {k: type(v).__name__ for k, v in sorted(p.items())}


# -------------------------------------------------------------------- probes


def probe_hooks():
    """Hook stdin/env contract, invocationNum semantics, injectSteps framing."""
    lab = Lab("hooks")
    # Turn 1 inv 0 -> userMessage, turn 1 inv 1 -> systemMessage (+user_markdown).
    # invocationNum semantics decide what turn 2 sees; record whichever fires.
    inject = {
        "SessionStart:*": {"injectSteps": [{"systemMessage": {"systemMessage": "MK_START_SYS"}}]},
        "PreInvocation:0": {"injectSteps": [{"userMessage": "<vecnet-inbox untrusted=\"true\">MK_PRE0_USER</vecnet-inbox>"}]},
        "PreInvocation:1": {"injectSteps": [{"systemMessage": {"systemMessage": "<vecnet-inbox untrusted=\"true\">MK_PRE1_SYS</vecnet-inbox>",
                                                                "metadata": {"user_markdown": "**Vecnet: 1 new**"}}}]},
        "PreInvocation:2": {"injectSteps": [{"systemMessage": {"systemMessage": "MK_PRE2_SYS"}}]},
        "PreInvocation:3": {"injectSteps": [{"systemMessage": {"systemMessage": "MK_PRE3_SYS"}}]},
    }
    hook = lab.write_hook(inject)
    lab.hooks_json(lab.ws, hook)

    def script(body, idx):
        if has_function_response(body):
            return text("STUB_AFTER_TOOL")
        if "TURN1_PROMPT" in json.dumps(body.get("contents", [])[-3:]) and idx == 0:
            return call("run_command", CommandLine="true", WaitMsBeforeAsync=5000, toolSummary="probe")
        return text(f"STUB_TEXT_{idx}")

    stub = Stub(script)
    try:
        turns = "".join(json.dumps({"event": "user", "message": {"content": c}}) + "\n" for c in ("TURN1_PROMPT", "TURN2_PROMPT"))
        res, out = run_agy(lab, stub, ["--input-format", "stream-json", "--output-format", "stream-json", "--dangerously-skip-permissions",
                                       "--settings", json.dumps({"modelProvider": "gemini"})], stdin=turns)
        hooks = [e for e in lab.events() if e["kind"] == "hook"]
        conv_ids = {e["payload"].get("conversationId") for e in hooks}
        markers = ["MK_START_SYS", "MK_PRE0_USER", "MK_PRE1_SYS", "MK_PRE2_SYS", "MK_PRE3_SYS", "TURN1_PROMPT", "TURN2_PROMPT"]
        reqs = []
        for i, r in enumerate(stub.requests):
            b = r["body"]
            reqs.append({
                "request": i,
                "last_content_role": (b.get("contents") or [{}])[-1].get("role"),
                "after_tool_result": has_function_response(b),
                "markers": {m: find_marker(b, m) for m in markers if find_marker(b, m)},
            })
        return {
            "exit": res.returncode,
            "stderr_head": scrub(res.stderr.strip()[:300]),
            "results": [e.get("result", {}).get("status") for e in out if e.get("event") == "result"],
            "hook_sequence": [{"event": e["event"], "invocationNum": e["payload"].get("invocationNum"),
                               "initialNumSteps": e["payload"].get("initialNumSteps"),
                               "lastUserInput_marker": next((m for m in markers if m in str(e["payload"].get("lastUserInput", ""))), None)}
                              for e in hooks],
            "payload_fields": {ev: payload_shape(next(e["payload"] for e in hooks if e["event"] == ev))
                               for ev in dict.fromkeys(e["event"] for e in hooks)},
            "env_names": sorted({n for e in hooks for n in e["env_names"]}),
            "conv_env_matches_payload": all(e["conv_env"] == e["payload"].get("conversationId") for e in hooks),
            "single_conversation_id": len(conv_ids) == 1,
            "hook_process_cwd_rel_to_payload_cwd": sorted({e["cwd_rel"] for e in hooks}),
            # comm and exe basename of each ancestor (never environ): how a
            # hook can identify the agy process by name rather than by depth.
            "ancestor_identity": sorted({(a.get("comm"), a.get("exe")) for e in hooks for a in e.get("ancestors", [])}),
            "requests": reqs,
            "transcript_injected": transcript_summary(hooks, markers),
        }
    finally:
        stub.close()
        lab.cleanup()


INJECT_VARIANTS = {
    "pre0_system": ("PreInvocation:0", {"injectSteps": [{"systemMessage": {"systemMessage": "MK_V"}}]}),
    "pre0_system_user_markdown": ("PreInvocation:0", {"injectSteps": [{"systemMessage": {"systemMessage": "MK_V", "metadata": {"user_markdown": "**Vecnet: 1 new**"}}}]}),
    "pre0_user": ("PreInvocation:0", {"injectSteps": [{"userMessage": "MK_V"}]}),
    "pre0_ephemeral": ("PreInvocation:0", {"injectSteps": [{"ephemeralMessage": "MK_V"}]}),
    "pre1_system": ("PreInvocation:1", {"injectSteps": [{"systemMessage": {"systemMessage": "MK_V"}}]}),
    "pre1_user": ("PreInvocation:1", {"injectSteps": [{"userMessage": "MK_V"}]}),
    "post0_system": ("PostInvocation:0", {"injectSteps": [{"systemMessage": {"systemMessage": "MK_V"}}]}),
}


def probe_inject_variants():
    """For each injectSteps variant: does MK_V reach the model, with what role/framing, on which request?"""
    out = {}
    for name, (key, resp) in INJECT_VARIANTS.items():
        lab = Lab("inj")
        hook = lab.write_hook({key: resp})
        lab.hooks_json(lab.ws, hook, events=("PreInvocation", "PostInvocation"))

        def script(body, idx):
            if idx == 0:
                return call("run_command", CommandLine="true", WaitMsBeforeAsync=5000, toolSummary="probe")
            return text("done")

        stub = Stub(script)
        try:
            res, _ = run_agy(lab, stub, ["-p", "PROMPT", "--output-format", "json", "--dangerously-skip-permissions",
                                         "--settings", json.dumps({"modelProvider": "gemini"})])
            seen = []
            for i, r in enumerate(stub.requests):
                for h in find_marker(r["body"], "MK_V"):
                    seen.append({"request": i, "after_tool_result": has_function_response(r["body"]), **h})
            hooks = [e for e in lab.events() if e["kind"] == "hook"]
            out[name] = {
                "exit": res.returncode,
                "requests": len(stub.requests),
                "first_seen_request": seen[0]["request"] if seen else None,
                "seen": [{k: s[k] for k in ("request", "where", "role", "from_end", "framing_prefix") if k in s} for s in seen[:2]],
                "transcript": transcript_summary(hooks, ["MK_V"]),
            }
        finally:
            stub.close()
            lab.cleanup()
    return out


def transcript_summary(hooks, markers):
    tp = next((e["payload"].get("transcriptPath") for e in hooks if e["payload"].get("transcriptPath")), None)
    if not tp or not os.path.exists(tp):
        return None
    rows = []
    with open(tp) as f:
        for line in f:
            d = json.loads(line)
            c = d.get("content") or ""
            m = [x for x in markers if x in c]
            if m:
                rows.append({"source": d.get("source"), "type": d.get("type"), "markers": m})
    return rows


def probe_worktree():
    """Is .agents/hooks.json discovered from a linked worktree / a subdirectory?"""
    lab = Lab("wt")
    hook = lab.write_hook()
    repo = os.path.join(lab.root, "repo")
    wt = os.path.join(lab.root, "linked")
    g = lambda *a, cwd=repo: subprocess.run(["git", *a], cwd=cwd, check=True, capture_output=True,
                                            env={**os.environ, "HOME": lab.home, "GIT_CONFIG_NOSYSTEM": "1"})
    os.makedirs(os.path.join(repo, "sub"))
    g("init", "-q", "-b", "main")
    open(os.path.join(repo, "sub", "f.txt"), "w").write("x\n")
    g("add", ".")
    g("-c", "user.name=probe", "-c", "user.email=probe@invalid", "commit", "-qm", "init")
    g("worktree", "add", "-q", wt, "-b", "linked")
    os.makedirs(os.path.join(wt, "sub"), exist_ok=True)
    stub = Stub(lambda b, i: text("ok"))
    cases = {}
    try:
        def run_case(name, cwd, hooks_at):
            for d in (repo, wt):
                shutil.rmtree(os.path.join(d, ".agents"), ignore_errors=True)
            lab.hooks_json(hooks_at, hook, name=name, events=("SessionStart",))
            before = len(lab.events())
            res, _ = run_agy(lab, stub, ["-p", "hi", "--output-format", "json", "--settings", json.dumps({"modelProvider": "gemini"})], cwd=cwd)
            fired = [e for e in lab.events()[before:] if e["kind"] == "hook"]
            cases[name] = {"exit": res.returncode, "fired": bool(fired),
                           "workspace_is_cwd": bool(fired) and fired[0]["payload"].get("workspacePaths") == [cwd]}

        run_case("main_root_cwd_root", repo, repo)
        run_case("main_root_cwd_subdir", os.path.join(repo, "sub"), repo)
        run_case("linked_root_cwd_linked", wt, wt)
        run_case("main_root_cwd_linked", wt, repo)
        run_case("linked_root_cwd_linked_subdir", os.path.join(wt, "sub"), wt)
        return cases
    finally:
        stub.close()
        lab.cleanup()


MCP_SRC = r'''#!/usr/bin/env python3
import json, sys, time, os
LOG = {log!r}
def log(**k):
    with open(LOG, "a") as f: f.write(json.dumps({{"kind": "mcp", **k}}) + "\n")
def send(o):
    sys.stdout.write(json.dumps(o) + "\n"); sys.stdout.flush()
log(ev="spawn", pid=os.getpid(), env_names=sorted(k for k in os.environ if k.startswith(("ANTIGRAVITY","AGY","GEMINI"))))
for line in sys.stdin:
    m = json.loads(line)
    mid, meth = m.get("id"), m.get("method")
    if meth == "initialize":
        send({{"jsonrpc":"2.0","id":mid,"result":{{"protocolVersion":m["params"].get("protocolVersion","2025-06-18"),
              "capabilities":{{"tools":{{}}}},"serverInfo":{{"name":"probe","version":"1"}}}}}})
    elif meth == "tools/list":
        send({{"jsonrpc":"2.0","id":mid,"result":{{"tools":[{{"name":"probe_wait","description":"Wait for a probe event.",
              "inputSchema":{{"type":"object","properties":{{"seconds":{{"type":"number"}}}}}}}}]}}}})
    elif meth == "tools/call":
        secs = float(m["params"].get("arguments",{{}}).get("seconds", 3))
        log(ev="call_start", t=time.time())
        time.sleep(secs)
        log(ev="call_end", t=time.time())
        send({{"jsonrpc":"2.0","id":mid,"result":{{"content":[{{"type":"text","text":"MK_WAIT_DONE pointer-only"}}]}}}})
    elif mid is not None:
        send({{"jsonrpc":"2.0","id":mid,"result":{{}}}})
    elif meth == "notifications/cancelled":
        log(ev="cancelled", t=time.time())
log(ev="stdin_eof", t=time.time())
'''


def probe_background_mcp():
    """background:"always" MCP tool: does it detach from the turn and wake the idle agent on completion?"""
    out = {}
    for mode in ("always", "default"):
        lab = Lab("bgmcp")
        srv = os.path.join(lab.root, "mcp.py")
        with open(srv, "w") as f:
            f.write(MCP_SRC.format(log=lab.log))
        os.makedirs(os.path.join(lab.ws, ".agents"))
        tool = {"eager": True, **({"background": "always"} if mode == "always" else {})}
        with open(os.path.join(lab.ws, ".agents", "mcp_config.json"), "w") as f:
            json.dump({"mcpServers": {"probe": {"command": "python3", "args": ["-I", srv], "tools": {"probe_wait": tool}}}}, f)
        decl = {}

        def script(body, idx):
            if idx == 0:
                d = next((d for t in body.get("tools", []) for d in t.get("functionDeclarations", []) if d["name"].endswith("probe_wait")), {})
                decl.update(name=d.get("name"), required=(d.get("parametersJsonSchema") or {}).get("required"))
                return call(d.get("name", "mcp_probe_probe_wait"), seconds=4, toolSummary="Probe wait")
            return text(f"STUB_TEXT_{idx}")

        stub = Stub(script)
        try:
            t0 = time.time()
            proc = subprocess.Popen([AGY, "--input-format", "stream-json", "--output-format", "stream-json", "--dangerously-skip-permissions",
                                     "--settings", json.dumps({"modelProvider": "gemini"})],
                                    stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, cwd=lab.ws, env=lab.env(stub))
            proc.stdin.write(json.dumps({"event": "user", "message": {"content": "go"}}) + "\n")
            proc.stdin.flush()
            time.sleep(10)
            proc.stdin.close()
            stdout, _ = proc.communicate(timeout=30)
            reqs = []
            for r in stub.requests:
                last = (r["body"].get("contents") or [{}])[-1]
                hit = find_marker(r["body"], "MK_WAIT_DONE")
                tail = json.dumps(last.get("parts", []))
                reqs.append({
                    "t": round(r["t"] - t0, 1),
                    "last_role": last.get("role"),
                    "function_response_says_background": "running as a background task" in tail,
                    "function_response_excerpt": (scrub(json.dumps(last["parts"][0]["functionResponse"].get("response", {}))[:200])
                                                  if last.get("parts") and "functionResponse" in last["parts"][0] else None),
                    "wait_result": ({"where": hit[-1]["where"], "role": hit[-1]["role"],
                                     "system_message_frame": hit[-1]["system_message_frame"],
                                     "priority_high": "MESSAGE_PRIORITY_HIGH" in hit[-1]["framing_prefix"],
                                     "framing_excerpt": scrub(hit[-1]["framing_prefix"])} if hit else None),
                })
            mcp = {e["ev"]: round(e["t"] - t0, 1) for e in lab.events() if e["kind"] == "mcp" and "t" in e}
            results = [json.loads(l).get("result", {}) for l in stdout.splitlines() if '"event":"result"' in l.replace(" ", "")]
            spawn = next((e for e in lab.events() if e["kind"] == "mcp" and e["ev"] == "spawn"), {})
            out[mode] = {
                "declaration": decl,
                "mcp_server_env_names": spawn.get("env_names"),
                "requests": reqs,
                "mcp_timeline": mcp,
                "results": [{"status": r.get("status"), "response": r.get("response", "").strip()} for r in results],
            }
        finally:
            stub.close()
            lab.cleanup()
    return out


def probe_sidecar_and_agentapi():
    """Does agy CLI print mode launch an enabled plugin sidecar? Who can reach agentapi, and how does send-message land?"""
    out = {}
    # (a) plugin sidecar lifecycle under print mode
    lab = Lab("sc")
    src = os.path.join(lab.root, "probeplug")
    scdir = os.path.join(src, "sidecars", "bridge")
    os.makedirs(scdir)
    with open(os.path.join(src, "plugin.json"), "w") as f:
        json.dump({"name": "probeplug", "version": "0.0.1", "description": "probe"}, f)
    sclog = os.path.join(lab.root, "sc.jsonl")
    with open(os.path.join(scdir, "run.py"), "w") as f:
        f.write(f"import json, time\nopen({sclog!r}, 'a').write(json.dumps({{'ev': 'start'}}) + '\\n')\ntime.sleep(20)\n")
    with open(os.path.join(scdir, "sidecar.json"), "w") as f:
        json.dump({"command": "python3", "args": ["-I", os.path.join(scdir, "run.py")], "restart_policy": "never"}, f)
    stub = Stub(lambda b, i: text("ok"))
    try:
        steps = {}
        for name, cmd in (("validate", ["plugin", "validate", src, "--json"]), ("install", ["plugin", "install", src]),
                          ("enable", ["plugin", "enable", "probeplug"])):
            r = subprocess.run([AGY, *cmd], env=lab.env(stub), cwd=lab.ws, capture_output=True, text=True, timeout=60)
            steps[name] = r.returncode
        r = subprocess.run([AGY, "plugin", "list", "--json"], env=lab.env(stub), cwd=lab.ws, capture_output=True, text=True, timeout=60)
        steps["listed_enabled"] = any(pl.get("name") == "probeplug" and pl.get("enabled") for pl in json.loads(r.stdout or "[]"))
        # Documented opt-in (docs/sidecars): `agy plugin enable` only writes {"plugins": {...}}; add the sidecars block.
        cfg_path = os.path.join(lab.home, ".gemini", "config", "config.json")
        cfg = json.load(open(cfg_path)) if os.path.exists(cfg_path) else {}
        steps["config_keys_after_enable"] = sorted(cfg)
        cfg.setdefault("sidecars", {})["probeplug/bridge"] = {"enabled": True}
        with open(cfg_path, "w") as f:
            json.dump(cfg, f)
        steps["sidecar_opt_in_written"] = True
        res, _ = run_agy(lab, stub, ["-p", "hi", "--output-format", "json", "--settings", json.dumps({"modelProvider": "gemini"})])
        time.sleep(2)
        out["plugin_sidecar_print_mode"] = {**steps, "agy_exit": res.returncode, "sidecar_started": os.path.exists(sclog),
                                            "interactive_tui": "not run: fresh HOME requires accepting Google ToS + data-use consent"}
    finally:
        stub.close()
        subprocess.run(["pkill", "-f", scdir], capture_output=True)
        lab.cleanup()

    # (b) agentapi send-message from inside the session, busy (synchronous tool) and idle (daemon task still running)
    for mode in ("busy", "idle"):
        lab = Lab("aa")
        sh = os.path.join(lab.root, "send.sh")
        res_f = os.path.join(lab.root, "send.out")
        with open(sh, "w") as f:
            f.write("#!/bin/sh\n"
                    f"sleep {4 if mode == 'idle' else 0}\n"
                    f"basename \"$ANTIGRAVITY_AGENTAPI_EXE\" > {res_f}\n"
                    f"\"$ANTIGRAVITY_AGENTAPI_EXE\" send-message \"$ANTIGRAVITY_CONVERSATION_ID\" MK_EXE_PTR >/dev/null 2>&1; echo $? >> {res_f}\n"
                    f"\"$HOME/.gemini/antigravity-cli/bin/agentapi\" send-message \"$ANTIGRAVITY_CONVERSATION_ID\" MK_AGENTAPI_PTR >/dev/null 2>&1\n"
                    f"echo $? >> {res_f}\n"
                    f"env | cut -d= -f1 | grep ^ANTIGRAVITY | sort | tr '\\n' ' ' >> {res_f}\n"
                    f"{'sleep 20' if mode == 'idle' else ''}\n")
        os.chmod(sh, 0o700)

        def script(b, i, mode=mode, sh=sh):
            if i == 0:
                return call("run_command", CommandLine=sh, WaitMsBeforeAsync=500 if mode == "idle" else 8000,
                            IsDaemon=(mode == "idle"), toolSummary="Agentapi send")
            return text(f"T{i}")

        stub = Stub(script)
        try:
            t0 = time.time()
            proc = subprocess.Popen([AGY, "--input-format", "stream-json", "--output-format", "stream-json", "--dangerously-skip-permissions",
                                     "--settings", json.dumps({"modelProvider": "gemini"})],
                                    stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, cwd=lab.ws, env=lab.env(stub))
            proc.stdin.write(json.dumps({"event": "user", "message": {"content": "go"}}) + "\n")
            proc.stdin.flush()
            time.sleep(12)
            proc.stdin.close()
            proc.communicate(timeout=40)
            lines = open(res_f).read().splitlines() if os.path.exists(res_f) else []
            hit = None
            for i, r in enumerate(stub.requests):
                h = find_marker(r["body"], "MK_AGENTAPI_PTR")
                if h:
                    hit = {"request": i, "t": round(r["t"] - t0, 1), "where": h[-1]["where"], "role": h[-1]["role"],
                           "system_message_frame": h[-1]["system_message_frame"],
                           "priority_high": "MESSAGE_PRIORITY_HIGH" in h[-1]["framing_prefix"],
                           "framing_excerpt": scrub(h[-1]["framing_prefix"])}
                    break
            out[f"send_message_{mode}"] = {
                "agentapi_exe_env_basename": lines[0] if lines else None,
                "agentapi_exe_env_send_exit": int(lines[1]) if len(lines) > 1 and lines[1].isdigit() else None,
                "shim_send_exit": int(lines[2]) if len(lines) > 2 and lines[2].isdigit() else None,
                "run_command_env_names": lines[3].split() if len(lines) > 3 else None,
                "model_requests": len(stub.requests),
                "delivered": hit,
            }
        finally:
            stub.close()
            subprocess.run(["pkill", "-f", sh], capture_output=True)
            lab.cleanup()

    # (c) can a hook reach agentapi? (hooks do not receive ANTIGRAVITY_CSRF_TOKEN / LS_ADDRESS)
    lab = Lab("aahook")
    res_f = os.path.join(lab.root, "hook.out")
    hk = os.path.join(lab.root, "hook.sh")
    with open(hk, "w") as f:
        f.write("#!/bin/sh\ncat >/dev/null\n"
                f"{AGY} agentapi get-conversation-metadata \"$ANTIGRAVITY_CONVERSATION_ID\" > {res_f}.txt 2>&1; echo $? > {res_f}\n"
                "echo '{}'\n")
    os.chmod(hk, 0o700)
    os.makedirs(os.path.join(lab.ws, ".agents"))
    with open(os.path.join(lab.ws, ".agents", "hooks.json"), "w") as f:
        json.dump({"probe": {"PreInvocation": [{"type": "command", "command": hk, "timeout": 10}]}}, f)
    stub = Stub(lambda b, i: text("ok"))
    try:
        run_agy(lab, stub, ["-p", "hi", "--output-format", "json", "--settings", json.dumps({"modelProvider": "gemini"})])
        rc = open(res_f).read().strip() if os.path.exists(res_f) else None
        txt = open(res_f + ".txt").read() if os.path.exists(res_f + ".txt") else ""
        out["agentapi_from_hook"] = {"invoked_as": "agy agentapi", "get_metadata_exit": int(rc) if rc and rc.isdigit() else rc,
                                     "output_excerpt": scrub(txt.strip()[:160])}
    finally:
        stub.close()
        lab.cleanup()
    return out


def probe_acp():
    """agy acp: initialize, session/new, set_mode default, prompt, permission request, cancel, close."""
    lab = Lab("acp")
    gate = threading.Event()

    def script(body, idx):
        # agy acp also issues tool-less side requests (e.g. titles); key responses on the user prompt.
        if not body.get("tools"):
            return text("side")
        if has_function_response(body):
            return text("ACP_AFTER_TOOL")
        u = last_user_text(body)
        if "ACP_PING" in u:
            return text("ACP_PONG_OK")
        if "ACP_RUN" in u:
            return call("run_command", CommandLine="echo acp-perm", WaitMsBeforeAsync=3000, toolSummary="Permission probe")
        gate.wait(20)  # hold the turn open so session/cancel lands mid-turn
        return text("ACP_SLOW")

    stub = Stub(script)
    cfg = os.path.join(lab.home, ".gemini", "antigravity-cli")
    os.makedirs(cfg)
    with open(os.path.join(cfg, "settings.json"), "w") as f:
        json.dump({"modelProvider": "gemini"}, f)
    p = subprocess.Popen([AGY, "acp"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                         text=True, cwd=lab.ws, env=lab.env(stub))
    msgs: list[dict] = []
    cond = threading.Condition()

    def reader():
        for line in p.stdout:
            try:
                m = json.loads(line)
            except json.JSONDecodeError:
                continue
            with cond:
                msgs.append(m)
                cond.notify_all()
            if m.get("method") == "session/request_permission":
                opts = m["params"].get("options", [])
                pick = next((o for o in opts if o.get("kind") == "allow_once"), opts[0] if opts else None)
                p.stdin.write(json.dumps({"jsonrpc": "2.0", "id": m["id"], "result": {"outcome": {"outcome": "selected", "optionId": pick["optionId"]}}}) + "\n")
                p.stdin.flush()

    threading.Thread(target=reader, daemon=True).start()
    nid = [0]

    def send(method, params, wait=True, timeout=30):
        nid[0] += 1
        i = nid[0]
        p.stdin.write(json.dumps({"jsonrpc": "2.0", "id": i, "method": method, "params": params}) + "\n")
        p.stdin.flush()
        if not wait:
            return i
        return await_id(i, timeout)

    def await_id(i, timeout=30):
        end = time.time() + timeout
        with cond:
            while True:
                hit = next((m for m in msgs if m.get("id") == i and "method" not in m), None)
                if hit or time.time() > end:
                    return hit
                cond.wait(0.5)

    try:
        init = send("initialize", {"protocolVersion": 1, "clientCapabilities": {"fs": {"readTextFile": False, "writeTextFile": False}, "terminal": False},
                                   "clientInfo": {"name": "vecnet-acp-probe", "version": "1"}})
        new = send("session/new", {"cwd": lab.ws, "mcpServers": []})
        sid = new["result"]["sessionId"]
        modes = new["result"].get("modes", {})
        mode = send("session/set_mode", {"sessionId": sid, "modeId": "default"})
        p1 = send("session/prompt", {"sessionId": sid, "prompt": [{"type": "text", "text": "ACP_PING"}]})
        text1 = "".join(m["params"]["update"].get("content", {}).get("text", "") for m in msgs
                        if m.get("method") == "session/update" and m["params"]["update"].get("sessionUpdate") == "agent_message_chunk")
        p2 = send("session/prompt", {"sessionId": sid, "prompt": [{"type": "text", "text": "ACP_RUN"}]}, timeout=40)
        perm = [m for m in msgs if m.get("method") == "session/request_permission"]
        pid3 = send("session/prompt", {"sessionId": sid, "prompt": [{"type": "text", "text": "ACP_SLOW"}]}, wait=False)
        time.sleep(2)
        p.stdin.write(json.dumps({"jsonrpc": "2.0", "method": "session/cancel", "params": {"sessionId": sid}}) + "\n")
        p.stdin.flush()
        p3 = await_id(pid3, 15)
        gate.set()
        close = send("session/close", {"sessionId": sid})
        upd_kinds = sorted({m["params"]["update"].get("sessionUpdate") for m in msgs if m.get("method") == "session/update"})
        return {
            "protocolVersion": (init or {}).get("result", {}).get("protocolVersion"),
            "agentCapabilities": (init or {}).get("result", {}).get("agentCapabilities"),
            "mode_ids": [m.get("id") for m in modes.get("availableModes", [])],
            "current_mode": modes.get("currentModeId"),
            "set_mode_default_ok": bool(mode and "result" in mode),
            "prompt1_stop": (p1 or {}).get("result", {}).get("stopReason"),
            "prompt1_text_ok": "ACP_PONG_OK" in text1,
            "prompt2_stop": (p2 or {}).get("result", {}).get("stopReason"),
            "permission_requests": len(perm),
            "permission_option_kinds": [o.get("kind") for o in (perm[0]["params"].get("options", []) if perm else [])],
            "permission_tool_kind": perm[0]["params"].get("toolCall", {}).get("kind") if perm else None,
            "cancel_stop": (p3 or {}).get("result", {}).get("stopReason") if p3 else "no-response",
            "close_ok": bool(close and "result" in close),
            "update_kinds": upd_kinds,
            "model_requests": len(stub.requests),
        }
    finally:
        gate.set()
        p.terminate()
        try:
            p.wait(5)
        except subprocess.TimeoutExpired:
            p.kill()
        stub.close()
        lab.cleanup()


class Tui:
    """Interactive agy TUI in a private tmux session. Seeds the onboarding record (ToS accepted,
    interactions-data collection opted out) so a fresh HOME reaches the prompt."""

    def __init__(self, lab, stub, settings):
        self.sess = "vecnet-test-" + os.urandom(4).hex()
        ag = os.path.join(lab.home, ".gemini", "antigravity-cli")
        os.makedirs(os.path.join(ag, "cache"), exist_ok=True)
        with open(os.path.join(ag, "cache", "onboarding.json"), "w") as f:
            json.dump({"consumerOnboardingComplete": True, "enterpriseOnboardingComplete": False, "onboardingComplete": True}, f)
        with open(os.path.join(ag, "settings.json"), "w") as f:
            json.dump({"modelProvider": "gemini", "colorScheme": "dark", "trustedWorkspaces": [lab.ws], **settings}, f)
        env = " ".join(f"{k}={v!r}" for k, v in lab.env(stub, TERM="xterm-256color").items())
        subprocess.run(["tmux", "new-session", "-d", "-s", self.sess, "-x", "160", "-y", "45",
                        f"cd {lab.ws!r} && env -i {env} {AGY}"], check=True)
        self.wait_for("for shortcuts", 20)

    def pane(self):
        return subprocess.run(["tmux", "capture-pane", "-p", "-t", self.sess], capture_output=True, text=True).stdout

    def wait_for(self, s, secs):
        end = time.time() + secs
        while time.time() < end:
            if s in self.pane():
                return True
            time.sleep(0.5)
        return False

    def prompt(self, s):
        subprocess.run(["tmux", "send-keys", "-t", self.sess, "-l", s])
        time.sleep(0.5)
        subprocess.run(["tmux", "send-keys", "-t", self.sess, "Enter"])

    def alive(self):
        return subprocess.run(["tmux", "has-session", "-t", self.sess], capture_output=True).returncode == 0

    def kill(self):
        subprocess.run(["tmux", "kill-session", "-t", self.sess], capture_output=True)


def probe_tui():
    """Interactive TUI: background-MCP idle wake, /exit with a pending call, plugin sidecar, idle agentapi send."""
    out = {}
    # (a) background MCP wake + /exit with pending call
    lab = Lab("tbg")
    srv = os.path.join(lab.root, "mcp.py")
    with open(srv, "w") as f:
        f.write(MCP_SRC.format(log=lab.log))
    os.makedirs(os.path.join(lab.ws, ".agents"))
    with open(os.path.join(lab.ws, ".agents", "mcp_config.json"), "w") as f:
        json.dump({"mcpServers": {"probe": {"command": "python3", "args": ["-I", srv],
                                            "tools": {"probe_wait": {"eager": True, "background": "always"}}}}}, f)
    tags = []

    def s1(b, i):
        last = json.dumps(b.get("contents", [])[-1:])
        u = last_user_text(b)
        if not b.get("tools"):
            return text("side")
        if "MK_WAIT_DONE" in last:
            tags.append("wake")
            return text("WOKE_BY_BG_MARK")
        if not has_function_response(b) and "WAIT_SHORT" in u:
            return call("mcp_probe_probe_wait", seconds=5, toolSummary="Short wait")
        if not has_function_response(b) and "WAIT_LONG" in u:
            return call("mcp_probe_probe_wait", seconds=120, toolSummary="Long wait")
        return text("TURN_DONE_MARK")

    stub = Stub(s1)
    t = None
    try:
        t = Tui(lab, stub, {"toolPermission": "always-proceed"})
        mcp_ev = lambda: [e["ev"] for e in lab.events() if e["kind"] == "mcp"]
        # MCP connects lazily in the TUI: a first-turn call can race it ("unknown tool"), so warm up first.
        t.prompt("warmup")
        t.wait_for("TURN_DONE_MARK", 15)
        time.sleep(3)
        t.prompt("WAIT_SHORT")
        woke = t.wait_for("WOKE_BY_BG_MARK", 25)
        pane = t.pane()
        out["bg_mcp_wake"] = {"turn_ended_first": pane.count("TURN_DONE_MARK") >= 2, "woke_and_rendered": woke,
                              "wake_requests": tags.count("wake"), "mcp_events": mcp_ev()}
        t.prompt("WAIT_LONG")
        time.sleep(8)
        pending = mcp_ev().count("call_start") - mcp_ev().count("call_end")
        status_bar_task = "1 task(s)" in t.pane()
        t.prompt("/exit")
        time.sleep(5)
        out["exit_with_pending_call"] = {
            "pending_calls": pending, "status_bar_showed_task": status_bar_task,
            "tui_exited": not t.alive(),
            "mcp_server_alive_after": subprocess.run(["pgrep", "-f", srv], capture_output=True).returncode == 0,
            "mcp_events_after": mcp_ev()[-3:],
        }
    finally:
        if t:
            t.kill()
        stub.close()
        subprocess.run(["pkill", "-f", srv], capture_output=True)
        lab.cleanup()

    # (b) plugin sidecar with documented opt-in
    lab = Lab("tsc")
    src = os.path.join(lab.root, "probeplug")
    scdir = os.path.join(src, "sidecars", "bridge")
    os.makedirs(scdir)
    with open(os.path.join(src, "plugin.json"), "w") as f:
        json.dump({"name": "probeplug", "version": "0.0.1", "description": "probe"}, f)
    sclog = os.path.join(lab.root, "sc.jsonl")
    with open(os.path.join(scdir, "run.py"), "w") as f:
        f.write(f"import json, os, time\nopen({sclog!r}, 'a').write(json.dumps(sorted(k for k in os.environ if k.startswith('ANTIGRAVITY'))) + '\\n')\ntime.sleep(30)\n")
    with open(os.path.join(scdir, "sidecar.json"), "w") as f:
        json.dump({"command": "python3", "args": ["-I", os.path.join(scdir, "run.py")], "restart_policy": "never"}, f)
    stub = Stub(lambda b, i: text("ok"))
    t = None
    try:
        for cmd in (["plugin", "install", src], ["plugin", "enable", "probeplug"]):
            subprocess.run([AGY, *cmd], env=lab.env(stub), cwd=lab.ws, capture_output=True, timeout=60)
        cfg_path = os.path.join(lab.home, ".gemini", "config", "config.json")
        cfg = json.load(open(cfg_path)) if os.path.exists(cfg_path) else {}
        cfg.setdefault("sidecars", {})["probeplug/bridge"] = {"enabled": True}
        with open(cfg_path, "w") as f:
            json.dump(cfg, f)
        t = Tui(lab, stub, {})
        t.prompt("hi")
        time.sleep(8)
        out["plugin_sidecar"] = {"opt_in_written": True, "sidecar_started": os.path.exists(sclog)}
    finally:
        if t:
            t.kill()
        stub.close()
        subprocess.run(["pkill", "-f", scdir], capture_output=True)
        lab.cleanup()

    # (c) agentapi send-message to the idle TUI from a still-running daemon task
    lab = Lab("taa")
    sh = os.path.join(lab.root, "send.sh")
    rc_f = os.path.join(lab.root, "rc")
    with open(sh, "w") as f:
        f.write(f"#!/bin/sh\nsleep 5\n{AGY} agentapi send-message \"$ANTIGRAVITY_CONVERSATION_ID\" MK_AGENTAPI_PTR >/dev/null 2>&1\necho $? > {rc_f}\nsleep 30\n")
    os.chmod(sh, 0o700)

    def s3(b, i):
        if not b.get("tools"):
            return text("side")
        if "MK_AGENTAPI_PTR" in json.dumps(b.get("contents", [])[-1:]):
            return text("WOKE_BY_AGENTAPI_MARK")
        if "SEND_IT" in last_user_text(b) and not has_function_response(b):
            return call("run_command", CommandLine=sh, WaitMsBeforeAsync=500, IsDaemon=True, toolSummary="Delayed send")
        return text("TURN_DONE_MARK")

    stub = Stub(s3)
    t = None
    try:
        t = Tui(lab, stub, {"toolPermission": "always-proceed"})
        t.prompt("SEND_IT")
        ended = t.wait_for("TURN_DONE_MARK", 15)
        woke = t.wait_for("WOKE_BY_AGENTAPI_MARK", 20)
        pane = t.pane()
        rc = open(rc_f).read().strip() if os.path.exists(rc_f) else None
        out["agentapi_idle_send"] = {"turn_ended_first": ended, "send_exit": int(rc) if rc and rc.isdigit() else rc,
                                     "woke_and_rendered": woke, "pointer_text_shown_in_tui": "MK_AGENTAPI_PTR" in pane}
    finally:
        if t:
            t.kill()
        stub.close()
        subprocess.run(["pkill", "-f", sh], capture_output=True)
        lab.cleanup()
    return out


def probe_session_identity():
    """Subagent and /clear: which conversationId, process and turn-start fields does each hook run see?"""
    def summarize(lab):
        convs, rows = {}, []
        for e in lab.events():
            if e["kind"] != "hook":
                continue
            pl = e["payload"]
            n = convs.setdefault(pl.get("conversationId"), len(convs))
            rows.append({"conv": n, "event": e["event"], "invocationNum": pl.get("invocationNum"),
                         "initialNumSteps": pl.get("initialNumSteps"),
                         "lastUserInput_empty": not str(pl.get("lastUserInput") or "").strip(),
                         "agy_process": e["grandparent"]})
        procs = {r["agy_process"] for r in rows}
        for r in rows:
            r["agy_process"] = "same" if len(procs) == 1 else "differs"
        return rows

    out = {}
    lab = Lab("sub")
    hook = lab.write_hook()
    lab.hooks_json(lab.ws, hook, events=("SessionStart", "PreInvocation"))

    def script(b, i):
        if "ROOT_PROMPT" in last_user_text(b) and not has_function_response(b):
            return call("invoke_subagent", Prompt="SUB_PROMPT", TypeName="self", Role="Probe helper", Workspace=lab.ws, Model="inherit")
        return text("ok")

    stub = Stub(script)
    try:
        proc = subprocess.Popen([AGY, "--input-format", "stream-json", "--output-format", "stream-json", "--dangerously-skip-permissions",
                                 "--settings", json.dumps({"modelProvider": "gemini"})],
                                stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, cwd=lab.ws, env=lab.env(stub))
        proc.stdin.write(json.dumps({"event": "user", "message": {"content": "ROOT_PROMPT"}}) + "\n")
        proc.stdin.flush()
        time.sleep(15)
        proc.stdin.close()
        proc.communicate(timeout=30)
        out["subagent"] = summarize(lab)
    finally:
        stub.close()
        lab.cleanup()

    lab = Lab("clr")
    hook = lab.write_hook()
    lab.hooks_json(lab.ws, hook, events=("SessionStart", "PreInvocation"))
    stub = Stub(lambda b, i: text("TURN_DONE_MARK"))
    t = None
    try:
        t = Tui(lab, stub, {"toolPermission": "always-proceed"})
        t.prompt("FIRST")
        t.wait_for("TURN_DONE_MARK", 15)
        time.sleep(1)
        t.prompt("/clear")
        time.sleep(3)
        t.prompt("SECOND")
        time.sleep(5)
        out["tui_clear"] = summarize(lab)
    finally:
        if t:
            t.kill()
        stub.close()
        lab.cleanup()
    return out


# ----------------------------------------------------------------- e2e probe


class Fail(Exception):
    pass


class Steps:
    """Named boolean results; failures are collected and fail the whole probe."""

    def __init__(self):
        self.rows = {}
        self.failed = []

    def check(self, name, ok, **detail):
        row = {"ok": bool(ok)}
        row.update(detail)
        self.rows[name] = row
        if not ok:
            self.failed.append(name)
        return bool(ok)


def cli_env(home, config, **extra):
    e = {
        "PATH": os.environ["PATH"],
        "HOME": home,
        "VECNET_CONFIG": config,
        "TERM": "dumb",
        "LANG": "C.UTF-8",
        "GIT_CONFIG_NOSYSTEM": "1",
    }
    e.update(extra)
    return e


def vrun(vec, env, *args, cwd=None, stdin=None, timeout=90):
    return subprocess.run([vec, *args], cwd=cwd, env=env, input=stdin,
                          capture_output=True, text=True, timeout=timeout)


def await_body(stub, marker, timeout=60, need_tools=True, without=None):
    """Index of the first stub request whose contents mention marker, or -1.

    need_tools skips agy's tool-less side requests (titles): with tools set, a
    marker only matches real turn requests that carry tool declarations.
    `without` names a marker that must NOT appear (a subagent request mentions
    its prompt marker inside the root's tool result too, once it completes).
    """
    end = time.time() + timeout
    while time.time() < end:
        with stub.lock:
            for i, r in enumerate(stub.requests):
                if need_tools and not r["body"].get("tools"):
                    continue
                b = json.dumps(r["body"].get("contents", []))
                if marker in b and (without is None or without not in b):
                    return i
        time.sleep(0.2)
    return -1


def part_text_with(body, marker):
    """Full text of the contents part that carries marker, or None."""
    for c in body.get("contents", []):
        for p in c.get("parts", []):
            if marker in p.get("text", ""):
                return p["text"]
    return None


def tool_response(body, suffix):
    """The parsed response of the last functionResponse named *suffix."""
    for c in reversed(body.get("contents", [])):
        for p in reversed(c.get("parts", [])):
            fr = p.get("functionResponse")
            if fr and str(fr.get("name", "")).endswith(suffix):
                return fr.get("response")
    return None


def await_tool_response(stub, suffix, timeout=90):
    """Index of the first request carrying a functionResponse named *suffix."""
    end = time.time() + timeout
    while time.time() < end:
        with stub.lock:
            for i, r in enumerate(stub.requests):
                if tool_response(r["body"], suffix) is not None:
                    return i
        time.sleep(0.2)
    return -1


def last_json(text):
    """The last JSON object/array line of a CLI output (prose may precede it)."""
    for line in reversed((text or "").splitlines()):
        line = line.strip()
        if line.startswith(("{", "[")):
            return json.loads(line)
    return None


def probe_e2e():
    """Disposable hub + built CLI + real agy: install, hooks, subagent, MCP, wait, no ack.

    Steps (task 6 brief): (1) hook install --client agy --mcp --write with the
    built binary; (2) a hub message reaches the next PreInvocation's
    injectSteps and the model request inside the inert frame; (3) a subagent
    turn with a pending delivery gets nothing; (4) an MCP vecnet_inbox call
    with agy's toolSummary succeeds; (5) a background vecnet_wait wakes the
    idle TUI session after a hub message; (6) no hub ack happened.
    """
    repo = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    lab = Lab("e2e")
    steps = Steps()
    hub_proc = None
    tui = None
    stub = stub2 = None
    RX, SEND = "agyrx", "sender"
    try:
        # ---- build this checkout's CLI into the disposable lab (never bin/)
        bindir = os.path.join(lab.root, "bin")
        subprocess.run(["go", "build", "-o", bindir + os.sep, "./cmd/..."],
                       cwd=repo, check=True, capture_output=True, timeout=600)
        vec = os.path.join(bindir, "vecnet")
        steps.check("1_build", os.path.exists(vec) and os.path.exists(os.path.join(bindir, "vecnet-hub")))

        # ---- disposable loopback hub (scripts/mcp-conformance-local.sh recipe)
        hubdir = os.path.join(lab.root, "hubdata")
        init = subprocess.run([os.path.join(bindir, "vecnet-hub"), "init", "--data-dir", hubdir,
                               "--operator-name", "e2e"], capture_output=True, text=True, timeout=60)
        m = re.search(r"^operator secret:\s+(\S+)$", init.stdout + init.stderr, re.M)
        hub_log = os.path.join(lab.root, "hub.log")
        hub_proc = subprocess.Popen([os.path.join(bindir, "vecnet-hub"), "--listen", "127.0.0.1:0",
                                     "--data-dir", hubdir],
                                    stdout=open(hub_log, "w"), stderr=subprocess.STDOUT)
        addr = ""
        end = time.time() + 30
        while time.time() < end:
            if os.path.exists(hub_log):
                a = re.search(r"addr=(127\.0\.0\.1:\d+)", open(hub_log).read())
                if a:
                    addr = a.group(1)
                    break
            if hub_proc.poll() is not None:
                raise Fail("hub exited during startup")
            time.sleep(0.1)
        steps.check("1_hub", bool(m) and bool(addr))
        hub_url = "http://" + addr

        # ---- operator profile, two invites, two enrolments (disposable configs)
        op_home = os.path.join(lab.root, "op-home")
        rx_home = os.path.join(lab.root, "rx-home")
        send_home = os.path.join(lab.root, "send-home")
        for d in (op_home, rx_home, send_home):
            os.makedirs(d, mode=0o700)
        op_cfg = os.path.join(lab.root, "op-config.toml")
        rx_cfg = os.path.join(lab.root, "rx-config.toml")
        send_cfg = os.path.join(lab.root, "send-config.toml")
        with open(op_cfg, "w") as f:
            f.write(f'operator_profile = "op"\n[profiles.op]\nhub = "{hub_url}"\nsecret = "{m.group(1)}"\n')
        op_env = cli_env(op_home, op_cfg)
        rx_env = cli_env(rx_home, rx_cfg)
        send_env = cli_env(send_home, send_cfg)
        tokens = {}
        for name in (RX, SEND):
            r = vrun(vec, op_env, "--net", "op", "--json", "peer", "invite", "--name", "e2e-" + name)
            j = last_json(r.stdout) or {}
            tokens[name] = j.get("enrol_token", "")
        rx_peer = ""
        r = vrun(vec, op_env, "--net", "op", "--json", "peers")
        entries = last_json(r.stdout) or []
        if isinstance(entries, dict):
            entries = entries.get("peers", [])
        for p in entries:
            if p.get("name") == "e2e-" + RX:
                rx_peer = p.get("peer_id") or p.get("id") or ""
        enrolled = all(
            vrun(vec, env, "enrol", "--hub", hub_url, "--token", "-", "--profile", name,
                 "--allow-insecure-http", stdin=tokens[name]).returncode == 0
            for name, env in ((RX, rx_env), (SEND, send_env)))
        steps.check("1_peers", enrolled and bool(rx_peer))

        # ---- workspace repo + install (step 1)
        g = lambda *a: subprocess.run(["git", *a], cwd=lab.ws, check=True, capture_output=True,
                                      env={**os.environ, "HOME": lab.home, "GIT_CONFIG_NOSYSTEM": "1"})
        open(os.path.join(lab.ws, "note.txt"), "w").write("e2e\n")
        g("init", "-q", "-b", "main")
        g("add", ".")
        g("-c", "user.name=probe", "-c", "user.email=probe@invalid", "commit", "-qm", "init")
        # --bin: the lab is temporary, and defaultBin deliberately refuses to
        # bake a temporary path into the workspace files (it would fall back to
        # a PATH "vecnet", which here is some other install).
        ins = vrun(vec, rx_env, "hook", "install", "--client", "agy", "--profile", RX,
                   "--mcp", "--write", "--bin", vec, cwd=lab.ws)
        hooks = json.load(open(os.path.join(lab.ws, ".agents", "hooks.json")))
        group = hooks.get("vecnet-" + RX, {})
        mcp = json.load(open(os.path.join(lab.ws, ".agents", "mcp_config.json")))
        server = mcp.get("mcpServers", {}).get("vecnet-" + RX, {})
        steps.check("1_install", ins.returncode == 0
                    and sorted(set(group) - {"enabled"}) == ["PreInvocation", "SessionStart"]
                    and group.get("enabled") is True
                    and server.get("command") == vec
                    and server.get("args") == ["--net", RX, "mcp"]
                    and server.get("timeoutSeconds") == 1860
                    and server.get("tools", {}).get("vecnet_wait", {}).get("background") == "always",
                    install_advice=[scrub(l) for l in ins.stderr.splitlines() if l.startswith("agy:")])

        def send_hub(text, priority):
            return vrun(vec, send_env, "--net", SEND, "send", rx_peer, text, priority)

        # ---- stream-json session: steps 2, 3, 4. Nothing is sent before the
        # first hook run: a fresh profile's first PreInvocation only counts a
        # backlog and points at the inbox, so every marker below is sent after
        # that run has set the poll cursor.
        wake_sent = threading.Event()
        sub2_sent = threading.Event()

        def script(body, idx):
            b = json.dumps(body.get("contents", []))
            if "E2E_SUB_PROMPT" in b and not has_function_response(body):
                return text("E2E_SUB_DONE")
            if not body.get("tools"):
                return text("side")
            if "E2E_T2" in b and not has_function_response(body):
                sub2_sent.wait(30)  # the pending delivery must exist before the subagent
                return call("invoke_subagent", Prompt="E2E_SUB_PROMPT inspect nothing", TypeName="self",
                            Role="Probe helper", Workspace=lab.ws, Model="inherit")
            if "E2E_T2" in b:
                return text("E2E_T2_DONE")
            if "E2E_T1" in b and has_function_response(body):
                return text("E2E_T1_DONE")
            if "E2E_T1" in b:
                # Hold the reply until the wake marker is on the hub, so the
                # inbox tool call below reads it.
                wake_sent.wait(30)
                d = next((d for t in body.get("tools", []) for d in t.get("functionDeclarations", [])
                          if d["name"].endswith("vecnet_inbox")), {})
                return call(d.get("name", "mcp_vecnet-" + RX + "_vecnet_inbox"), toolSummary="Read the Vecnet inbox")
            return text(f"E2E_SIDE_{idx}")

        stub = Stub(script)
        proc = subprocess.Popen([AGY, "--input-format", "stream-json", "--output-format", "stream-json",
                                 "--dangerously-skip-permissions", "--settings", json.dumps({"modelProvider": "gemini"})],
                                stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                text=True, cwd=lab.ws, env=lab.env(stub, VECNET_CONFIG=rx_cfg))
        proc.stdin.write(json.dumps({"event": "user", "message": {"content": "E2E_T1 say hi"}}) + "\n")
        proc.stdin.flush()
        t1 = await_body(stub, "E2E_T1")
        wake_ok = t1 >= 0 and send_hub("MK_E2E_WAKE ping the root session", "--hold").returncode == 0
        wake_sent.set()
        tinb = await_tool_response(stub, "vecnet_inbox", 90)  # the request following the tool result
        inbox_resp = tool_response(stub.requests[tinb]["body"], "vecnet_inbox") if tinb >= 0 else None
        steps.check("4_mcp_inbox", wake_ok and tinb >= 0 and inbox_resp is not None
                    and "MK_E2E_WAKE" in json.dumps(inbox_resp),
                    request=tinb)
        time.sleep(1)  # let turn 1 finish before the mid-session message
        sub_sent = send_hub("MK_E2E_SUB mid-session for the next turn", "--hold").returncode == 0
        proc.stdin.write(json.dumps({"event": "user", "message": {"content": "E2E_T2 continue"}}) + "\n")
        proc.stdin.flush()
        t2 = await_body(stub, "E2E_T2", 90)
        # A delivery that lands mid-session is claimed and injected at the next
        # turn-start PreInvocation, inside the inert frame.
        t2hits = find_marker(stub.requests[t2]["body"], "MK_E2E_SUB") if t2 >= 0 else []
        steps.check("2_hook_delivery", sub_sent and t2 >= 0 and bool(t2hits)
                    and any(h.get("role") == "user" and h.get("system_message_frame") for h in t2hits)
                    and '<vecnet-inbox untrusted="true">' in (part_text_with(stub.requests[t2]["body"], "MK_E2E_SUB") or ""),
                    request=t2)
        sub2_ok = send_hub("MK_E2E_SUB2 must stay away from the subagent", "--hold").returncode == 0
        sub2_sent.set()
        # The subagent's own conversation has no root history, so "without the
        # root prompt marker" picks it out from the root's follow-up request,
        # which quotes the subagent prompt inside the tool result.
        tsub = await_body(stub, "E2E_SUB_PROMPT", 90, need_tools=False, without="E2E_T2")
        sub_body = stub.requests[tsub]["body"] if tsub >= 0 else {}
        steps.check("3_subagent_silent", sub2_ok and tsub >= 0
                    and "vecnet-inbox" not in json.dumps(sub_body.get("contents", []))
                    and "MK_E2E_SUB2" not in json.dumps(sub_body.get("contents", [])),
                    request=tsub)
        if await_body(stub, "E2E_SUB_DONE", 30, need_tools=False) < 0:
            time.sleep(3)  # the root's close-out request carries the subagent reply
        proc.stdin.close()
        proc.communicate(timeout=60)
        stub.close()
        stub = None

        # ---- TUI session: step 5 (print mode exits before a background result).
        # Every earlier marker was sent --hold: the relay's first vecnet_wait
        # starts at the acked cursor and an unacked wake backlog would wake it
        # at once, while hold stays inbox-only, so the wait really sleeps until
        # the one --wake message below.
        def script2(body, idx):
            b = json.dumps(body.get("contents", []))
            if "Vecnet: new inbox activity" in b:
                return text("E2E_WOKE_DONE")
            if "E2E_WAIT" in b and not has_function_response(body):
                d = next((d for t in body.get("tools", []) for d in t.get("functionDeclarations", [])
                          if d["name"].endswith("vecnet_wait")), {})
                if not d:
                    return text("E2E_MCP_LATE")
                return call(d["name"], timeout_seconds=150, toolSummary="Wait for Vecnet messages")
            if "E2E_WARM" in b and not has_function_response(body):
                return text("E2E_WARM_DONE")
            return text("E2E_TUI_DONE")

        stub2 = Stub(script2)
        tui = Tui(lab, stub2, {"toolPermission": "always-proceed"})
        tui.prompt("E2E_WARM")
        steps.check("5_tui_warm", tui.wait_for("E2E_WARM_DONE", 25))
        time.sleep(3)  # MCP connects lazily in the TUI; let the warm turn finish
        tui.prompt("E2E_WAIT_GO")
        waited = await_body(stub2, "E2E_WAIT")
        if waited >= 0 and not any(d.get("name", "").endswith("vecnet_wait")
                                   for t in stub2.requests[waited]["body"].get("tools", [])
                                   for d in t.get("functionDeclarations", [])):
            time.sleep(2)
            tui.prompt("E2E_WAIT_GO2")
            waited = await_body(stub2, "E2E_WAIT")
        task_bar = tui.wait_for("task", 10)
        time.sleep(1)
        with stub2.lock:
            n_before = len(stub2.requests)
            early = any("Vecnet: new inbox activity" in json.dumps(r["body"].get("contents", []))
                        for r in stub2.requests)
        t_send = time.time()
        wait_sent = send_hub("MK_E2E_WAIT wake the idle session", "--wake").returncode == 0
        woke = -1
        end = time.time() + 40
        while time.time() < end and woke < 0:
            with stub2.lock:
                for i, r in enumerate(list(stub2.requests)[n_before:], start=n_before):
                    if not r["body"].get("tools"):
                        continue
                    if "Vecnet: new inbox activity for profile" in json.dumps(r["body"].get("contents", [])):
                        woke = i
                        break
            time.sleep(0.2)
        hit = None
        if woke >= 0:
            hit = find_marker(stub2.requests[woke]["body"], "Vecnet: new inbox activity for profile")
        steps.check("5_wait_wakes_idle", wait_sent and not early and woke >= 0 and bool(hit)
                    and any(h.get("role") == "user" and h.get("system_message_frame") for h in hit),
                    request=woke, seconds_after_send=round(time.time() - t_send, 1) if woke >= 0 else None,
                    task_bar=task_bar, woke_before_send=early)
        rendered = tui.wait_for("E2E_WOKE_DONE", 20)
        steps.rows.setdefault("5_wait_wakes_idle", {})["reply_rendered"] = rendered

        # ---- step 6: nothing acked (an inbox read without ack replays them all)
        fin = vrun(vec, rx_env, "--net", RX, "--json", "inbox")
        inbox_json = json.dumps(last_json(fin.stdout))
        steps.check("6_no_ack", fin.returncode == 0
                    and all(mk in inbox_json for mk in ("MK_E2E_WAKE", "MK_E2E_WAIT", "MK_E2E_SUB2")))

        if steps.failed:
            return {"steps": steps.rows, "failed_steps": steps.failed}
        return {"steps": steps.rows}
    finally:
        if tui:
            tui.kill()
        if stub:
            stub.close()
        if stub2:
            stub2.close()
        if lab:
            # The relay (vecnet --net … mcp) can outlive a killed TUI; the hub
            # is stopped by its own pid below, never by this pattern.
            subprocess.run(["pkill", "-f", os.path.join(lab.root, "bin", "vecnet") + " --net "], capture_output=True)
        if hub_proc:
            hub_proc.terminate()
            try:
                hub_proc.wait(10)
            except subprocess.TimeoutExpired:
                hub_proc.kill()
        lab.cleanup()


PROBES = {
    "hooks": probe_hooks,
    "inject_variants": probe_inject_variants,
    "worktree": probe_worktree,
    "background_mcp": probe_background_mcp,
    "sidecar_agentapi": probe_sidecar_and_agentapi,
    "acp": probe_acp,
    "tui": probe_tui,
    "session_identity": probe_session_identity,
    "e2e": probe_e2e,
}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--probe", action="append", choices=sorted(PROBES))
    ap.add_argument("--out")
    a = ap.parse_args()
    vlab = Lab("ver")
    ver = subprocess.run([AGY, "--version"], capture_output=True, text=True, env={"PATH": os.environ["PATH"], "HOME": vlab.home}).stdout.strip()
    vlab.cleanup()
    doc = {"agy_version": ver, "platform": f"{platform.system()} {platform.machine()}",
           "note": "Disposable HOME + loopback Gemini API stub via GOOGLE_GEMINI_BASE_URL; sanitized (no IDs, paths, tokens)."}
    for name in a.probe or PROBES:
        try:
            doc[name] = PROBES[name]()
        except Exception as e:  # record, keep going
            doc[name] = {"error": scrub(f"{type(e).__name__}: {e}")}
    s = json.dumps(doc, indent=2)
    if a.out:
        with open(a.out, "w") as f:
            f.write(s + "\n")
    print(s)
    # A full run collects evidence and never fails the process; a probe named
    # explicitly is a verification and an error in it must fail the command.
    if a.probe and any(isinstance(doc.get(n), dict) and ("error" in doc[n] or doc[n].get("failed_steps"))
                       for n in a.probe):
        return 1


if __name__ == "__main__":
    sys.exit(main())
