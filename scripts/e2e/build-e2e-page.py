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
import json
import re
import sys
import tomllib
from pathlib import Path

RUN = Path(sys.argv[1]).resolve()
PAGE = Path(sys.argv[2]).resolve()
ASSETS = PAGE / "assets"
FRAMES = ASSETS / "frames"

PAGE.mkdir(parents=True, exist_ok=True)
FRAMES.mkdir(parents=True, exist_ok=True)

summary = json.loads((RUN / "evidence/98-summary.json").read_text())
results = summary["results"]
meta = json.loads((RUN / "evidence/00-run-meta.json").read_text())


# Fail closed on credential-shaped content in any published artifact. The one
# config exhibit is projected onto public fields, even for historical captures
# whose capture-time redaction was incomplete.
CREDENTIAL = re.compile(
    rb"vn_(?:peer|operator|enrol)_[A-Za-z0-9_-]+|"
    rb"sk-ant-[A-Za-z0-9_-]+|-----BEGIN [A-Z ]*PRIVATE KEY-----"
)


def publish(src: Path, dst: Path) -> None:
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
    elif dst.parent == FRAMES:
        # Some frames show the stub echoing startup context before the actual
        # conversation. Keep the visible canary run, not that context.
        lines = data.splitlines(keepends=True)
        pointer = next((i for i, line in enumerate(lines) if b"Vecnet message available." in line), None)
        if pointer is not None:
            start = next((i for i in range(pointer - 1, -1, -1)
                          if b"READY: e2e receiver idle" in lines[i]), pointer - 2)
            data = b"".join(lines[max(0, start):])
    if CREDENTIAL.search(data):
        raise SystemExit(f"refusing to publish credential-shaped data in {src.name}")
    dst.write_bytes(data)


index = json.loads((RUN / "frames/index.json").read_text())
run_start = datetime.datetime.fromisoformat(meta["started"]).timestamp()

daemon_lines = []
log_path = RUN / "evidence/50-daemon.log"
if log_path.exists():
    for line in log_path.read_text(errors="replace").splitlines():
        m = re.search(r'"at":"([^"]+)"', line)
        at = datetime.datetime.fromisoformat(m.group(1).replace("Z", "+00:00")).timestamp() if m else None
        daemon_lines.append({"t": at - run_start if at else None, "line": line})

frame_data = []
used_labels = set()
for f in index:
    if f["file"] != f"frame-{f['n']:04d}.txt":
        raise SystemExit("unexpected frame filename")
    frame_path = FRAMES / f["file"]
    publish(RUN / "frames" / f["file"], frame_path)
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
chain_keys = ("claude_delivery_scope_admitted", "claude_delivery_claim_held",
              "claude_delivery_claim_reserved", "claude_delivery_claim_attempting",
              "claude_delivery_disposition", "claude_delivery_uncertain_restart",
              "claude_delivery_blocked_pending_limit", "Killed")
chain = [d for d in daemon_lines if any(k in d["line"] for k in chain_keys) and d["t"] is not None]

for ev_name in ("00-run-meta.json", "10-hub-init.txt", "11-hub-serve.log", "20-enrol-sender.txt",
                "21-enrol-claude-rx.txt", "22-config.toml.redacted",
                "23-runtime-stage.txt" if "enrollment" in results else "23-hook-install.txt",
                "30-daemon-env.txt", "31-stub-observed.jsonl", "32-runtime-snapshot.json",
                "40-send-wake.json", "40b-send-wake-cli.txt", "41-hub-delivery.json",
                "50-daemon.log", "51-daemon-chain.txt", "60-transcript-rows.json",
                "62-claude-deliveries.json", "70-hold-send.json", "71-hold-inbox.json",
                "73-hub-acks.json", "74-hub-delivery-states.json", "80-crash-send.json",
                "81-crash-attempting.txt", "82-crash-claim.json", "90-cap-send.json",
                "91-cap-seed.json", "92-cap-deliveries.json", "93-cap-status.json",
                "98-summary.json", "99-teardown.txt"):
    publish(RUN / "evidence" / ev_name, ASSETS / ev_name)

