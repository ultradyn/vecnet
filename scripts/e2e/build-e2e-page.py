#!/usr/bin/env python3
"""Build the served-gallery proof page from an E2E run directory.

Usage: python3 scripts/e2e/build-e2e-page.py <run-dir> <page-dir>

Copies the captured artifacts (pane frames, daemon chain, receipts, claims,
hub queries, transcript rows) into <page-dir>/assets and renders index.html
on the modest-workspace shell: an annotated chain-link table, a stepped frame
player for the tmux captures with a synchronized daemon-log panel, and the
negative-control outcomes.
"""

import datetime
import html
import importlib.util
import json
import re
import shutil
import sys
import tempfile
import tomllib
from argparse import ArgumentParser
from contextlib import contextmanager
from pathlib import Path

# Load the live harness's pure assertions without executing the canary (also
# works with python3 -I, where the script directory isn't on sys.path).
spec = importlib.util.spec_from_file_location("harness", Path(__file__).with_name("claude-delivery-e2e.py"))
harness = importlib.util.module_from_spec(spec)
spec.loader.exec_module(harness)

E = html.escape
CANONICAL_ASSETS = """  <link rel="icon" href="/examples/html-system/favicon.svg" type="image/svg+xml">
  <script src="/assets/js/theme-init.js"></script>
  <link rel="stylesheet" href="/assets/css/document.css">
  <script defer src="/assets/vendor/lucide/lucide.min.js"></script>
  <script defer src="/assets/js/theme-controls.js"></script>
  <script defer src="/assets/js/icons.js"></script>"""


class ProofFailure(Exception):
    pass


@contextmanager
def gate(name):
    try:
        yield
    except (harness.Fail, OSError, AttributeError, IndexError, KeyError, TypeError, ValueError) as exc:
        raise ProofFailure(name) from exc


def require(condition):
    if not condition:
        raise ValueError("evidence did not satisfy the gate")


