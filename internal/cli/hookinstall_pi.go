package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/XertroV/vecnet/internal/platform"
)

// The pi installer. pi (the pi coding agent) has no hook configuration file:
// lifecycle hooks are TypeScript extensions, auto-discovered from
// <cwd>/.pi/extensions/ (project-local, loaded once the project is trusted)
// and ~/.pi/agent/extensions/ (global). The installer owns exactly one file,
// vecnet-<profile>.ts, marked by its first line; it replaces only a file
// that carries that marker and never edits pi's settings.json.

// piExtensionMarker is the first line of every extension the installer
// writes. A file without it is the user's, and is never replaced.
const piExtensionMarker = "// vecnet-managed: written by `vecnet hook install --client pi`; rerun the installer to change it."

// piExtensionName is the owned extension's file name for profile.
func piExtensionName(profile string) string { return "vecnet-" + profile + ".ts" }

// piExtensionTemplate is the extension body; @CONFIG@ is replaced by a JSON
// object literal (bin, args, env). The hook protocol is hook_pi.go's.
//
// before_agent_start runs before a prompt's agent run and may return one
// message, which pi stores in the session and sends to the model. Only runs
// started by a user prompt (typed, or sent by an RPC client or an
// extension's sendUserMessage) count as a turn start; runs started by a
// custom message are left alone.
//
// tool_execution_end fires once for every finished tool call, without
// blocking the agent: the hook runs in the background (one at a time; the
// hook itself rate limits post-tool polls to one per 5 s), and its context
// is steered into the conversation after the current tool calls, before
// the next model request — or, if the run has ended meanwhile, stored for
// the next prompt. The hook has already recorded the deliveries as shown,
// so a steer the user discards (Esc clears pi's queue) must not be lost:
// each steer carries an id in details, and when the run settles any steer
// that never reached the session is stored again while idle.
//
// Only interactive sessions (ctx.mode tui or rpc) ever run the hook; it
// refuses every other mode anyway (hook_pi.go).
const piExtensionTemplate = piExtensionMarker + `
//
// vecnet inbox delivery for pi, profile @PROFILE@. New vecnet messages for the
// profile arrive at the start of a prompt and after tool calls, framed as
// untrusted data. The profile binds to the first interactive pi session
// (terminal UI or RPC) that sends a prompt; subagents, print mode
// (pi -p) and JSON mode (pi --mode json) never bind or receive anything.
// Docs: docs/connectors/pi.md in the vecnet repository.
import { spawn } from "node:child_process";

const VECNET = @CONFIG@;
const TIMEOUT_MS = 4000;

function runHook(event, ctx) {
  let payload;
  try {
    payload = JSON.stringify({
      event,
      session_id: String(ctx.sessionManager.getSessionId() ?? ""),
      session_file: String(ctx.sessionManager.getSessionFile() ?? ""),
      pid: process.pid,
      mode: String(ctx.mode ?? ""),
    }) + "\n";
  } catch {
    return Promise.resolve("");
  }
  return new Promise((resolve) => {
    let out = "";
    let settled = false;
    let timer;
    const finish = (text) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolve(text);
    };
    let child;
    try {
      child = spawn(VECNET.bin, [...VECNET.args, "--event", event], {
        stdio: ["pipe", "pipe", "ignore"],
        env: { ...process.env, ...VECNET.env },
        windowsHide: true,
      });
    } catch {
      finish("");
      return;
    }
    timer = setTimeout(() => {
      try { child.kill(); } catch {}
      finish("");
    }, TIMEOUT_MS);
    child.on("error", () => finish(""));
    child.stdout.setEncoding("utf8");
    child.stdout.on("data", (chunk) => { out += chunk; });
    child.on("close", () => {
      try {
        const parsed = JSON.parse(out);
        finish(typeof parsed?.additionalContext === "string" ? parsed.additionalContext : "");
      } catch {
        finish("");
      }
    });
    child.stdin.on("error", () => {});
    child.stdin.end(payload);
  });
}

const interactive = (ctx) => ctx.mode === "tui" || ctx.mode === "rpc";

export default function vecnetHook(pi) {
  pi.on("before_agent_start", async (event, ctx) => {
    if (event.messageType !== "user" || !interactive(ctx)) return;
    const text = await runHook("user-prompt", ctx);
    if (!text) return;
    return { message: { customType: "vecnet", content: text, display: true } };
  });

  let inFlight = false;
  let seq = 0;
  const pending = new Map(); // steer id -> message, until seen in the session
  pi.on("tool_execution_end", (_event, ctx) => {
    if (inFlight || !interactive(ctx)) return;
    inFlight = true;
    runHook("post-tool", ctx)
      .then((text) => {
        if (!text) return;
        const id = process.pid + "-" + Date.now() + "-" + (++seq);
        const message = { customType: "vecnet", content: text, display: true, details: { vecnetSteer: id } };
        pending.set(id, message);
        pi.sendMessage(message, { deliverAs: "steer" });
      })
      .catch(() => {})
      .finally(() => { inFlight = false; });
  });

  pi.on("agent_settled", (_event, ctx) => {
    if (pending.size === 0) return;
    try {
      for (const entry of ctx.sessionManager.getEntries()) {
        const id = entry?.type === "custom_message" ? entry.details?.vecnetSteer : undefined;
        if (id) pending.delete(id);
      }
    } catch {}
    // Idle and without triggerTurn: stored, and sent with the next prompt.
    for (const message of pending.values()) {
      try { pi.sendMessage(message); } catch {}
    }
    pending.clear();
  });
}
`