player_data = {"frames": frame_data, "chain": chain}
(ASSETS / "player-data.json").write_text(json.dumps(player_data))

pos, hold, crash, cap = (results["positive"], results["hold"], results["crash"], results["cap"])
E = html.escape


def chain_rows():
    enrol = ('''<tr><td>0 · automatic enrollment</td><td>real in-session CLI enroll registers the live root and stages the embedded runtime through a private scheduler fixture</td>
        <td><a href="assets/21-enrol-claude-rx.txt">enrol output</a> · <a href="assets/23-runtime-stage.txt">runtime stage</a></td></tr>'''
             if "enrollment" in results else "")
    return f"""
      {enrol}
      <tr><td>1 · hub send receipt</td><td>sender CLI + hub row</td>
        <td><a href="assets/40-send-wake.json">send --json</a> · <a href="assets/41-hub-delivery.json">hub delivery row</a></td></tr>
      <tr><td>2 · daemon admission</td><td>binding registered, live/policy/version-qualified</td>
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


page = f"""<!doctype html>
<html lang="en-AU" data-theme="system" data-design="modest">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="description" content="End-to-end canary proof of Vecnet's native Claude Code delivery chain: hub send to native inbox pointer with durable claims, captured against a real Claude Code 2.1.322 session.">
  <title>Vecnet native delivery canary — end-to-end proof</title>
  <link rel="icon" href="/examples/html-system/favicon.svg" type="image/svg+xml">
  <script src="/assets/js/theme-init.js"></script>
  <link rel="stylesheet" href="/assets/css/document.css">
  <script defer src="/assets/vendor/lucide/lucide.min.js"></script>
  <script defer src="/assets/js/theme-controls.js"></script>
  <script defer src="/assets/js/icons.js"></script>