def verify_proof(run):
    """Recheck published claims from the saved artifacts, not summary flags alone."""
    def text(name):
        return (run / "evidence" / name).read_text()

    def doc(name):
        return harness.parse_json_output(text(name))

    def read_json(name):
        return json.loads(text(name))

    with gate("run summary"):
        summary = read_json("98-summary.json")
        results = summary["results"]
        pos, hold, crash, cap = (results[name] for name in ("positive", "hold", "crash", "cap"))
        backlog, off, kill = (results[name] for name in ("backlog", "canary_off", "kill_switch"))
        meta = read_json("00-run-meta.json")
        require(meta["nonce"] == summary["nonce"])

    with gate("no provision"):
        require(meta["provisions"] == [] and meta["canary_opt_in"] == harness.CANARY_ENABLE
                and "claude_code_version_provision" not in meta)
        environ = text("24-claude-environ-names.txt").splitlines()
        require("CLAUDE_CODE_VERSION present: false" in environ and "CLAUDE_CODE_VERSION" not in environ
                and "CLAUDE_CONFIG_DIR" in environ)
        daemon_env = text("30-daemon-env.txt").splitlines()
        require(not any(line.startswith(harness.CANARY_ENV + "=") for line in daemon_env))
        require(text("25-canary-enable.txt").startswith("$ " + harness.CANARY_ENABLE + "\n"))
        status = doc("34-status-canary.json")
        harness.check_no_provision(b"CLAUDE_CONFIG_DIR=x\0", status)
        require(results["no_provision"] == {"claude_code_version_present": False, "mode": "canary",
                                            "canary_opt_in": True, "build_check": "disabled"})

    with gate("ordered lifecycle"):
        controls = ((hold, "70-hold-send.json"), (crash, "80-crash-send.json"), (cap, "90-cap-send.json"),
                    (backlog, "26-backlog-send.json"), (off, "94-off-send.json"), (kill, "100-kill-send.json"))
        expected = {}
        message_ids = set()
        for result, filename in ((pos, "40-send-wake.json"), *controls):
            receipt = doc(filename)
            delivery, = receipt["deliveries"]
            require(receipt["message_id"] == result["message_id"]
                    and delivery["delivery_id"] == result["delivery_id"] and delivery["state"] == "queued")
            dly, msg = result["delivery_id"], result["message_id"]
            require(dly and msg and dly not in expected and msg not in message_ids)
            expected[dly] = {"dly_id": dly, "state": "queued", "recipient": delivery["recipient"]}
            message_ids.add(msg)
        delivery_id = pos["delivery_id"]
        recipient = expected[delivery_id]["recipient"]
        require(read_json("41-hub-delivery.json") == [expected[delivery_id]])
        scope, generation = (results["admission"][key] for key in ("binding_key", "generation"))
        binding, = [b for b in doc("32-runtime-snapshot.json")["bindings"]
                    if b["binding_key"] == scope and b["generation"] == generation]
        require(binding["state"] == "registered" and binding["scope"]["caller"] == recipient
                and all(binding["admission"][key] is True
                        for key in ("live", "policy", "version_qualified", "canary", "witness"))
                and binding["admission"]["build_check"] == "disabled"
                and harness.PEER_ID.match(recipient))
        events = [json.loads(line) for line in text("50-daemon.log").splitlines() if line.startswith("{")]
        lifecycle = harness.ordered_lifecycle(events, scope, generation, delivery_id)
        require([json.loads(line) for line in text("51-daemon-chain.txt").splitlines()] == lifecycle)
        state = ("released", "attempted")
        require((lifecycle[-1]["exit"], lifecycle[-1]["state"], lifecycle[-1]["disposition"]) == (0, *state))
        require(status["generation"] == generation)

    with gate("baseline before any send"):
        base = read_json("33-baseline.json")
        baselined = base["baselined"]
        require(baselined["msg"] == "claude_delivery_scope_baselined"
                and (baselined["scope"], baselined["generation"]) == (scope, generation))
        first = next(i for i, e in enumerate(events) if e.get("msg") == "claude_delivery_scope_baselined")
        require(events[first] == baselined)
        claims_before = [e for e in events[:first] if e.get("msg") in harness.CLAIM_EVENTS]
        require(not claims_before and events.index(lifecycle[1]) > first)
        require(base["cli_head_events"] == [] and base["backlog_delivery_id"] == backlog["delivery_id"])
        (be, bs), (se, ss), (he, hs) = (harness.cursor_seq(base[k]) for k in
                                        ("backlog_cursor", "stored_cursor", "cli_head_next_cursor"))
        require(be == se == he and bs <= ss <= hs)
        require(results["baseline"]["stored_cursor"] == base["stored_cursor"])

    with gate("pre-admission backlog"):
        require(not any(backlog["delivery_id"] in json.dumps(e) for e in events))
        require(backlog["pointer_frames"] == 0)

    with gate("rejected-cursor rebaseline"):
        rb = read_json("75-rebaseline.json")
        require(rb["corrupted_to"] == harness.FOREIGN_CURSOR
                and rb["real_cli_refusal"].startswith("cursor_purged: ")
                and rb["stored_after"] != harness.FOREIGN_CURSOR
                and harness.cursor_seq(rb["stored_after"])[0] == harness.cursor_seq(rb["stored_before"])[0])
        require([e["msg"] for e in rb["events"]] == results["rebaseline"]["events"]
                == ["claude_delivery_cursor_rejected", "claude_delivery_scope_baselined"]
                and all(e["scope"] == scope for e in rb["events"]))
        require(all(e in events for e in rb["events"]))
        claims = doc("62-claude-deliveries.json")
        claim, = [c for c in claims["deliveries"] if c["delivery_id"] == delivery_id]
        require(claims["pending_count"] == 0 and claim["message_id"] == pos["message_id"]
                and (claim["scope"], claim["generation"]) == (scope, generation)
                and (claim["state"], claim["disposition"]) == (pos["state"], pos["disposition"]) == state)

    with gate("exactly-one fetch"):
        rows = read_json("60-transcript-rows.json")
        # The original summary records the run path used in the transcript,
        # which remains the same when a saved evidence directory is moved.
        fetch_cmd = f"{summary['run_dir']}/vec.sh inbox --json --limit 8"
        # The historical summary doesn't store the marker. Recover it from
        # the one ID-matched result, then use it to join the pane evidence.
        selected, tool_id = harness.transcript_proof(rows, pos["message_id"], fetch_cmd, "WAKE-E2E-")
        result = next(b for b in selected[-1]["message"]["content"] if b.get("tool_use_id") == tool_id)
        messages = [m for m in harness.inbox_messages(result["content"]) if m["message_id"] == pos["message_id"]]
        marker, separator, _ = messages[0]["content"]["text"].partition(":")
        require(separator and re.fullmatch(r"WAKE-E2E-[\w-]+", marker))
        # The selected transcript rows alone cannot prove absence of a second
        # fetch. Check every saved stub request too; repeated API history is
        # not another call, but distinct IDs (or duplicates in one request) are.
        observed = set()
        for line in text("31-stub-observed.jsonl").splitlines():
            request = json.loads(json.loads(line)["body"])
            tool_results = [(i, b) for i, message in enumerate(request.get("messages", []))
                            if message["role"] == "user" and isinstance(message.get("content"), list)
                            for b in message["content"] if b.get("type") == "tool_result"]
            fetches = harness.successful_fetches(tool_results, pos["message_id"])
            require(len(fetches) <= 1)
            for _, tid, matched in fetches:
                require(matched == messages)
                observed.add(tid)
        require(observed == {tool_id} and pos["tool_use_id"] == tool_id
                and pos["enqueues"] == pos["fetch_pairs"] == 1
                and pos["lifecycle"] == [row["msg"] for row in lifecycle])

    with gate("control frames"):
        index = json.loads((run / "frames/index.json").read_text())
        frame_names = [f"frame-{i:04d}.txt" for i in range(len(index))]
        require(index and len(index) == summary["frames"]
                and [f["n"] for f in index] == list(range(len(index)))
                and [f["file"] for f in index] == frame_names
                and set(frame_names) == {p.name for p in (run / "frames").glob("frame-*.txt")})
        paths = [run / "frames" / name for name in frame_names]
        for control, prefix in ((hold, "HOLD"), (crash, "CRASH"), (cap, "CAP"), (off, "OFF"), (kill, "KILL")):
            harness.verify_control_frames(paths, control["message_id"], f"{prefix}-E2E-")
            require(all(control["message_id"] not in json.dumps(row) for row in rows))
        harness.check_backlog(paths, rows, events, backlog["message_id"], backlog["delivery_id"])

    with gate("positive frames"):
        frames = [path.read_text() for path in paths]
        require(max(frame.count(harness.PREVIEW) for frame in frames) == 1
                and any(pos["message_id"] in frame and marker in frame for frame in frames))

    with gate("hub acknowledgements"):
        require(read_json("73-hub-acks.json") == [] and results["no_ack"]["acks"] == 0)
        states = read_json("74-hub-delivery-states.json")
        require(len(states) == len(expected) and {row["dly_id"]: row for row in states} == expected)

    with gate("control final states"):
        final = doc("92-cap-deliveries.json")
        by_id = {claim["delivery_id"]: claim for claim in final["deliveries"]}
        held = by_id[hold["delivery_id"]]
        require(held["message_id"] == hold["message_id"]
                and (held["state"], held["disposition"]) == (hold["state"], hold["disposition"]) == ("held", "held"))
        require(any(m["message_id"] == hold["message_id"] and m["priority"] == "hold"
                    for m in harness.inbox_messages(text("71-hold-inbox.json"))))
        crash_doc = read_json("82-crash-claim.json")
        at_crash, after = crash_doc["at_crash"], crash_doc["after_restart"]
        require(at_crash["delivery_id"] == crash["delivery_id"] and at_crash["state"] == "attempting"
                and after == by_id[crash["delivery_id"]] and after["message_id"] == crash["message_id"]
                and after["state"] == crash["final_state"] == "uncertain"
                and after["reason"] == crash["reason"] == "restart_may_have_written"
                and crash_doc["transcript_rows"] == 0)
        attempt_line, exit_line = text("81-crash-attempting.txt").splitlines()
        attempts = [row for row in events if row.get("deliveryId") == crash["delivery_id"]
                    and row.get("msg") == "claude_delivery_claim_attempting"]
        require(attempts == [json.loads(attempt_line)] and exit_line == "daemon exit rc=137")
        seed = read_json("91-cap-seed.json")
        status = doc("93-cap-status.json")
        require((seed["scope"], seed["generation"], seed["seeded"]) == (scope, generation, 128)
                and status["generation"] == generation and cap["delivery_id"] not in by_id
                and final["pending_count"] == status["pending_count"] == cap["pending"] == 129
                and all(d[k] is True for d, k in ((final, "blocked_pending_limit"),
                        (status, "blocked_pending_limit"), (cap, "blocked"))))

    with gate("canary off"):
        off_doc, off_status = doc("95-off-deliveries.json"), doc("96-off-status.json")
        require(harness.check_canary_off(off_doc, off_status, off["delivery_id"], off["fenced"])
                == {k: off[k] for k in ("mode", "canary_opt_in", "fenced", "claims_for_wake")})
        fenced = [json.loads(line) for line in text("97-off-fence.txt").splitlines()]
        require(any(e["deliveryId"] == off["fenced"] and e["state"] == "released" and e["scope"] == scope
                    for e in fenced) and all(e in events for e in fenced))
        require(not any(off["delivery_id"] in json.dumps(e) for e in events))

    with gate("kill switch"):
        kill_status = doc("101-kill-status.json")
        require((kill_status["mode"], kill_status["canary_opt_in"], kill_status["build_check"])
                == ("legacy", True, "disabled") and "has not admitted" in kill_status["reason"])
        require(harness.CANARY_ENV + "=0" in text("102-kill-env.txt").splitlines())
        require(kill["adapter_events"] == 0 and not any(kill["delivery_id"] in json.dumps(e) for e in events))

    with gate("enrollment and teardown"):
        if "enrollment" in results:
            require("credentials saved; claude setup registered:" in text("21-enrol-claude-rx.txt")
                    and "Automatic enrollment staged the embedded delivery runtime" in text("23-runtime-stage.txt"))
        teardown = results["teardown"]
        require(teardown["private_server_stopped"] is True and teardown["default_server_strays"] == 0
                and "private server: no server running" in text("99-teardown.txt")
                and "default server vecnet-test-* sessions: 0" in text("99-teardown.txt"))

    return summary, meta, index


