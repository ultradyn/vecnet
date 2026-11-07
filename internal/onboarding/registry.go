package onboarding

import (
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strings"
)

// maxKeyBytes is the CLI key-file cap (4 KiB). A larger paste is refused
// before the bytes are interpreted, and the error never quotes them.
const maxKeyBytes = 4 << 10

const keyErrorMessage = "a routine key is printable ASCII starting crsr_"

// webhookURLRE is stricter than the legacy route check, which allows any 36
// lowercase hex-or-hyphen characters. Grouped 8-4-4-4-12 is the onboarding
// rule. The only match is public https on api2.cursor.sh with no userinfo,
// port, query, fragment or extra path, so it cannot be a private address or
// a redirector URL. Redirect refusal at send time stays with the push client
// (PROTOCOL §13.3). This package does not change the legacy route validator.
var webhookURLRE = regexp.MustCompile(`^https://api2\.cursor\.sh/automations/webhook/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// agentNameRE is the open-enrol suggestion shape (PROTOCOL §2.12): 2–32
// ASCII letters, digits, spaces, dots, underscores and hyphens, starting
// and ending with a letter or digit. Operator-set names stay free-form.
var agentNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,30}[A-Za-z0-9]$`)

// connectorNameRE is the optional connector override. The default is
// vecnet- plus Slug, which already fits.
var connectorNameRE = regexp.MustCompile(`^vecnet-[a-z0-9-]{1,48}$`)

var requestIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

var registeredActions = map[string]bool{
	ActionInvite: true, ActionOAuthLink: true, ActionGrokbotSetup: true,
}

// Specs returns the six Add agent kinds in catalogue order. The slice and
// its maps are copies; callers cannot edit the registry.
func Specs() []KindSpec {
	out := make([]KindSpec, len(kindList))
	for i := range kindList {
		out[i] = cloneSpec(kindList[i])
	}
	return out
}

// Lookup returns a copy of the named kind.
func Lookup(kind string) (KindSpec, bool) {
	for i := range kindList {
		if kindList[i].Kind == kind {
			return cloneSpec(kindList[i]), true
		}
	}
	return KindSpec{}, false
}

// Validate checks req against the kind contract. It does not mutate req.
// A stale spec_version is rejected before inputs are read.
func Validate(req SubmitRequest) error {
	_, err := Normalize(req)
	return err
}

