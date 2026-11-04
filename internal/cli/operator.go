package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/x/term"

	"github.com/XertroV/vecnet/internal/envelope"
)

// Bare runs (PROTOCOL §13.7). A run is bare when it names no profile: no
// --net (or hook --profile) and no $VECNET_PROFILE. The config file's
// legacy `default` key no longer selects a profile for it. A bare run by an
// agent is refused; a bare run by a human at a terminal speaks as the
// operator, through the profile named by `operator_profile`, and labels its
// sends "Operator on <host> CLI" (plus "via ssh"). A run that names an
// operator profile explicitly is labelled too, and marked agent_suspected
// when it looks like an agent's.
//
// The detection is a convenience, not a security boundary: anything that
// can run vecnet as the user can read the config file and its secrets. It
// exists so an agent that forgets --net fails loudly instead of speaking
// as whichever profile happened to be the default.

// agentMarkers are environment variables an agent harness sets in the
// environment of the commands it runs for the model, in check order.
// Sources, checked 2026-11-04 (see docs/OPERATOR.md for the list):
//   - Claude Code 2.1.318: observed in a Bash tool child's environment.
//   - Codex CLI 0.166.0, Gemini CLI 0.44.1 (GEMINI_CLI_IDENTIFICATION_ENV_VAR),
//     Cursor agent (CURSOR_AGENT:"1" in its exec env), OpenCode (sets AGENT,
//     OPENCODE and AI_AGENT on its shell tool), Antigravity
//     ("ANTIGRAVITY_AGENT=1"), Grok 1.0.70 (`export GROK_AGENT=1` in its
//     shell snapshot), Muse 1.4.14 (MUSE_THREAD_ID, MUSE_TOOL_USE_ID beside
//     its exec env), Goose 1.25.0 (GOOSE_TERMINAL=1, AGENT=goose): strings
//     in the installed binaries and bundles.
//   - pi 1.4.0: docs/environment-variables.md (PI_CODING_AGENT=true,
//     AI_AGENT=pi).
//   - @vercel/detect-agent 1.2.7 (src/index.ts): AI_AGENT, CURSOR_AGENT,
//     GEMINI_CLI, CODEX_SANDBOX|CODEX_CI|CODEX_THREAD_ID,
//     ANTIGRAVITY_AGENT, AUGMENT_AGENT, OPENCODE=1, CLAUDECODE.
//
// Variables a human's interactive shell carries too (API keys, CODEX_HOME,
// CLAUDE_CONFIG_DIR, TERM_PROGRAM, CURSOR_TRACE_ID, OPENCODE_TERMINAL, CI,
// most CLAUDE_CODE_*) are deliberately absent. GROK_AGENT doubles as Grok's
// own "which agent profile" setting, so a human who exports it is refused
// too (VECNET_HUMAN=1 fixes that). Kimi Code sets no marker; the terminal
// check catches it.
var agentMarkers = []string{
	"CLAUDECODE",                     // Claude Code: Bash tool, hooks and MCP servers
	"CLAUDE_CODE_ENTRYPOINT",         // Claude Code: set on its own process, so every child inherits it
	"AI_AGENT",                       // cross-tool convention: Claude Code, pi, OpenCode, Copilot CLI
	"CODEX_THREAD_ID",                // OpenAI Codex CLI
	"CODEX_CI",                       // OpenAI Codex CLI
	"CODEX_SANDBOX",                  // OpenAI Codex CLI, macOS seatbelt
	"CODEX_SANDBOX_NETWORK_DISABLED", // OpenAI Codex CLI, sandboxed runs
	"PI_CODING_AGENT",                // pi
	"OPENCODE",                       // OpenCode
	"CURSOR_AGENT",                   // Cursor agent CLI
	"GEMINI_CLI",                     // Gemini CLI
	"ANTIGRAVITY_AGENT",              // Antigravity (agy)
	"GROK_AGENT",                     // Grok CLI
	"MUSE_THREAD_ID",                 // Muse Code
	"MUSE_TOOL_USE_ID",               // Muse Code
	"AUGMENT_AGENT",                  // Augment CLI (from @vercel/detect-agent)
	"GOOSE_TERMINAL",                 // Goose
	"AGENT",                          // generic: OpenCode (1), Goose (goose)
}

// runner is who started this invocation, as far as the CLI can tell.
type runner struct {
	agent  bool
	reason string // why it counts as an agent: a variable name or the terminal check
}