# Fail closed on credential-shaped content in any published artifact. The one
# config exhibit is projected onto public fields, even for historical captures
# whose capture-time redaction was incomplete.
CREDENTIAL = re.compile(
    rb"vn_(?:peer|operator|enrol)_[A-Za-z0-9_-]+|"
    rb"sk-ant-[A-Za-z0-9_.-]+|dummy-e2e-key|-----BEGIN [A-Z ]*PRIVATE KEY-----"
)
API_KEY_ECHO = re.compile(rb"(ANTHROPIC_API_KEY:\s*)\S+")


def publish(src: Path, dst: Path, *, is_frame=False) -> None:
    data = src.read_bytes()
    if dst.name == "22-config.toml.redacted":
        profiles = tomllib.loads(data.decode())["profiles"]
        lines = ["[profiles]"]
        for name in ("claude-rx", "sender"):
            profile = profiles[name]
            lines.append(f"  [profiles.{name}]")
            for key in ("hub", "hub_pubkey", "peer_id", "allow_insecure_http"):
                lines.append(f"    {key} = {json.dumps(profile[key])}")
            lines.append('    secret = "<redacted>"')
        data = ("\n".join(lines) + "\n").encode()
    elif dst.name == "31-stub-observed.jsonl":
        # Publish proof-bearing messages, not the startup context, tool schemas,
        # account metadata, or system prompts from the local capture.
        observations = []
        for line in data.splitlines():
            row = json.loads(line)
            messages = []
            for message in json.loads(row["body"]).get("messages", []):
                content = message.get("content", [])
                if isinstance(content, str):
                    content = [{"type": "text", "text": content}]
                keep = [block for block in content if (
                    (block["type"] == "text" and message["role"] == "user"
                     and block.get("text", "").startswith(("Another Claude session sent a message:",
                                                              "READY: acknowledge and stay idle.",
                                                              "SETUP: enroll the disposable receiver.")))
                    or (block["type"] == "tool_use" and block.get("name") == "Bash"
                        and "vec.sh inbox" in block.get("input", {}).get("command", ""))
                    or (block["type"] == "tool_result"
                        and "WAKE-E2E-" in json.dumps(block.get("content")))
                )]
                if keep:
                    messages.append({"role": message["role"], "content": keep})
            observations.append(json.dumps({"path": row["path"], "body": json.dumps({"messages": messages})}))
        data = ("\n".join(observations) + "\n").encode()
    elif is_frame:
        # Claude's API-key dialog echoes the key's masked form; the lab key is
        # a placeholder, but no key-shaped value is ever published.
        data = API_KEY_ECHO.sub(rb"\1<redacted>", data)
        # Some frames show the stub echoing startup context before the actual
        # conversation. Keep the visible canary run, not that context.
        lines = data.splitlines(keepends=True)
        pointer = next((i for i, line in enumerate(lines) if b"Vecnet message available." in line), None)
        if pointer is not None:
            start = next((i for i in range(pointer - 1, -1, -1)
                          if b"READY: e2e receiver idle" in lines[i]), pointer - 2)
            data = b"".join(lines[max(0, start):])
    if CREDENTIAL.search(data):
        raise harness.Fail(f"refusing to publish credential-shaped data in {src.name}")
    dst.write_bytes(data)