// Normalize validates req and returns cleaned inputs. WebhookKey is the raw
// crsr_ value with one leading Bearer prefix removed; callers must not log
// Normalized (its String method redacts the routine URL and key).
func Normalize(req SubmitRequest) (Normalized, error) {
	spec, ok := Lookup(req.Kind)
	if !ok {
		return Normalized{}, fieldErr(CodeUnknownKind, "kind", "unknown peer kind")
	}
	if req.SpecVersion != spec.SpecVersion {
		return Normalized{}, fieldErr(CodeStaleSpec, "", "spec_version does not match the kind contract")
	}
	if !requestIDRE.MatchString(req.RequestID) {
		return Normalized{}, fieldErr(CodeInvalidValue, "request_id", "request_id must be 1 to 128 characters: letters, digits, and . _ : -")
	}
	if !registeredActions[spec.SubmitAction] {
		return Normalized{}, fieldErr(CodeUnsupportedAction, "submit_action", "submit_action is not registered")
	}
	known := make(map[string]FieldSpec, len(spec.Fields))
	for _, f := range spec.Fields {
		known[f.Name] = f
	}
	var unknown []string
	for k := range req.Inputs {
		if _, ok := known[k]; !ok {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	if len(unknown) > 0 {
		return Normalized{}, fieldErr(CodeUnknownField, unknown[0], "unknown field")
	}

	var p parsed
	for _, f := range spec.Fields {
		v, present := req.Inputs[f.Name]
		if !present {
			if f.Required {
				return Normalized{}, fieldErr(CodeInvalidValue, f.Name, f.Name+" is required")
			}
			continue
		}
		if err := p.store(f, v); err != nil {
			return Normalized{}, err
		}
	}
	out := Normalized{Kind: spec.Kind, SpecVersion: spec.SpecVersion, RequestID: req.RequestID, Name: p.name, PrincipalID: p.principal}
	switch spec.SubmitAction {
	case ActionInvite:
		if p.nameOpen {
			if p.name != "" && !agentNameRE.MatchString(p.name) {
				return Normalized{}, fieldErr(CodeInvalidValue, "name", "a suggested name is 2 to 32 characters: letters, digits, space, dot, underscore and hyphen, starting and ending with a letter or digit")
			}
		} else if p.name == "" {
			return Normalized{}, fieldErr(CodeInvalidValue, "name", "peer name is required")
		}
		ttl, err := effectiveTTL(p.ttlSet, p.ttl)
		if err != nil {
			return Normalized{}, err
		}
		out.NameOpen = p.nameOpen
		out.EnrolTTLSeconds = ttl
	case ActionOAuthLink:
		if p.name == "" {
			return Normalized{}, fieldErr(CodeInvalidValue, "name", "peer name is required")
		}
	case ActionGrokbotSetup:
		if p.name == "" {
			return Normalized{}, fieldErr(CodeInvalidValue, "name", "peer name is required")
		}
		if !webhookURLRE.MatchString(p.webhookURL) {
			return Normalized{}, fieldErr(CodeInvalidValue, "webhook_url", "webhook_url must be https://api2.cursor.sh/automations/webhook/ and a lowercase UUID in groups 8-4-4-4-12, with no userinfo, port, query, fragment, or extra path")
		}
		key, err := normalizeKey(p.webhookKey)
		if err != nil {
			return Normalized{}, err
		}
		connector, err := ConnectorName(p.name, p.connector)
		if err != nil {
			return Normalized{}, err
		}
		out.WebhookURL = p.webhookURL
		out.WebhookKey = key
		out.ConnectorName = connector
		out.SkipMCP = p.skip
	default:
		return Normalized{}, fieldErr(CodeUnsupportedAction, "submit_action", "submit_action is not registered")
	}
	return out, nil
}

// Slug is the profile slug of a peer name: ASCII letters lowered, every
// other run of bytes one "-", trimmed, at most 40 bytes, or "agent" when
// nothing remains. internal/cli/invite.go and the web invite model share
// this result; testdata/invite.json pins it.
func Slug(name string) string {
	var b strings.Builder
	dash := false
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteByte(c)
			continue
		}
		dash = true
	}
	s := b.String()
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	if s == "" {
		return "agent"
	}
	return s
}

// ConnectorName returns the per-peer connector descriptor. An empty override
// is vecnet- plus Slug(peerName). Any other override must match
// connectorNameRE. Collision with another peer is ConnectorCollision, not
// this function.
func ConnectorName(peerName, override string) (string, error) {
	if override == "" {
		return "vecnet-" + Slug(peerName), nil
	}
	if !connectorNameRE.MatchString(override) {
		return "", fieldErr(CodeInvalidValue, "connector_name", "connector_name must match ^vecnet-[a-z0-9-]{1,48}$")
	}
	return override, nil
}

// ConnectorCollision reports CodeConnectorNameConflict when name is already
// held by another peer. taken is other peers' descriptor names; a retry of
// the same peer is the caller's concern.
func ConnectorCollision(name string, taken []string) error {
	for _, other := range taken {
		if other == name {
			return fieldErr(CodeConnectorNameConflict, "connector_name", "connector_name is already used by another peer on this hub")
		}
	}
	return nil
}

type parsed struct {
	name, principal, webhookURL, webhookKey, connector string
	nameOpen, skip, ttlSet                             bool
	ttl                                                int64
}