// piExtensionConfig is the VECNET object the extension spawns from.
type piExtensionConfig struct {
	Bin  string            `json:"bin"`
	Args []string          `json:"args"`
	Env  map[string]string `json:"env"`
}

// renderPiExtension is the complete extension for profile. VECNET_CONFIG,
// when set at install time, is baked in as an absolute path so the hook
// reads the installer's config whatever environment pi starts in.
func renderPiExtension(bin, profile string, getenv func(string) string) ([]byte, error) {
	cfg := piExtensionConfig{Bin: bin, Args: []string{"hook", "--client", "pi", "--profile", profile}, Env: map[string]string{}}
	if conf := getenv("VECNET_CONFIG"); conf != "" {
		if abs, err := filepath.Abs(conf); err == nil {
			conf = abs
		}
		cfg.Env["VECNET_CONFIG"] = conf
	}
	var raw bytes.Buffer
	encoder := json.NewEncoder(&raw)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(cfg); err != nil {
		return nil, err
	}
	body := strings.Replace(piExtensionTemplate, "@CONFIG@", strings.TrimSuffix(raw.String(), "\n"), 1)
	body = strings.Replace(body, "@PROFILE@", profile, 1)
	return []byte(body), nil
}

// piAgentDir is pi's global config directory: PI_CODING_AGENT_DIR, else
// ~/.pi/agent (pi docs/environment-variables.md).
func piAgentDir(getenv func(string) string) (string, error) {
	if dir := getenv("PI_CODING_AGENT_DIR"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".pi", "agent"), nil
}

// piTarget resolves the extension path: --settings PATH (a .ts/.js file, or
// a directory to put vecnet-<profile>.ts in), --global (pi's global
// extensions directory), or by default <cwd>/.pi/extensions, the only
// project directory pi loads extensions from.
func (a *app) piTarget(o opts, profile, cwd string) (path string, explicit bool, err error) {
	name := piExtensionName(profile)
	switch {
	case o.has("settings"):
		path = o.str("settings")
		if path == "" {
			return "", false, usageErr("hook install: --settings must not be empty")
		}
		if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
			path = filepath.Join(path, name)
		} else if ext := filepath.Ext(path); ext != ".ts" && ext != ".js" {
			return "", false, usageErr("hook install: --client pi --settings takes an extension file (.ts or .js) or an existing directory")
		}
		fmt.Fprintf(a.stderr, "extension target %s (explicit --settings)\n", path)
		return path, true, nil
	case o.has("global"):
		dir, err := piAgentDir(a.getenv)
		if err != nil {
			return "", false, err
		}
		path = filepath.Join(dir, "extensions", name)
		fmt.Fprintf(a.stderr, "extension target %s (global; every pi session loads it)\n", path)
		return path, false, nil
	}
	path = filepath.Join(cwd, ".pi", "extensions", name)
	fmt.Fprintf(a.stderr, "extension target %s (project-local: pi loads it for sessions started in %s, once the project is trusted)\n", path, cwd)
	return path, false, nil
}