def chain_rows(results):
    enrol = ('''<tr><td>0 · automatic enrollment</td><td>real in-session CLI enroll registers the live root and stages the embedded runtime through a private scheduler fixture</td>
        <td><a href="assets/21-enrol-claude-rx.txt">enrol output</a> · <a href="assets/23-runtime-stage.txt">runtime stage</a></td></tr>'''
             if "enrollment" in results else "")
    return f"""
      {enrol}
      <tr><td>0a · no provision</td><td>no <code>CLAUDE_CODE_VERSION</code> in the disposable Claude process; daemon env carries no canary variable</td>
        <td><a href="assets/24-claude-environ-names.txt">environ names</a> · <a href="assets/30-daemon-env.txt">daemon env</a></td></tr>
      <tr><td>0b · per-profile opt-in</td><td><code>vecnet --net claude-rx claude canary enable</code> before the daemon starts; status <code>mode:"canary"</code>, <code>canary_opt_in:true</code>, <code>build_check:"disabled"</code></td>
        <td><a href="assets/25-canary-enable.txt">canary enable</a> · <a href="assets/34-status-canary.json">claude status --json</a></td></tr>
      <tr><td>0c · new-arrivals baseline</td><td><code>claude_delivery_scope_baselined</code> at a real hub head before any send; the real CLI <code>inbox --json --since head</code> agrees</td>
        <td><a href="assets/33-baseline.json">baseline</a> · <a href="assets/33b-head-read-cli.txt">head read</a></td></tr>
      <tr><td>1 · hub send receipt</td><td>sender CLI + hub row</td>
        <td><a href="assets/40-send-wake.json">send --json</a> · <a href="assets/41-hub-delivery.json">hub delivery row</a></td></tr>
      <tr><td>2 · daemon admission</td><td>binding registered, live/policy/canary/witness; build check disabled</td>
        <td><a href="assets/32-runtime-snapshot.json">runtime-snapshot</a> · <code>claude_delivery_scope_admitted</code> in <a href="assets/51-daemon-chain.txt">chain log</a></td></tr>
      <tr><td>3 · reserved → attempting</td><td>durable claim before any byte leaves</td>
        <td><code>claude_delivery_claim_reserved</code> / <code>claude_delivery_claim_attempting</code></td></tr>
      <tr><td>4 · helper exit disposition</td><td>exit 0 → released/attempted (write completed, never presentation proof)</td>
        <td><code>claude_delivery_disposition</code> · <a href="assets/62-claude-deliveries.json">claude deliveries --json</a></td></tr>
      <tr><td>5 · pointer in the live session</td><td>pane frames show the native pointer preview</td>
        <td>player frames below</td></tr>
      <tr><td>6 · model fetch via the packaged CLI</td><td>stub-issued Bash <code>vec.sh inbox</code>; fetched marker in pane and request</td>
        <td>frames · <a href="assets/31-stub-observed.jsonl">stub observations</a></td></tr>
      <tr><td>7 · transcript rows</td><td>one enqueue, one peer-origin turn and one matching fetch pair</td>
        <td><a href="assets/60-transcript-rows.json">extracted rows</a></td></tr>
      <tr><td>8 · no hub ack</td><td>acks table empty; every delivery still <code>queued</code></td>
        <td><a href="assets/73-hub-acks.json">acks</a> · <a href="assets/74-hub-delivery-states.json">delivery states</a></td></tr>"""


