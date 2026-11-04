package cli

import (
	"errors"
	"strings"
	"testing"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDetectRunner(t *testing.T) {
	cases := []struct {
		name      string
		env       map[string]string
		tty       bool // stdin and stdout both terminals
		wantAgent bool
		reason    string
	}{
		{"human at a terminal", nil, true, false, ""},
		{"no terminal", nil, false, true, "stdin or stdout is not a terminal"},
		{"claude code bash tool", map[string]string{"CLAUDECODE": "1", "AI_AGENT": "claude-code_2-1-316_agent"}, false, true, "CLAUDECODE"},
		{"claude code hook", map[string]string{"CLAUDECODE": "1"}, false, true, "CLAUDECODE"},
		{"under a claude process", map[string]string{"CLAUDE_CODE_ENTRYPOINT": "cli"}, true, true, "CLAUDE_CODE_ENTRYPOINT"},
		{"ai_agent convention", map[string]string{"AI_AGENT": "pi"}, true, true, "AI_AGENT"},
		{"codex", map[string]string{"CODEX_THREAD_ID": "0199", "CODEX_CI": "1"}, true, true, "CODEX_THREAD_ID"},
		{"codex ci only", map[string]string{"CODEX_CI": "1"}, true, true, "CODEX_CI"},
		{"codex macos sandbox", map[string]string{"CODEX_SANDBOX": "seatbelt"}, true, true, "CODEX_SANDBOX"},
		{"codex sandbox network", map[string]string{"CODEX_SANDBOX_NETWORK_DISABLED": "1"}, true, true, "CODEX_SANDBOX_NETWORK_DISABLED"},
		{"pi", map[string]string{"PI_CODING_AGENT": "true"}, true, true, "PI_CODING_AGENT"},
		{"opencode", map[string]string{"OPENCODE": "1"}, true, true, "OPENCODE"},
		{"cursor agent", map[string]string{"CURSOR_AGENT": "1"}, true, true, "CURSOR_AGENT"},
		{"gemini", map[string]string{"GEMINI_CLI": "1"}, true, true, "GEMINI_CLI"},
		{"antigravity", map[string]string{"ANTIGRAVITY_AGENT": "1"}, true, true, "ANTIGRAVITY_AGENT"},
		{"grok", map[string]string{"GROK_AGENT": "1"}, true, true, "GROK_AGENT"},
		{"muse", map[string]string{"MUSE_THREAD_ID": "t1"}, true, true, "MUSE_THREAD_ID"},
		{"muse tool use", map[string]string{"MUSE_TOOL_USE_ID": "u1"}, true, true, "MUSE_TOOL_USE_ID"},
		{"augment", map[string]string{"AUGMENT_AGENT": "1"}, true, true, "AUGMENT_AGENT"},
		{"goose", map[string]string{"GOOSE_TERMINAL": "1"}, true, true, "GOOSE_TERMINAL"},
		{"generic AGENT", map[string]string{"AGENT": "goose"}, true, true, "AGENT"},
		// Variables a human shell carries too are not markers.
		{"human shell noise", map[string]string{
			"TERM_PROGRAM": "vscode", "CURSOR_TRACE_ID": "x", "CODEX_HOME": "/h/.codex", "CLAUDE_CONFIG_DIR": "/h/.claude",
			"CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS": "1", "OPENCODE_TERMINAL": "1", "CI": "true", "SSH_TTY": "/dev/pts/3",
		}, true, false, ""},
		{"empty marker ignored", map[string]string{"CLAUDECODE": ""}, true, false, ""},
		{"VECNET_HUMAN overrides markers and pipes", map[string]string{"VECNET_HUMAN": "1", "CLAUDECODE": "1"}, false, false, ""},
		{"VECNET_HUMAN=0 is not an override", map[string]string{"VECNET_HUMAN": "0"}, false, true, "stdin or stdout is not a terminal"},
		{"VECNET_AGENT forces agent", map[string]string{"VECNET_AGENT": "1"}, true, true, "VECNET_AGENT"},
		{"VECNET_AGENT beats VECNET_HUMAN", map[string]string{"VECNET_AGENT": "true", "VECNET_HUMAN": "1"}, true, true, "VECNET_AGENT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detectRunner(envOf(tc.env), tc.tty, tc.tty)
			if got.agent != tc.wantAgent || got.reason != tc.reason {
				t.Fatalf("detectRunner = %+v, want agent=%v reason=%q", got, tc.wantAgent, tc.reason)
			}
		})
	}
	// Either stream alone being a pipe is enough.
	if r := detectRunner(envOf(nil), true, false); !r.agent {
		t.Fatal("stdout pipe should count as an agent")
	}
	if r := detectRunner(envOf(nil), false, true); !r.agent {
		t.Fatal("stdin pipe should count as an agent")
	}
}

func TestOperatorClientInfo(t *testing.T) {
	host := func(s string, err error) func() (string, error) { return func() (string, error) { return s, err } }
	cases := []struct {
		name     string
		env      map[string]string
		hostname func() (string, error)
		agent    bool
		want     string
	}{
		{"local", nil, host("xsm", nil), false, "Operator on xsm CLI"},
		{"ssh connection", map[string]string{"SSH_CONNECTION": "10.0.0.5 51234 10.0.0.9 22"}, host("x-tall", nil), false, "Operator on x-tall CLI via ssh"},
		{"ssh client", map[string]string{"SSH_CLIENT": "2001:db8::7 51234 22"}, host("x-tall", nil), false, "Operator on x-tall CLI via ssh"},
		{"ssh tty only", map[string]string{"SSH_TTY": "/dev/pts/1"}, host("box", nil), false, "Operator on box CLI via ssh"},
		{"hostname error", nil, host("", errors.New("no")), false, "Operator on unknown CLI"},
		{"hostile hostname", nil, host("a\x1b]0;b\n c", nil), false, "Operator on a0bc CLI"},
		{"agent suspected", nil, host("xsm", nil), true, "Operator on xsm CLI (agent suspected)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := operatorClientInfo(envOf(tc.env), tc.hostname, tc.agent)
			if got.Label != tc.want || got.Kind != "operator-cli" || got.AgentSuspected != tc.agent {
				t.Fatalf("operatorClientInfo = %+v, want label %q", got, tc.want)
			}
			// The header carries no address and no label: the hub rebuilds it.
			if h := got.Header(); strings.Contains(h, "10.0.0") || strings.Contains(h, "label") {
				t.Fatalf("header %s leaks an address or label", h)
			}
		})
	}
}