</head>
<body>
  <a class="skip-link" href="#main-content">Skip to content</a>
  <div class="l-shell">
    <header class="doc-header">
      <div class="doc-header__identity">
        <p class="doc-header__kicker">Vecnet · Claude delivery chain · task 9 canary</p>
        <h1 class="type-hero">Native pointer delivery, proven end to end.</h1>
        <p class="doc-header__summary">One disposable run inside a loopback-only network namespace: a real Claude&nbsp;Code 2.1.322 session receives a Vecnet hub message as a native cross-session pointer, fetches it through the packaged CLI, and never acknowledges the hub. Every link is a captured artifact.</p>
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
          <p>Run <code>{E(summary['nonce'])}</code> at {E(meta['started'])}, commit <code>{E(results['build']['commit'][:12])}</code>, Claude Code <strong>{E(results['build']['claude'])}</strong> (the qualified lab baseline). The whole chain — disposable loopback hub, freshly enrolled peers, the standalone delivery daemon under <code>VECNET_CLAUDE_DELIVERY_CANARY=1</code>, the Claude session and its scripted loopback model stub — ran inside one bubblewrap network namespace with loopback as its only interface. No paid provider was reachable at any point.</p>
          <p><code>CLAUDE_CODE_VERSION</code> was exported by the harness for the disposable session and its enrollment child from its own <code>claude --version</code> observation; the qualified-baseline check never guesses a version, and production activation stays gated behind the canary opt-in.</p>
          <p><strong>Run results:</strong> released/attempted · hold inbox-only · crash uncertain, no resend · pending limit latched · zero hub acks.</p>
        </section>
        <section class="workspace-section" id="chain" aria-labelledby="chain-title">
          <h2 id="chain-title">The chain</h2>
          <div class="u-scroll-region" tabindex="0" role="region" aria-label="Delivery proof chain table">
          <table class="data-table data-table--compact">
            <thead><tr><th scope="col">Link</th><th scope="col">What it proves</th><th scope="col">Artifact</th></tr></thead>
            <tbody>{chain_rows()}</tbody>
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
            <dt>Hold stays inbox-only</dt>
            <dd>Delivery <code>{E(hold['delivery_id'])}</code> quarantined <code>{E(hold['state'])}</code>/<code>{E(hold['disposition'])}</code>; no native pointer in any captured frame, no transcript row, still readable in the inbox (<a href="assets/71-hold-inbox.json">inbox read</a>), never acked.</dd>
            <dt>SIGKILL between <code>attempting</code> and the write</dt>
            <dd>The canary crash seam killed the daemon after <code>attempting</code> was durable and before any byte could leave (exit 137). No captured frame contains a native pointer for this control message. The replacement host recovered the claim to <code>{E(crash['final_state'])}</code> (<code>{E(crash['reason'])}</code>) and never resent: the parsed claim is unchanged after a later reconcile tick and the transcript holds zero rows for the marker (<a href="assets/82-crash-claim.json">claim record</a>).</dd>
            <dt>The pending limit with 129 unresolved claims</dt>
            <dd>With 128 unresolved claims seeded for the live scope (plus the crash control's uncertain claim), the new wake message <code>{E(cap['delivery_id'])}</code> was never staged: pending {E(str(cap['pending']))}, <code>blocked_pending_limit</code> latched, nothing staged for that delivery and no captured frame contains its native pointer (<a href="assets/92-cap-deliveries.json">deliveries</a> · <a href="assets/93-cap-status.json">status</a>).</dd>
            <dt>No acknowledgement, anywhere</dt>
            <dd>The hub's <code>acks</code> table is empty and all four deliveries remain <code>queued</code>: the adapter, the hooks, the inbox reads and the fetch never ack (<a href="assets/73-hub-acks.json">acks</a> · <a href="assets/74-hub-delivery-states.json">states</a>).</dd>
          </dl>
        </section>
        <section class="workspace-section prose" id="evidence" aria-labelledby="evidence-title">
          <h2 id="evidence-title">Evidence &amp; teardown</h2>
          <p>Every published evidence artifact sits beside this page under <code>assets/</code>; the committed index with SHA-256 digests is <code>docs/research/evidence/claude-delivery-e2e-2026-11-19.json</code> in the vecnet repository. Full daemon log: <a href="assets/50-daemon.log">50-daemon.log</a>; enrolments and receipts: <a href="assets/20-enrol-sender.txt">sender</a> · <a href="assets/21-enrol-claude-rx.txt">receiver</a> · <a href="assets/40b-send-wake-cli.txt">send output</a>; transcript rows: <a href="assets/60-transcript-rows.json">60-transcript-rows.json</a>.</p>
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

# The suite checker requires exactly the shared head assets and prohibits
# script bodies in suite documents. Keep the bespoke player in its own frame.
(PAGE / "player.html").write_text("""<!doctype html>
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
(PAGE / "player.css").write_text("""html { color-scheme: light dark; }
body { margin: 0; padding: 10px; background: Canvas; color: CanvasText; font: 13px system-ui, sans-serif; }
.controls { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; margin-bottom: 8px; }
.controls button { font: inherit; padding: 3px 9px; }
.panes { display: grid; grid-template-columns: minmax(0, 3fr) minmax(0, 2fr); gap: 10px; }
.panes pre { border: 1px solid GrayText; border-radius: 4px; height: 445px; margin: 0;
  overflow: auto; padding: 8px; font: 12px/1.35 ui-monospace, monospace; }
#log { white-space: pre-wrap; overflow-wrap: anywhere; }
@media (max-width: 560px) {
  .panes { grid-template-columns: minmax(0, 1fr); }
  .panes pre { height: 183px; }
}
""")
(PAGE / "player.js").write_text("""(async () => {
  const byId = (id) => document.getElementById(id);
  const [prev, next, play, scrub, tag, pane, log] =
    ['prev', 'next', 'play', 'scrub', 'tag', 'frame', 'log'].map(byId);
  const parentTheme = window.parent.document.documentElement;
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

(PAGE / "index.html").write_text(page)
print(f"wrote {PAGE / 'index.html'} ({len(page)} bytes), {len(frame_data)} frames, "
      f"{len(chain)} chain lines")