// truthy is the override spelling: 1, true or yes.
func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// detectRunner decides agent or human. VECNET_AGENT forces agent (it wins
// over VECNET_HUMAN); VECNET_HUMAN forces human; any agent marker means
// agent; otherwise a human must have a terminal on both stdin and stdout.
func detectRunner(getenv func(string) string, stdinTTY, stdoutTTY bool) runner {
	if truthy(getenv("VECNET_AGENT")) {
		return runner{agent: true, reason: "VECNET_AGENT"}
	}
	if truthy(getenv("VECNET_HUMAN")) {
		return runner{}
	}
	for _, k := range agentMarkers {
		if getenv(k) != "" {
			return runner{agent: true, reason: k}
		}
	}
	if !stdinTTY || !stdoutTTY {
		return runner{agent: true, reason: "stdin or stdout is not a terminal"}
	}
	return runner{}
}

// isTerminal reports whether v is an *os.File open on a terminal.
func isTerminal(v any) bool {
	f, ok := v.(*os.File)
	return ok && term.IsTerminal(f.Fd())
}

// operatorClientInfo describes this terminal for the X-Vecnet-Client
// header: the host name, whether this is an ssh session, and whether the
// run looked like an agent's. Only the fact of ssh is sent, never the
// client address. Everything is cleaned here and again by the hub.
func operatorClientInfo(getenv func(string) string, hostname func() (string, error), agent bool) envelope.OperatorClient {
	host, err := hostname()
	if err != nil {
		host = ""
	}
	c := envelope.OperatorClient{Kind: envelope.ClientKindOperatorCLI, Host: host, AgentSuspected: agent}
	for _, k := range []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY"} {
		if getenv(k) != "" {
			c.SSH = true
		}
	}
	out, _ := c.Clean()
	return out
}

// isOperatorSecret reports whether secret is an operator credential. The
// hub trims a bearer with strings.TrimSpace before authenticating it, so a
// profile secret padded with (Unicode) whitespace is the same credential.
func isOperatorSecret(secret string) bool {
	return strings.HasPrefix(strings.TrimSpace(secret), "vn_operator_")
}

// operatorLabel is the label of a run that names a profile explicitly: an
// operator credential always says which terminal it came from, and is
// marked agent_suspected when the run looks like an agent's; any other
// credential sends no label.
func (a *app) operatorLabel(p Profile) *envelope.OperatorClient {
	if !isOperatorSecret(p.Secret) {
		return nil
	}
	r := detectRunner(a.getenv, isTerminal(a.stdin), isTerminal(a.stdout))
	c := operatorClientInfo(a.getenv, os.Hostname, r.agent)
	return &c
}

// checkDefault refuses a config whose legacy `default` key names a profile
// holding an operator credential: that file was written so that a bare run
// speaks as the operator, which an agent forgetting --net would then do.
func (c Config) checkDefault() error {
	if c.Default == "" {
		return nil
	}
	if p, ok := c.Profiles[c.Default]; ok && isOperatorSecret(p.Secret) {
		return usageErr(fmt.Sprintf("config: default = %q names an operator credential, and `default` no longer selects a profile. "+
			"Replace that line with operator_profile = %q: bare runs from your terminal then speak as the operator, and agents must pass --net <profile>", c.Default, c.Default))
	}
	return nil
}

// namedProfile is the profile this run names: --net (hook: --profile),
// else $VECNET_PROFILE; "" means the run is bare.
func (a *app) namedProfile() string {
	if a.net != "" {
		return a.net
	}
	return strings.TrimSpace(a.getenv("VECNET_PROFILE"))
}

// bareAgentErr is the refusal for a bare run by an agent.
func bareAgentErr(r runner) error {
	return usageErr(fmt.Sprintf("refusing a bare run: this looks like an agent (%s). "+
		"Name your identity with --net <profile> (or set VECNET_PROFILE); "+
		"bare vecnet is the human operator's. A human can set VECNET_HUMAN=1.", r.reason))
}

// resolveBare picks the profile of a bare run, or refuses it. It returns
// the profile and the operator CLI label to send with it.
func (a *app) resolveBare(cfg Config) (string, Profile, *envelope.OperatorClient, error) {
	r := detectRunner(a.getenv, isTerminal(a.stdin), isTerminal(a.stdout))
	if r.agent {
		return "", Profile{}, nil, bareAgentErr(r)
	}
	if cfg.OperatorProfile == "" {
		return "", Profile{}, nil, usageErr("a bare run from a terminal speaks as the operator, but the config file has no operator_profile; " +
			"add operator_profile = \"<name>\" naming a profile that holds the operator credential, or pass --net <profile>")
	}
	name, p, err := cfg.profile(cfg.OperatorProfile)
	if err != nil {
		return "", Profile{}, nil, usageErr("operator_profile: " + err.Error())
	}
	if !isOperatorSecret(p.Secret) {
		return "", Profile{}, nil, usageErr(fmt.Sprintf("operator_profile %q does not hold an operator credential (vn_operator_…); bare runs never speak as a peer", name))
	}
	c := operatorClientInfo(a.getenv, os.Hostname, false)
	return name, p, &c, nil
}
