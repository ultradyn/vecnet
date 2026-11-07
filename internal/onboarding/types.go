// Package onboarding is the hub-owned registry of Add agent kinds: prompt
// templates, field validation and submit-action ids.
//
// The package is stdlib-only. It does not open a database or speak HTTP.
// Contracts are Decisions §1 and §§3–6 of
// docs/plans/2026-11-06-agent-onboarding-kinds.md, and PROTOCOL §§2.12,
// 4.5–4.6, 5.2, 12 and 13.3.
//
// Keeping a particular peer on CLI bearer is an operating rule for the
// operator. This package does not special-case any peer id.
package onboarding

import (
	"encoding/json"
	"fmt"
)

// SpecVersion is the opaque revision of every kind contract this package
// currently publishes. A submit whose spec_version differs is stale.
const SpecVersion = "2026-11-06"

// Submit actions are declarative ids. Clients never send a function name.
const (
	ActionInvite       = "invite"
	ActionOAuthLink    = "oauth_link"
	ActionGrokbotSetup = "grokbot_setup"
)

// Field types the registry knows how to validate.
const (
	FieldString  = "string"
	FieldSecret  = "secret"
	FieldBoolean = "boolean"
	FieldInteger = "integer"
	FieldEnum    = "enum"
)

// Prompt phases RenderPrompt accepts.
const (
	PhaseInvite    = "invite"
	PhaseAuthorize = "authorize"
	PhaseSetup     = "setup"
)

// Credential modes for a Grok Bot. A bot has one of them, never both.
const (
	CredentialHostedOAuth = "hosted_oauth"
	CredentialCLIBearer   = "cli_bearer"
)

// Error codes Validate, Normalize and RenderPrompt return.
const (
	CodeUnknownKind           = "unknown_kind"
	CodeStaleSpec             = "stale_spec"
	CodeUnknownField          = "unknown_field"
	CodeInvalidValue          = "invalid_value"
	CodeUnsupportedAction     = "unsupported_action"
	CodeConnectorNameConflict = "connector_name_conflict"
)

// Auth states on a submit result. Route acceptance is not "verified".
const (
	AuthUnlinked = "unlinked"
	AuthPending  = "pending"
	AuthVerified = "verified"
)

// Setup delivery states a result may report (PROTOCOL §5.1, §5.2). "held"
// means the delivery is still queued and is not being pushed (hold, or
// wake_idle while the recipient is busy).
const (
	SetupQueued     = "queued"
	SetupHeld       = "held"
	SetupAccepted   = "accepted"
	SetupProcessed  = "processed"
	SetupFailed     = "failed"
	SetupExpired    = "expired"
	SetupDeadLetter = "dead_letter"
)

// KindSpec is one Add agent kind. JSON names are the wire contract.
type KindSpec struct {
	Kind           string            `json:"kind"`
	Label          string            `json:"label"`
	SpecVersion    string            `json:"spec_version"`
	PromptTemplate string            `json:"prompt_template"`
	Fields         []FieldSpec       `json:"fields"`
	SubmitAction   string            `json:"submit_action"`
	SendDefaults   map[string]string `json:"send_defaults"`
}

// FieldSpec describes one input. Requiredness, bounds and human_only are
// enforced by Validate; a client check is only assistance.
type FieldSpec struct {
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Required  bool     `json:"required"`
	HumanOnly bool     `json:"human_only"`
	Help      string   `json:"help,omitempty"`
	Default   any      `json:"default,omitempty"`
	MinLength *int     `json:"min_length,omitempty"`
	MaxLength *int     `json:"max_length,omitempty"`
	Minimum   *int64   `json:"minimum,omitempty"`
	Maximum   *int64   `json:"maximum,omitempty"`
	Pattern   string   `json:"pattern,omitempty"`
	Enum      []string `json:"enum,omitempty"`
}

// SubmitRequest is one onboarding submission. Inputs are validated against
// the selected kind; unknown keys are rejected.
type SubmitRequest struct {
	Kind        string         `json:"kind"`
	SpecVersion string         `json:"spec_version"`
	RequestID   string         `json:"request_id"`
	Inputs      map[string]any `json:"inputs"`
}