def build_page(run, page_dir, summary, meta, index):
    results = summary["results"]
    assets = page_dir / "assets"
    frames = assets / "frames"
    frames.mkdir(parents=True)
    run_start = datetime.datetime.fromisoformat(meta["started"]).timestamp()

    daemon_lines = []
    log_path = run / "evidence/50-daemon.log"
    if log_path.exists():
        for line in log_path.read_text(errors="replace").splitlines():
            m = re.search(r'"at":"([^"]+)"', line)
            at = datetime.datetime.fromisoformat(m.group(1).replace("Z", "+00:00")).timestamp() if m else None
            daemon_lines.append({"t": at - run_start if at else None, "line": line})

    frame_data = []
    used_labels = set()
    for f in index:
        if f["file"] != f"frame-{f['n']:04d}.txt":
            raise harness.Fail("unexpected frame filename")
        frame_path = frames / f["file"]
        publish(run / "frames" / f["file"], frame_path, is_frame=True)
        text = frame_path.read_text(errors="replace")
        labels = []
        for marker, name in (
                ("READY: e2e receiver idle", "session ready (stub model online)"),
                ("Vecnet message available.", "native pointer preview visible"),
                ("(end of fetched inbox data)", "stub-issued inbox fetch complete"),
                (f"Find {results['positive']['message_id']} in thread", "expanded pointer with message ID")):
            if marker in text and name not in used_labels:
                labels.append(name)
                used_labels.add(name)
        frame_data.append({"n": f["n"], "t": f["t"], "file": "assets/frames/" + f["file"],
                           "label": " · ".join(labels) or None})

    # Per-frame daemon context: chain lines whose timestamp precedes the frame.
    chain_keys = ("claude_delivery_scope_admitted", "claude_delivery_scope_baselined",
                  "claude_delivery_cursor_rejected", "claude_delivery_claim_fenced", "claude_delivery_claim_held",
                  "claude_delivery_claim_reserved", "claude_delivery_claim_attempting",
                  "claude_delivery_disposition", "claude_delivery_uncertain_restart",
                  "claude_delivery_blocked_pending_limit", "Killed")
    chain = [d for d in daemon_lines if any(k in d["line"] for k in chain_keys) and d["t"] is not None]

    for ev_name in ("00-run-meta.json", "10-hub-init.txt", "11-hub-serve.log", "20-enrol-sender.txt",
                    "21-enrol-claude-rx.txt", "22-config.toml.redacted",
                    "23-runtime-stage.txt" if "enrollment" in results else "23-hook-install.txt",
                    "24-claude-environ-names.txt", "25-canary-enable.txt", "26-backlog-send.json",
                    "30-daemon-env.txt", "31-stub-observed.jsonl", "32-runtime-snapshot.json",
                    "33-baseline.json", "33b-head-read-cli.txt", "34-status-canary.json",
                    "75-rebaseline.json", "94-off-send.json", "95-off-deliveries.json", "96-off-status.json",
                    "97-off-fence.txt", "100-kill-send.json", "101-kill-status.json", "102-kill-env.txt",
                    "40-send-wake.json", "40b-send-wake-cli.txt", "41-hub-delivery.json",
                    "50-daemon.log", "51-daemon-chain.txt", "60-transcript-rows.json",
                    "62-claude-deliveries.json", "70-hold-send.json", "71-hold-inbox.json",
                    "73-hub-acks.json", "74-hub-delivery-states.json", "80-crash-send.json",
                    "81-crash-attempting.txt", "82-crash-claim.json", "90-cap-send.json",
                    "91-cap-seed.json", "92-cap-deliveries.json", "93-cap-status.json",
                    "98-summary.json", "99-teardown.txt"):
        publish(run / "evidence" / ev_name, assets / ev_name)

    player_data = {"frames": frame_data, "chain": chain}
    (assets / "player-data.json").write_text(json.dumps(player_data))

    pos, hold, crash, cap = (results["positive"], results["hold"], results["crash"], results["cap"])

    page = f"""<!doctype html>
    <html lang="en-AU" data-theme="system" data-design="modest">
    <head>
      <meta charset="utf-8">
      <meta name="viewport" content="width=device-width, initial-scale=1">
      <meta name="description" content="End-to-end canary proof of Vecnet's native Claude Code delivery chain: hub send to native inbox pointer with durable claims, captured against a real Claude Code session with no test-env provision.">
      <title>Vecnet native delivery canary — end-to-end proof</title>
{CANONICAL_ASSETS}
    </head>
    <body>
      <a class="skip-link" href="#main-content">Skip to content</a>
      <div class="l-shell">
        <header class="doc-header">
          <div class="doc-header__identity">
            <p class="doc-header__kicker">Vecnet · Claude delivery chain · Phase 2 acceptance</p>
            <h1 class="type-hero">Provision-free native pointer delivery, proven end to end.</h1>
            <p class="doc-header__summary">One disposable run inside a loopback-only network namespace: the per-profile canary record alone admits a real Claude&nbsp;Code session, which receives a Vecnet hub message as a native cross-session pointer, fetches it through the packaged CLI, and never acknowledges the hub. Every link is a captured artifact.</p>
          </div>
          <div class="doc-header__tools">
            <div class="theme-control" role="group" aria-label="Colour theme">
              <button type="button" data-theme-choice="system" aria-pressed="true">System</button>
              <button type="button" data-theme-choice="light" aria-pressed="false">Paper</button>
              <button type="button" data-theme-choice="dark" aria-pressed="false">Ink</button>
            </div>
          </div>
        </header>
        <div class="workspace">
          <nav class="workspace__nav" aria-label="Document sections">
            <ul>
              <li><a href="#overview">Overview</a></li>
              <li><a href="#chain">The chain</a></li>
              <li><a href="#clip">Session clip</a></li>
              <li><a href="#controls">Negative controls</a></li>
              <li><a href="#evidence">Evidence</a></li>
            </ul>
          </nav>
          <main class="workspace__main" id="main-content" tabindex="-1">
            <section class="workspace-section prose" id="overview" aria-labelledby="overview-title">
              <h2 id="overview-title">Overview</h2>
              <p>Run <code>{E(summary['nonce'])}</code> at {E(meta['started'])}, commit <code>{E(results['build']['commit'][:12])}</code>, Claude Code <strong>{E(results['build']['claude'])}</strong> (observed only: build checking is parked, so any build is admitted and none is hashed). The whole chain — disposable loopback hub, freshly enrolled <code>peer_&lt;ULID&gt;</code> identities, the standalone delivery daemon with no canary variable in its environment, the Claude session and its scripted loopback model stub — ran inside one bubblewrap network namespace with loopback as its only interface. No paid provider was reachable at any point.</p>
              <p><strong>No provisions.</strong> Nothing was exported for the session: <code>CLAUDE_CODE_VERSION</code> is absent from the disposable Claude process environment, and the only gate is the durable record written by <code>{E(harness.CANARY_ENABLE)}</code> before the daemon started.</p>
              <p><strong>Run results:</strong> baseline before any send · released/attempted · backlog never pointered · hold inbox-only · rejected cursor rebaselined once · crash uncertain, no resend · pending limit latched · canary off drains to legacy · kill switch builds no adapter · zero hub acks.</p>
              <p><strong>Documented limit:</strong> this proof exercises the idle-path injection only — the pointer lands while the session sits idle (a queue-operation enqueue plus a peer-origin turn in the transcript evidence). Busy-turn <code>queued_command</code> placement is a separately gated experiment and is neither exercised nor claimed here.</p>
            </section>
            <section class="workspace-section" id="chain" aria-labelledby="chain-title">
              <h2 id="chain-title">The chain</h2>
              <div class="u-scroll-region" tabindex="0" role="region" aria-label="Delivery proof chain table">
              <table class="data-table data-table--compact">
                <thead><tr><th scope="col">Link</th><th scope="col">What it proves</th><th scope="col">Artifact</th></tr></thead>
                <tbody>{chain_rows(results)}</tbody>
              </table>
              </div>
              <p class="type-small u-muted">Delivery <code>{E(pos['delivery_id'])}</code> for message <code>{E(pos['message_id'])}</code>: state <code>{E(pos['state'])}</code>, disposition <code>{E(pos['disposition'])}</code> — a completed write, deliberately never a presentation proof.</p>
            </section>
            <section class="workspace-section" id="clip" aria-labelledby="clip-title">
              <h2 id="clip-title">Session clip</h2>
              <p>Captured from the private tmux server at ~0.6&nbsp;s cadence ({len(frame_data)} frames). Step through the run, or autoplay; the right panel shows the daemon's claim-lifecycle log lines as they appear against the timeline.</p>
              <div data-design-exception="The terminal player is a standalone interactive figure with its own local scripts and responsive layout.">
                <iframe title="Session frame player and daemon lifecycle log" src="player.html"
                  sandbox="allow-scripts allow-same-origin" width="960" height="540"
                  style="display:block;width:100%;border:1px solid var(--color-rule-major);border-radius:6px"></iframe>
              </div>
              <p class="type-small u-muted">Annotated frames: {', '.join(f'<a href="{d["file"]}">#{d["n"]} {E(d["label"])}</a>' for d in frame_data if d['label']) or 'none'}.</p>
            </section>
            <section class="workspace-section prose" id="controls" aria-labelledby="controls-title">
              <h2 id="controls-title">Negative controls</h2>
              <dl>
                <dt>Pre-admission backlog is never pointered</dt>
                <dd>Wake <code>{E(results['backlog']['delivery_id'])}</code> was sent before the canary record existed. The first admission baselined at the hub head past it (<a href="assets/33-baseline.json">baseline</a>), it never entered the claim lifecycle, and no frame or transcript row carries a pointer for it. It stays on the legacy inbox path, so it remains readable and unacked.</dd>
                <dt>A rejected stored cursor rebaselines</dt>
                <dd>The scope's stored cursor was rewritten to the foreign-epoch <code>{E(harness.FOREIGN_CURSOR)}</code>, which the real CLI refuses with <code>cursor_purged</code>. The restarted daemon logged one <code>claude_delivery_cursor_rejected</code>, then one <code>claude_delivery_scope_baselined</code> at <code>{E(results['rebaseline']['stored_after'])}</code>, and no retry loop followed (<a href="assets/75-rebaseline.json">record</a>).</dd>
                <dd>Delivery <code>{E(hold['delivery_id'])}</code> quarantined <code>{E(hold['state'])}</code>/<code>{E(hold['disposition'])}</code>; no native pointer in any captured frame, no transcript row, still readable in the inbox (<a href="assets/71-hold-inbox.json">inbox read</a>), never acked.</dd>
                <dt>SIGKILL between <code>attempting</code> and the write</dt>
                <dd>The canary crash seam killed the daemon after <code>attempting</code> was durable and before any byte could leave (exit 137). No captured frame contains a native pointer for this control message. The replacement host recovered the claim to <code>{E(crash['final_state'])}</code> (<code>{E(crash['reason'])}</code>) and never resent: the parsed claim is unchanged after a later reconcile tick and the transcript holds zero rows for the marker (<a href="assets/82-crash-claim.json">claim record</a>).</dd>
                <dt>The pending limit with 129 unresolved claims</dt>
                <dd>With 128 unresolved claims seeded for the live scope (plus the crash control's uncertain claim), the new wake message <code>{E(cap['delivery_id'])}</code> was never staged: pending {E(str(cap['pending']))}, <code>blocked_pending_limit</code> latched, nothing staged for that delivery and no captured frame contains its native pointer (<a href="assets/92-cap-deliveries.json">deliveries</a> · <a href="assets/93-cap-status.json">status</a>).</dd>
                <dt>Canary off</dt>
                <dd>After <code>claude canary disable</code> (and with the harness's own 128 cap seeds removed, so nothing hides behind the latch), a seeded <code>reserved</code> row drained to <code>released</code>/<code>fenced_scope_not_admitted</code>. The wake <code>{E(results['canary_off']['delivery_id'])}</code> got no claim and no pointer, and status reports <code>mode:"legacy"</code> with <code>canary_opt_in:false</code> (<a href="assets/95-off-deliveries.json">deliveries</a> · <a href="assets/96-off-status.json">status</a>).</dd>
                <dt><code>VECNET_CLAUDE_DELIVERY_CANARY=0</code> kill switch</dt>
                <dd>With the canary re-enabled but the daemon started with <code>=0</code>, no delivery adapter was constructed: there were zero <code>claude_delivery_*</code> log lines, and wake <code>{E(results['kill_switch']['delivery_id'])}</code> got no claim and no pointer. Status stays <code>legacy</code>, with the opt-in present and the host "has not admitted" reason (<a href="assets/101-kill-status.json">status</a>).</dd>
                <dt>No acknowledgement, anywhere</dt>
                <dd>The hub's <code>acks</code> table is empty and all {len(results['no_ack']['deliveries'])} deliveries remain <code>queued</code>: the adapter, the hooks, the inbox reads and the fetch never ack (<a href="assets/73-hub-acks.json">acks</a> · <a href="assets/74-hub-delivery-states.json">states</a>).</dd>
              </dl>
            </section>
            <section class="workspace-section prose" id="evidence" aria-labelledby="evidence-title">
              <h2 id="evidence-title">Evidence &amp; teardown</h2>
              <p>Every published evidence artifact sits beside this page under <code>assets/</code>; the committed index with SHA-256 digests is <code>docs/research/evidence/claude-delivery-e2e-phase2-2026-11-21.json</code> in the vecnet repository. Full daemon log: <a href="assets/50-daemon.log">50-daemon.log</a>; enrolments and receipts: <a href="assets/20-enrol-sender.txt">sender</a> · <a href="assets/21-enrol-claude-rx.txt">receiver</a> · <a href="assets/40b-send-wake-cli.txt">send output</a>; transcript rows: <a href="assets/60-transcript-rows.json">60-transcript-rows.json</a>.</p>
              <p>Teardown was verified in the same run: the <code>vecnet-test-*</code> tmux session was killed on its private server, the server itself reported <em>no server running</em>, and the default server listed no <code>vecnet-test-*</code> sessions (<a href="assets/99-teardown.txt">teardown record</a>). Raw lab state: <strong>{E(results.get("teardown", {}).get("raw_disposition", "not disposed by this historical harness"))}</strong>.</p>
              <details class="disclosure-row">
                <summary>How the run was isolated</summary>
                <div class="disclosure-row__content"><p>Fresh hub (<code>vecnet-hub init</code> + loopback listen), two freshly enrolled throwaway peers, a private HOME for the registry/ledger, a private Claude config root with an explicit private messaging socket, and a bubblewrap network namespace whose only interface is loopback. The model backend is a scripted Messages-API stub on loopback; stray vendor probes were answered by a lab-CA TLS catch-all proxy so nothing ever left the namespace. No user hub identity, user session, or paid provider was involved.</p></div>
              </details>
            </section>
          </main>
        </div>
        <footer class="conclusion">
          <p class="type-small u-muted">Generated from run <code>{E(str(summary['run_dir']))}</code> by <code>scripts/e2e/build-e2e-page.py</code>.</p>
          <p class="type-small"><a href="#main-content">Back to content</a></p>
        </footer>
      </div>
    </body>
    </html>
    """

    # Keep the standalone document on the shared shell. The interactive
    # figure retains its own runtime and styles in a separate sandboxed frame.
    (page_dir / "player.html").write_text(f"""<!doctype html>
<html lang="en-AU" data-theme="system" data-design="modest">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Session frame player</title>
{CANONICAL_ASSETS}
</head>
<body>
  <a class="skip-link" href="#main-content">Skip to content</a>
  <main id="main-content" tabindex="-1" data-design-exception="The specialised terminal player fills its own interactive figure."
    style="padding:8px;height:100dvh;display:grid;grid-template-rows:auto minmax(0,1fr);gap:4px">
    <h1 class="type-small">Session frame player</h1>
    <iframe title="Frame controls and synchronized logs" src="player-widget.html"
      sandbox="allow-scripts allow-same-origin" style="display:block;width:100%;height:100%;border:0"></iframe>
  </main>
</body>
</html>
""")
    (page_dir / "player-widget.html").write_text("""<!doctype html>
<html lang="en-AU">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Session frame player</title>
  <link rel="stylesheet" href="player.css">
  <script defer src="player.js"></script>
</head>
<body>
  <main aria-label="Session frame player">
    <div class="controls">
      <button type="button" id="prev" disabled>‹ Prev</button>
      <button type="button" id="next" disabled>Next ›</button>
      <button type="button" id="play" disabled>Autoplay</button>
      <label for="scrub">Frame</label>
      <input id="scrub" type="range" min="0" value="0" disabled>
      <output id="tag" for="scrub">Loading frames…</output>
    </div>
    <div class="panes">
      <pre id="frame" tabindex="0" aria-label="Claude session pane capture"></pre>
      <pre id="log" tabindex="0" aria-label="Daemon claim lifecycle log">–</pre>
    </div>
  </main>
</body>
</html>
""")
    (page_dir / "player.css").write_text("""html { color-scheme: light dark; }
body { margin: 0; background: Canvas; color: CanvasText; font: 13px system-ui, sans-serif; }
main { height: 100dvh; display: grid; grid-template-rows: auto minmax(0, 1fr); gap: 8px; }
.controls { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
.controls button { font: inherit; padding: 3px 9px; }
.panes { display: grid; grid-template-columns: minmax(0, 3fr) minmax(0, 2fr); gap: 10px; }
.panes pre { border: 1px solid GrayText; border-radius: 4px; min-height: 0; margin: 0;
  overflow: auto; padding: 8px; font: 12px/1.35 ui-monospace, monospace; }
#log { white-space: pre-wrap; overflow-wrap: anywhere; }
@media (max-width: 560px) {
  .panes { grid-template-columns: minmax(0, 1fr); grid-template-rows: repeat(2, minmax(0, 1fr)); }
}
""")
    (page_dir / "player.js").write_text("""(async () => {
  const byId = (id) => document.getElementById(id);
  const [prev, next, play, scrub, tag, pane, log] =
    ['prev', 'next', 'play', 'scrub', 'tag', 'frame', 'log'].map(byId);
  // Follow the report when embedded and the standalone shell otherwise.
  const parentTheme = (window.parent === window.top ? window.parent : window.top).document.documentElement;
  const followTheme = () => {
    const value = parentTheme.dataset.theme;
    document.documentElement.style.colorScheme = value === 'light' || value === 'dark' ? value : 'light dark';
  };
  new MutationObserver(followTheme).observe(parentTheme, {attributes: true, attributeFilter: ['data-theme']});
  followTheme();
  const load = async (url) => {
    const response = await fetch(url);
    if (!response.ok) throw new Error(`Could not load ${url} (${response.status})`);
    return response;
  };
  const {frames, chain} = await (await load('assets/player-data.json')).json();
  const cache = new Map();
  let index = 0, timer = null;
  const stop = () => {
    clearInterval(timer);
    timer = null;
    play.textContent = 'Autoplay';
  };
  const show = async (n) => {
    index = Math.max(0, Math.min(frames.length - 1, n));
    const frame = frames[index];
    scrub.value = index;
    prev.disabled = index === 0;
    next.disabled = index === frames.length - 1;
    if (!cache.has(frame.file)) cache.set(frame.file, load(frame.file).then(response => response.text()));
    try {
      const text = await cache.get(frame.file);
      if (frame !== frames[index]) return;
      pane.textContent = text;
      const lines = text.split('\\n');
      const pointer = lines.findIndex(line => line.includes('Vecnet message available.'));
      pane.scrollTop = pointer >= 2 ? (pointer - 2) * parseFloat(getComputedStyle(pane).lineHeight) : 0;
      pane.scrollLeft = 0;
      tag.textContent = `Frame ${frame.n} / ${frames.at(-1).n} · t+${frame.t}s` + (frame.label ? ` · ${frame.label}` : '');
      log.textContent = chain.filter(event => event.t <= frame.t + 0.6).map(event => event.line).join('\\n') || '–';
      log.scrollTop = log.scrollHeight;
    } catch (error) {
      cache.delete(frame.file);
      if (frame === frames[index]) tag.textContent = error.message;
    }
  };
  const step = (n) => { stop(); void show(n); };
  scrub.max = frames.length - 1;
  scrub.disabled = play.disabled = false;
  prev.onclick = () => step(index - 1);
  next.onclick = () => step(index + 1);
  scrub.oninput = () => step(Number(scrub.value));
  play.onclick = () => {
    if (timer !== null) { stop(); return; }
    if (index === frames.length - 1) void show(0);
    play.textContent = 'Pause';
    timer = setInterval(() => {
      if (index >= frames.length - 1) stop();
      else void show(index + 1);
    }, 700);
  };
  await show(0);
})().catch(error => { document.getElementById('tag').textContent = error.message; });
""")

    (page_dir / "index.html").write_text(page)
    return len(frame_data), len(chain)