func (p *parsed) store(f FieldSpec, v any) error {
	switch f.Type {
	case FieldString, FieldSecret:
		s, ok := v.(string)
		if !ok {
			return fieldErr(CodeInvalidValue, f.Name, f.Name+" must be a string")
		}
		switch f.Name {
		case "name":
			p.name = s
		case "principal_id":
			p.principal = s
		case "webhook_url":
			p.webhookURL = s
		case "webhook_key":
			p.webhookKey = s
		case "connector_name":
			p.connector = s
		default:
			return fieldErr(CodeInvalidValue, f.Name, "unsupported field")
		}
	case FieldBoolean:
		b, ok := v.(bool)
		if !ok {
			return fieldErr(CodeInvalidValue, f.Name, f.Name+" must be a boolean")
		}
		switch f.Name {
		case "name_open":
			p.nameOpen = b
		case "skip_mcp":
			p.skip = b
		default:
			return fieldErr(CodeInvalidValue, f.Name, "unsupported field")
		}
	case FieldInteger:
		n, ok := asInt(v)
		if !ok {
			return fieldErr(CodeInvalidValue, f.Name, f.Name+" must be a whole number")
		}
		if f.Minimum != nil && n < *f.Minimum && !(n == 0 && !f.Required) {
			return fieldErr(CodeInvalidValue, f.Name, boundsMessage(f))
		}
		if f.Maximum != nil && n > *f.Maximum {
			return fieldErr(CodeInvalidValue, f.Name, boundsMessage(f))
		}
		if f.Name == "enrol_ttl_seconds" {
			p.ttl = n
			p.ttlSet = true
		}
	case FieldEnum:
		s, ok := v.(string)
		if !ok || !containsStr(f.Enum, s) {
			return fieldErr(CodeInvalidValue, f.Name, f.Name+" is not an allowed value")
		}
	default:
		return fieldErr(CodeInvalidValue, f.Name, "unsupported field type")
	}
	return nil
}

func boundsMessage(f FieldSpec) string {
	if f.Name == "enrol_ttl_seconds" {
		return "enrol_ttl_seconds must be between 60 and 604800"
	}
	return f.Name + " is out of range"
}

func effectiveTTL(set bool, n int64) (int, error) {
	if !set || n == 0 {
		return 86400, nil
	}
	if n < 60 || n > 604800 {
		return 0, fieldErr(CodeInvalidValue, "enrol_ttl_seconds", "enrol_ttl_seconds must be between 60 and 604800")
	}
	return int(n), nil
}

func asInt(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) {
			return 0, false
		}
		if n < math.MinInt64 || n > math.MaxInt64 {
			return 0, false
		}
		return int64(n), true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	default:
		return 0, false
	}
}

func normalizeKey(raw string) (string, error) {
	if len(raw) > maxKeyBytes {
		return "", keyErr()
	}
	s := strings.TrimSpace(raw)
	const prefix = "bearer "
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		s = s[len(prefix):]
		if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
			return "", keyErr()
		}
	}
	if !validRawKey(s) {
		return "", keyErr()
	}
	return s, nil
}

func validRawKey(key string) bool {
	rest, ok := strings.CutPrefix(key, "crsr_")
	if !ok || rest == "" {
		return false
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] <= ' ' || rest[i] > '~' {
			return false
		}
	}
	return true
}

