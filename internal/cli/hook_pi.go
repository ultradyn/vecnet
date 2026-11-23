package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
)

// The pi coding agent hook runtime. pi has no shell-hook configuration:
// lifecycle hooks are TypeScript extensions loaded into the pi process.
// `vecnet hook install --client pi` writes one owned extension
// (hookinstall_pi.go) that spawns `vecnet hook --client pi` for two events
// and pipes it one JSON line on stdin:
//
//	{"event":"user-prompt"|"post-tool","session_id":ID,"session_file":PATH,"pid":N,"mode":M}
//
// session_id and session_file come from ctx.sessionManager, the same
// identity pi exports to its bash tool as PI_SESSION_ID and PI_SESSION_FILE
// (pi 1.6.0 docs/environment-variables.md). pi never sets those variables on
// its own process, so an extension cannot pass them on through the
// environment; a vecnet run from the agent's bash tool does see them
// (hook rebind). mode is ctx.mode: "tui" for the terminal UI and "rpc" for
// an RPC client (an editor integration) are the interactive top-level
// sessions. "json" (`pi --mode json -p`, how pi's example subagent spawns
// children) and "print" (`pi -p`, and in-process pi-subagents sessions,
// which pi binds with no mode and so defaults to print) are not.
//
// The output is {"additionalContext": TEXT}, or nothing on a silent run; the
// extension injects TEXT as a custom message before the turn, or appends it
// to the finished tool's result.

// piInput is the payload the installed extension writes on stdin.
type piInput struct {
	Event       string `json:"event"`
	SessionID   string `json:"session_id"`
	SessionFile string `json:"session_file"`
	PID         int    `json:"pid"`
	Mode        string `json:"mode"`
}

// piHookOutput encodes context for the installed pi extension.
func piHookOutput(context string) []byte {
	var raw bytes.Buffer
	encoder := json.NewEncoder(&raw)
	// The context is already HTML-escaped by inert; keep those bytes.
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(struct {
		AdditionalContext string `json:"additionalContext"`
	}{context})
	return raw.Bytes()
}

// piEvent maps one pi hook run onto the delivery path's event semantics and
// normalises its session identity. ok is false for a run that must stay
// silent and touch nothing: unusable stdin, no session id, a run from a
// session that is not a top-level interactive one (json or print mode, which
// is how subagents run), or an event the
// installed extension never sends. A --event flag, when present, must agree
// with the payload.
//
// user-prompt (pi's before_agent_start for a prompt typed or sent by a
// client) may bind; post-tool (tool_result) never binds and is rate limited.
func piEvent(o opts, raw []byte, e error) (event, session string, pid int, ok bool) {
	var in piInput
	if e != nil || len(raw) > 1<<20 || json.Unmarshal(raw, &in) != nil {
		return "", "", 0, false
	}
	event = in.Event
	if o.has("event") {
		if event != "" && event != o.str("event") {
			return "", "", 0, false
		}
		event = o.str("event")
	}
	if event != "user-prompt" && event != "post-tool" {
		return "", "", 0, false
	}
	switch in.Mode {
	case "tui", "rpc":
	default:
		return "", "", 0, false
	}
	if strings.TrimSpace(in.SessionID) == "" {
		return "", "", 0, false
	}
	return event, hookSession(in.SessionID), in.PID, true
}

// piHookProc is the pi process that ran this hook. The extension spawns
// vecnet directly, without a shell, so pi is this process's parent — or, on
// Linux, its grandparent when `vecnet` on PATH is the npm package's Node
// launcher. The payload names pi's own pid and must match that ancestor, so
// a hand-run hook from an unrelated terminal never borrows a live pi's
// identity. This is not authentication: the agent's bash tool runs commands
// as pi's children too, so a payload it forges can pass, exactly as a
// Claude Code Bash command can forge a hook payload with CLAUDE_PID; the
// hook trusts the agent host as Claude's does. Unknown stays the zero
// value, which never matches.
func piHookProc(payloadPID int) hookProc {
	if payloadPID <= 1 {
		return hookProc{}
	}
	pid := os.Getppid()
	if pid != payloadPID {
		pid = procParent(pid)
	}
	if pid != payloadPID {
		return hookProc{}
	}
	host, _ := os.Hostname()
	return hookProc{PID: pid, Start: procStart(pid), Host: host}
}