// SetupInfo identifies the one hub-authored setup message.
type SetupInfo struct {
	ThreadID   string `json:"thread_id"`
	MessageID  string `json:"message_id"`
	DeliveryID string `json:"delivery_id"`
	State      string `json:"state"`
}

// SubmitResult is the safe metadata of a submit. HumanOutput is the one-time
// invitation or link text from the human path. MarshalSafe omits it so a
// status, replay or MCP encoding cannot return it.
type SubmitResult struct {
	RequestID     string            `json:"request_id"`
	Kind          string            `json:"kind"`
	PeerID        string            `json:"peer_id"`
	PrincipalID   string            `json:"principal_id"`
	RouteID       string            `json:"route_id,omitempty"`
	ConnectorName string            `json:"connector_name,omitempty"`
	Setup         *SetupInfo        `json:"setup,omitempty"`
	AuthState     string            `json:"auth_state"`
	SendDefaults  map[string]string `json:"send_defaults,omitempty"`
	Status        string            `json:"status,omitempty"`
	WebPath       string            `json:"web_path,omitempty"`
	HumanOutput   string            `json:"human_output,omitempty"`
	// Name, SpecVersion, NameOpen, SkipMCP, EnrolTTLSeconds and
	// RequestPrincipalID are the receipt's nonsecret request options.
	// Status fills them so a retry can be compared without reading live
	// directory state. Zero values are omitted. RequestPrincipalID is the
	// principal the request named, which is empty when the request omitted
	// one; PrincipalID remains the peer's principal.
	Name               string `json:"name,omitempty"`
	SpecVersion        string `json:"spec_version,omitempty"`
	NameOpen           bool   `json:"name_open,omitempty"`
	SkipMCP            bool   `json:"skip_mcp,omitempty"`
	EnrolTTLSeconds    int    `json:"enrol_ttl_seconds,omitempty"`
	RequestPrincipalID string `json:"request_principal_id,omitempty"`
	// SafeComplete is true when the receipt stored every safe option, so a
	// zero value is the request's value. Historical receipts leave it false.
	// Absent fields on those receipts are not evidence the requests match.
	SafeComplete bool `json:"safe_complete,omitempty"`
}

// MarshalSafe encodes the result with HumanOutput removed.
func (r SubmitResult) MarshalSafe() ([]byte, error) {
	r.HumanOutput = ""
	return json.Marshal(r)
}

// Normalized is a validated submit with the routine key reduced to the raw
// crsr_ value. WebhookURL and WebhookKey must not be logged; String redacts
// both.
type Normalized struct {
	Kind            string
	SpecVersion     string
	RequestID       string
	Name            string
	NameOpen        bool
	EnrolTTLSeconds int
	PrincipalID     string
	WebhookURL      string
	WebhookKey      string
	ConnectorName   string
	SkipMCP         bool
}

// String redacts the routine URL and key so a log or a test dump cannot
// print them.
func (n Normalized) String() string {
	url, key := "absent", "absent"
	if n.WebhookURL != "" {
		url = "redacted"
	}
	if n.WebhookKey != "" {
		key = "redacted"
	}
	return fmt.Sprintf("Normalized kind=%s spec=%s request_id=%s name=%q name_open=%t enrol_ttl=%d principal=%s connector=%s skip_mcp=%t webhook_url=%s webhook_key=%s",
		n.Kind, n.SpecVersion, n.RequestID, n.Name, n.NameOpen, n.EnrolTTLSeconds, n.PrincipalID, n.ConnectorName, n.SkipMCP, url, key)
}

// GoString is String, so %#v redacts the routine URL and key too.
func (n Normalized) GoString() string { return n.String() }

// FieldError is a validation failure. Message is fixed text and never
// includes a supplied secret, URL or key.
type FieldError struct {
	Code    string
	Field   string
	Message string
}

func (e *FieldError) Error() string {
	if e.Field != "" {
		return e.Code + ": " + e.Field + ": " + e.Message
	}
	return e.Code + ": " + e.Message
}