func keyErr() error {
	return fieldErr(CodeInvalidValue, "webhook_key", keyErrorMessage)
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func fieldErr(code, field, msg string) error {
	return &FieldError{Code: code, Field: field, Message: msg}
}

func cloneSpec(s KindSpec) KindSpec {
	fields := make([]FieldSpec, len(s.Fields))
	for i, f := range s.Fields {
		fields[i] = cloneField(f)
	}
	s.Fields = fields
	if s.SendDefaults != nil {
		m := make(map[string]string, len(s.SendDefaults))
		for k, v := range s.SendDefaults {
			m[k] = v
		}
		s.SendDefaults = m
	}
	return s
}

func cloneField(f FieldSpec) FieldSpec {
	if f.Minimum != nil {
		v := *f.Minimum
		f.Minimum = &v
	}
	if f.Maximum != nil {
		v := *f.Maximum
		f.Maximum = &v
	}
	if f.MinLength != nil {
		v := *f.MinLength
		f.MinLength = &v
	}
	if f.MaxLength != nil {
		v := *f.MaxLength
		f.MaxLength = &v
	}
	if f.Enum != nil {
		f.Enum = append([]string(nil), f.Enum...)
	}
	switch d := f.Default.(type) {
	case *bool:
		v := *d
		f.Default = &v
	case *int:
		v := *d
		f.Default = &v
	}
	return f
}

func boolPtr(v bool) *bool { return &v }

func i64Ptr(v int64) *int64 { return &v }

var kindList = buildKinds()

func buildKinds() []KindSpec {
	return []KindSpec{
		inviteKind("session", "Agent session (Claude Code, Codex, CLI)"),
		inviteKind("launcher", "Launcher"),
		inviteKind("openclaw", "OpenClaw"),
		inviteKind("pagchat-bot", "pagchat bot"),
		{
			Kind: "grokbot", Label: "Grok Bot", SpecVersion: SpecVersion,
			PromptTemplate: grokbotAuthorizeSrc, SubmitAction: ActionGrokbotSetup,
			SendDefaults: map[string]string{"priority": "wake_idle"},
			Fields: []FieldSpec{
				{Name: "name", Type: FieldString, Required: true, Help: "Peer name. Operator-set and free-form, required. The same string is the bot name. There is no separate display name, and the owner is not renamed."},
				{Name: "webhook_url", Type: FieldString, Required: true, HumanOnly: true, Pattern: webhookURLRE.String(), Help: "Routine webhook URL. A person submits it on the protected form. It is never placed in a prompt, a setup message, or a receipt."},
				{Name: "webhook_key", Type: FieldSecret, Required: true, HumanOnly: true, Help: "Routine key: a raw crsr_ value, or the same value with one leading Bearer prefix. Human-only. Errors never quote it."},
				{Name: "connector_name", Type: FieldString, Pattern: connectorNameRE.String(), Help: "Optional connector descriptor. Default is vecnet- plus the bot-name slug. An override must match ^vecnet-[a-z0-9-]{1,48}$."},
				{Name: "skip_mcp", Type: FieldBoolean, Default: boolPtr(false), Help: "false is hosted OAuth (the default). true is CLI bearer. Never both."},
			},
		},
		{
			Kind: "dot", Label: "ChatGPT dot", SpecVersion: SpecVersion,
			PromptTemplate: dotPromptTemplate, SubmitAction: ActionOAuthLink,
			SendDefaults: map[string]string{},
			Fields: []FieldSpec{
				{Name: "name", Type: FieldString, Required: true, Help: "Peer name. Operator-set, free-form and required. A dot has no open name and no enrolment token (PROTOCOL §2.12, §12)."},
				{Name: "principal_id", Type: FieldString, Help: "Existing principal id. Empty creates an implicit principal named like the peer. Does not rename the owner."},
			},
		},
	}
}

func inviteKind(kind, label string) KindSpec {
	return KindSpec{
		Kind: kind, Label: label, SpecVersion: SpecVersion,
		PromptTemplate: invitePromptTemplate, SubmitAction: ActionInvite,
		SendDefaults: map[string]string{},
		Fields: []FieldSpec{
			{Name: "name", Type: FieldString, Help: "Peer name. Required unless name_open. Operator-set names are free-form. An open invite's suggestion, when set, is 2–32 characters: letters, digits, space, dot, underscore and hyphen, starting and ending with a letter or digit (PROTOCOL §2.12). This is not a second display name, and it does not rename the owner."},
			{Name: "name_open", Type: FieldBoolean, Default: boolPtr(false), Help: "When true, the agent chooses its name at enrol. Default false. Not valid for a dot."},
			{Name: "enrol_ttl_seconds", Type: FieldInteger, Default: 86400, Minimum: i64Ptr(60), Maximum: i64Ptr(604800), Help: "Enrolment token lifetime in seconds. Default 86400 (24h). 0 means that default. Otherwise 60 to 604800 (PROTOCOL §2.12)."},
			{Name: "principal_id", Type: FieldString, Help: "Existing principal id. Empty creates an implicit principal named like the peer. Does not rename the owner."},
		},
	}
}
