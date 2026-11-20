package cli

import (
	"bytes"
	"encoding/json"
	"strings"
)

// The Antigravity (agy) hook runtime. agy runs hooks from .agents/hooks.json
// and passes camelCase JSON on stdin (docs/research/evidence/agy-1.5.13.json,
// hooks.payload_fields; docs/research/2026-11-20-agy-autodelivery.md §5).
// Every silent run still answers {} on stdout: agy requires JSON whenever a
// hook exits 0. Injected context is one systemMessage step — never
// userMessage (it overwrites lastUserInput and is framed as a real user
// prompt), never ephemeralMessage or metadata/user_markdown.

// agyHookOutput encodes context for the agy host: exactly one systemMessage
// inject step, or {} when there is nothing to inject. The context is
// already HTML-escaped by inert; those bytes are kept rather than expanded
// a second time.
func agyHookOutput(context string) []byte {
	if context == "" {
		return []byte("{}\n")
	}
	type agyStep struct {
		SystemMessage struct {
			SystemMessage string `json:"systemMessage"`
		} `json:"systemMessage"`
	}
	type agyOutput struct {
		InjectSteps []agyStep `json:"injectSteps"`
	}
	var step agyStep
	step.SystemMessage.SystemMessage = context
	var raw bytes.Buffer
	encoder := json.NewEncoder(&raw)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(agyOutput{InjectSteps: []agyStep{step}})
	return raw.Bytes()
}

// hookAgyClient reports whether args name the agy hook client, so cmdHook
// can answer {} where the Claude and Codex paths print nothing. A parse
// error elsewhere in args still counts: agy rejects any non-JSON stdout,
// including from a run hookRun refuses.
func hookAgyClient(args []string) bool {
	o, _ := parseFlags("hook", args, hookFlagKinds)
	return o.str("client") == "agy"
}

// agyInput is the agy hook payload. SessionStart and Stop carry no
// invocation fields; conversationId identifies the conversation.
type agyInput struct {
	HookEventName   string `json:"hookEventName"`
	ConversationID  string `json:"conversationId"`
	InvocationNum   int    `json:"invocationNum"`
	InitialNumSteps int    `json:"initialNumSteps"`
	LastUserInput   string `json:"lastUserInput"`
	Cwd             string `json:"cwd"`
}

// agyEvent maps one agy hook run onto the Claude Code event semantics the
// delivery path already implements, and normalises its session identity.
// ok is false for a run that must stay silent and touch nothing: unusable
// stdin (which carried an identity this run cannot see), a payload with no
// conversationId and no ANTIGRAVITY_CONVERSATION_ID (no identity to admit),
// an event Vecnet does not handle (PostInvocation, Stop, unknown), or an
// --event value the agy installer never writes.
//
// A PreInvocation starts a turn (Claude user-prompt semantics: may bind)
// only with invocationNum 0 and a non-empty lastUserInput. Every other
// PreInvocation — mid-turn invocations, and a subagent's first invocation,
// which carries an empty lastUserInput (evidence I1) — takes the post-tool
// semantics and never binds. SessionStart does no hub work at all.
func agyEvent(o opts, raw []byte, e error, getenv func(string) string) (event, session string, ok bool) {
	var in agyInput
	if e != nil || len(raw) > 1<<20 || (len(bytes.TrimSpace(raw)) != 0 && json.Unmarshal(raw, &in) != nil) {
		return "", "", false
	}
	name := in.HookEventName
	if !o.has("event") {
		switch name {
		case "SessionStart", "PreInvocation":
		default:
			return "", "", false
		}
	} else {
		switch o.str("event") {
		case "session-start":
			name = "SessionStart"
		case "pre-invocation":
			if name != "" && name != "PreInvocation" {
				return "", "", false
			}
			name = "PreInvocation"
		default:
			return "", "", false
		}
	}
	conv := in.ConversationID
	if conv == "" {
		conv = getenv("ANTIGRAVITY_CONVERSATION_ID")
	}
	if conv == "" {
		// No conversation identity at all: this run can neither bind nor
		// claim, so it is not admitted and touches nothing.
		return "", "", false
	}
	if name == "SessionStart" {
		return "session-start", hookSession(conv), true
	}
	if in.InvocationNum == 0 && strings.TrimSpace(in.LastUserInput) != "" {
		return "user-prompt", hookSession(conv), true
	}
	return "post-tool", hookSession(conv), true
}