// installPiHooks writes the owned extension. Without --write it prints a
// diff and writes nothing. A default project target must not be a symlink
// and, inside a git repository, must be provably untracked; an existing
// file is replaced only when it carries piExtensionMarker.
func (a *app) installPiHooks(o opts, bin, profile string) error {
	cwd, err := os.Getwd()
	if err != nil && !o.has("settings") {
		return err
	}
	// pi resolves its project directory physically; so does git's
	// top-level, which the untracked check compares against.
	if c, cerr := platform.Canonical(cwd); cerr == nil {
		cwd = c
	}
	path, explicit, err := a.piTarget(o, profile, cwd)
	if err != nil {
		return err
	}
	project := !explicit && !o.has("global")
	if o.has("write") && project {
		root, fallback, unknown := a.projectTargetRoot(cwd)
		if unknown != nil {
			return fmt.Errorf("hook install: cannot tell whether %s is tracked by git (repository discovery failed: %s); pass --settings %s to write it anyway", path, hookGitReason(unknown), path)
		}
		if !fallback {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return fmt.Errorf("hook install: cannot place %s inside the repository at %s; pass --settings %s to write it anyway", path, root, path)
			}
			rel = filepath.ToSlash(rel)
			if err := a.checkUntracked(root, rel, path, "; to keep this file personal, list "+rel+" in .git/info/exclude"); err != nil {
				return err
			}
		}
	}
	if o.has("write") && !explicit {
		what := "the project .pi/extensions directory"
		if o.has("global") {
			what = "pi's global extensions directory"
		}
		if err := defaultSettingsRedirect(path, what); err != nil {
			return err
		}
		// A symlinked .pi would redirect the write, and the untracked check
		// above, somewhere else.
		if project {
			if err := symlinkSettingsRefused(filepath.Join(cwd, ".pi"), what, path); err != nil {
				return err
			}
		}
	}
	if others := piOtherProfiles(filepath.Dir(path), profile); len(others) > 0 {
		a.warn("%s already has vecnet extensions for profile(s) %s; every pi session that loads them runs all of them. Session binding keeps each profile's deliveries in one session, and a session that runs several profiles' hooks binds none of them until its agent runs `vecnet hook rebind --profile P`. Launch each session with its own profile, e.g. VECNET_PROFILE=%s pi, so the other profiles' hooks stay silent there", filepath.Dir(path), strings.Join(others, ", "), profile)
	}
	body, err := renderPiExtension(bin, profile, a.getenv)
	if err != nil {
		return err
	}
	target, err := platform.Canonical(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		// Do not replace a dangling symlink with an ordinary file.
		if info, statErr := os.Lstat(path); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return err
		}
		target = path
	}
	mode := os.FileMode(0644)
	if runtime.GOOS == "windows" {
		mode = 0666
	}
	existing, err := os.ReadFile(target)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		if !bytes.HasPrefix(existing, []byte(piExtensionMarker+"\n")) {
			return fmt.Errorf("hook install: %s exists and was not written by vecnet; move it aside or pass --settings with another path", path)
		}
		info, err := os.Stat(target)
		if err != nil {
			return err
		}
		mode = info.Mode().Perm()
	}
	if bytes.Equal(existing, body) {
		fmt.Fprintf(a.stderr, "unchanged %s\n", path)
		a.piAdvice(project)
		return nil
	}
	if !o.has("write") {
		if err := printHookDiff(a.stdout, path, existing, body); err != nil {
			return err
		}
		fmt.Fprintf(a.stderr, "would write %s; rerun with --write\n", path)
		a.piAdvice(project)
		return nil
	}
	if err := writeHookSettings(target, body, mode); err != nil {
		return err
	}
	fmt.Fprintf(a.stderr, "wrote %s (before_agent_start, tool_execution_end)\n", path)
	if conf := a.getenv("VECNET_CONFIG"); conf != "" {
		fmt.Fprintf(a.stderr, "pi: the extension's hook reads VECNET_CONFIG=%s, but the agent's own vecnet commands (reply, ack) see only pi's environment: start pi with the same VECNET_CONFIG\n", conf)
	}
	a.piAdvice(project)
	return nil
}

// piOtherProfiles lists the profiles of other vecnet-managed extensions in
// dir, inert for display.
func piOtherProfiles(dir, profile string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var found []string
	for _, e := range entries {
		name := e.Name()
		other, ok := strings.CutPrefix(strings.TrimSuffix(name, ".ts"), "vecnet-")
		if !ok || !strings.HasSuffix(name, ".ts") || other == profile || e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.HasPrefix(raw, []byte(piExtensionMarker+"\n")) {
			continue
		}
		if p := inert(other, 64); !slices.Contains(found, p) {
			found = append(found, p)
		}
	}
	slices.Sort(found)
	return found
}

// piAdvice prints the installer's stderr advice.
func (a *app) piAdvice(project bool) {
	if project {
		fmt.Fprintln(a.stderr, "pi: project extensions load only from the directory pi starts in, once you trust the project; start pi there (or install with --global)")
	}
	fmt.Fprintln(a.stderr, "pi: running sessions pick up the extension after /reload; the profile binds to the first interactive session that sends a prompt")
}