OUTPUTS = ("index.html", "player.html", "player-widget.html", "player.css", "player.js", "assets")


def remove_outputs(page):
    for name in OUTPUTS:
        path = page / name
        if path.is_dir() and not path.is_symlink():
            shutil.rmtree(path)
        else:
            path.unlink(missing_ok=True)


def failure_page(page, failed_gate):
    page.mkdir(parents=True, exist_ok=True)
    remove_outputs(page)
    (page / "index.html").write_text(f"""<!doctype html>
<html lang="en-AU" data-theme="system" data-design="modest">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Proof failed — Vecnet delivery canary</title>
{CANONICAL_ASSETS}
</head>
<body>
  <a class="skip-link" href="#main-content">Skip to content</a>
  <main class="l-shell prose" id="main-content" tabindex="-1">
    <h1>Proof failed</h1>
    <p>Failed gate: {E(failed_gate)}. No evidence or success claims were published.</p>
  </main>
</body>
</html>
""")


def main():
    parser = ArgumentParser(description=__doc__)
    parser.add_argument("run_dir", type=Path)
    parser.add_argument("page_dir", type=Path)
    args = parser.parse_args()
    run, page = args.run_dir.resolve(), args.page_dir.resolve()
    if run == page or run in page.parents or page in run.parents:
        parser.error("run and publication directories must be separate")
    try:
        summary, meta, index = verify_proof(run)
        with gate("evidence rendering"):
            page.parent.mkdir(parents=True, exist_ok=True)
            with tempfile.TemporaryDirectory(dir=page.parent, prefix=".e2e-page-") as tmp:
                stage = Path(tmp)
                frames, chain = build_page(run, stage, summary, meta, index)
                page.mkdir(exist_ok=True)
                remove_outputs(page)
                for name in (*OUTPUTS[1:], "index.html"):
                    (stage / name).replace(page / name)
    except ProofFailure as exc:
        # Diagnostics may include raw evidence; only publish the gate name.
        print(f"Proof failed: {exc}: {exc.__cause__}", file=sys.stderr)
        failure_page(page, str(exc))
        return 1
    print(f"wrote {page / 'index.html'}: {frames} frames, {chain} chain lines")
    return 0


if __name__ == "__main__":
    sys.exit(main())
