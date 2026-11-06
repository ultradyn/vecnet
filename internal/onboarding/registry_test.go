package onboarding_test

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/onboarding"
)

func TestOnboardingRegistryKinds(t *testing.T) {
	wantKinds := []string{"session", "launcher", "openclaw", "pagchat-bot", "grokbot", "dot"}
	wantLabels := []string{
		"Agent session (Claude Code, Codex, CLI)",
		"Launcher",
		"OpenClaw",
		"pagchat bot",
		"Grok Bot",
		"ChatGPT dot",
	}
	wantActions := []string{
		onboarding.ActionInvite,
		onboarding.ActionInvite,
		onboarding.ActionInvite,
		onboarding.ActionInvite,
		onboarding.ActionGrokbotSetup,
		onboarding.ActionOAuthLink,
	}
	specs := onboarding.Specs()
	if len(specs) != len(wantKinds) {
		t.Fatalf("Specs() returned %d kinds, want %d", len(specs), len(wantKinds))
	}
	for i, spec := range specs {
		if spec.Kind != wantKinds[i] {
			t.Fatalf("kind[%d] = %q, want %q", i, spec.Kind, wantKinds[i])
		}
		if spec.Label != wantLabels[i] {
			t.Fatalf("%s label = %q, want %q", spec.Kind, spec.Label, wantLabels[i])
		}
		if spec.SpecVersion != onboarding.SpecVersion || spec.SpecVersion == "" {
			t.Fatalf("%s spec_version = %q, want %s", spec.Kind, spec.SpecVersion, onboarding.SpecVersion)
		}
		if spec.SubmitAction != wantActions[i] {
			t.Fatalf("%s submit_action = %q, want %q", spec.Kind, spec.SubmitAction, wantActions[i])
		}
		if spec.PromptTemplate == "" {
			t.Fatalf("%s prompt_template is empty", spec.Kind)
		}
		if spec.SendDefaults == nil {
			t.Fatalf("%s send_defaults is null; want an object", spec.Kind)
		}
		for _, f := range spec.Fields {
			switch f.Name {
			case "display_name", "owner", "owner_name":
				t.Fatalf("%s has %s; peer name is the only name and the owner is not renamed", spec.Kind, f.Name)
			}
			switch f.Type {
			case onboarding.FieldString, onboarding.FieldSecret, onboarding.FieldBoolean, onboarding.FieldInteger, onboarding.FieldEnum:
			default:
				t.Fatalf("%s field %s has type %q", spec.Kind, f.Name, f.Type)
			}
		}
		again, ok := onboarding.Lookup(spec.Kind)
		if !ok {
			t.Fatalf("Lookup(%s) missed", spec.Kind)
		}
		if again.Label != spec.Label || again.SubmitAction != spec.SubmitAction {
			t.Fatalf("Lookup(%s) disagrees with Specs()", spec.Kind)
		}
	}
	for _, absent := range []string{"operator", "source", "", "Grokbot", "session "} {
		if _, ok := onboarding.Lookup(absent); ok {
			t.Fatalf("Lookup(%q) hit; infrastructure and unknown kinds are not Add agent choices", absent)
		}
	}

	specs[0].Label = "mutated"
	specs[0].Fields[0].Help = "mutated"
	specs[0].SendDefaults["priority"] = "wake"
	if got := onboarding.Specs()[0].Label; got == "mutated" {
		t.Fatal("Specs() returned a live label")
	}
	if onboarding.Specs()[0].Fields[0].Help == "mutated" {
		t.Fatal("Specs() returned a live field slice")
	}
	g, ok := onboarding.Lookup("grokbot")
	if !ok {
		t.Fatal("Lookup(grokbot)")
	}
	g.SendDefaults["priority"] = "wake"
	g2, _ := onboarding.Lookup("grokbot")
	if g2.SendDefaults["priority"] != "wake_idle" || len(g2.SendDefaults) != 1 {
		t.Fatalf("grokbot send_defaults = %#v, want priority wake_idle only", g2.SendDefaults)
	}
	session, _ := onboarding.Lookup("session")
	if len(session.SendDefaults) != 0 {
		t.Fatalf("session send_defaults = %#v, want none (priority stays required on the wire)", session.SendDefaults)
	}

	for _, kind := range []string{"session", "launcher", "openclaw", "pagchat-bot"} {
		spec, _ := onboarding.Lookup(kind)
		if got := fieldNames(spec); !reflect.DeepEqual(got, []string{"name", "name_open", "enrol_ttl_seconds", "principal_id"}) {
			t.Fatalf("%s fields = %v", kind, got)
		}
		name := mustField(t, spec, "name")
		if name.Type != onboarding.FieldString || name.Required {
			t.Fatalf("%s name = %+v, want an optional string (required unless name_open)", kind, name)
		}
		open := mustField(t, spec, "name_open")
		if open.Type != onboarding.FieldBoolean || open.Required || open.Default == nil {
			t.Fatalf("%s name_open = %+v", kind, open)
		}
		ttl := mustField(t, spec, "enrol_ttl_seconds")
		if ttl.Type != onboarding.FieldInteger || ttl.Required || ttl.Minimum == nil || ttl.Maximum == nil || *ttl.Minimum != 60 || *ttl.Maximum != 604800 {
			t.Fatalf("%s enrol_ttl_seconds = %+v, want 60..604800 defaulting", kind, ttl)
		}
		if !jsonNumber(t, ttl, 86400) {
			t.Fatalf("%s enrol ttl default is not 86400", kind)
		}
	}

	dot, _ := onboarding.Lookup("dot")
	if got := fieldNames(dot); !reflect.DeepEqual(got, []string{"name", "principal_id"}) {
		t.Fatalf("dot fields = %v, want name and principal_id (no TTL, no open name)", got)
	}
	if mustField(t, dot, "name").Required != true {
		t.Fatal("dot name is required")
	}
	for _, sentence := range []string{
		"In ChatGPT, open Plugins, choose +, then Add custom MCP server.",
		"Paste the MCP server URL, choose OAuth, and create it as a plugin.",
		"When the vecnet consent page asks for a link code, enter this one:",
		"Open the vecnet plugin page and click into its card; Rescan if vecnet.message is missing. In the dot, create a task with trigger vecnet, event vecnet.message and no settings. Under Advanced, set Runs in: Same chat for every run.",
		"Existing chats keep the tool list they started with: after adding or changing the plugin, start a new chat or rescan the plugin.",
	} {
		if !strings.Contains(dot.PromptTemplate, sentence) {
			t.Fatalf("dot prompt missing %q", sentence)
		}
	}
	if strings.Contains(dot.PromptTemplate, "printf %s") {
		t.Fatal("dot prompt is an enrolment paste")
	}

	grok, _ := onboarding.Lookup("grokbot")
	if got := fieldNames(grok); !reflect.DeepEqual(got, []string{"name", "webhook_url", "webhook_key", "connector_name", "skip_mcp"}) {
		t.Fatalf("grokbot fields = %v", got)
	}
	if !mustField(t, grok, "name").Required {
		t.Fatal("grokbot name is required")
	}
	urlField := mustField(t, grok, "webhook_url")
	keyField := mustField(t, grok, "webhook_key")
	if !urlField.HumanOnly || !keyField.HumanOnly {
		t.Fatal("webhook_url and webhook_key are human-only")
	}
	if keyField.Type != onboarding.FieldSecret || urlField.Type != onboarding.FieldString {
		t.Fatalf("webhook types url=%s key=%s", urlField.Type, keyField.Type)
	}
	conn := mustField(t, grok, "connector_name")
	if conn.Required || conn.Pattern != `^vecnet-[a-z0-9-]{1,48}$` {
		t.Fatalf("connector_name = %+v", conn)
	}
	skip := mustField(t, grok, "skip_mcp")
	if skip.Type != onboarding.FieldBoolean || skip.Required || skip.HumanOnly {
		t.Fatalf("skip_mcp = %+v", skip)
	}
	raw, err := json.Marshal(skip)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"default":false`) {
		t.Fatalf("skip_mcp JSON = %s, want default false", raw)
	}
	if !strings.Contains(skip.Help, "hosted OAuth") || !strings.Contains(strings.ToLower(skip.Help), "cli bearer") {
		t.Fatalf("skip_mcp help = %q, want both modes", skip.Help)
	}

	enc, err := json.Marshal(grok)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(enc, &obj); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"kind", "label", "spec_version", "prompt_template", "fields", "submit_action", "send_defaults"} {
		if _, ok := obj[key]; !ok {
			t.Fatalf("KindSpec JSON missing %s", key)
		}
	}
	if obj["send_defaults"].(map[string]any)["priority"] != "wake_idle" {
		t.Fatalf("send_defaults JSON = %#v", obj["send_defaults"])
	}

	res := onboarding.SubmitResult{
		RequestID: "req-1", Kind: "grokbot", PeerID: "peer_expected", PrincipalID: "prn_1",
		RouteID: "route_1", ConnectorName: "vecnet-helper",
		Setup:        &onboarding.SetupInfo{ThreadID: "thr_1", MessageID: "msg_1", DeliveryID: "dly_1", State: onboarding.SetupQueued},
		AuthState:    onboarding.AuthUnlinked,
		SendDefaults: map[string]string{"priority": "wake_idle"},
		Status:       "created", WebPath: "/#add-agent", HumanOutput: "secret-invite",
	}
	full, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"request_id", "kind", "peer_id", "principal_id", "route_id", "connector_name", "setup", "auth_state", "send_defaults", "status", "web_path", "human_output"} {
		if !strings.Contains(string(full), `"`+key+`"`) {
			t.Fatalf("SubmitResult JSON missing %s: %s", key, full)
		}
	}
	safe, err := res.MarshalSafe()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(safe), "secret-invite") || strings.Contains(string(safe), "human_output") {
		t.Fatal("MarshalSafe kept human_output")
	}

	secret := fakeKey(t)
	inputs := map[string]any{"name": "Bot", "webhook_key": secret, "display_name": secret}
	before := cloneInputs(inputs)
	req := onboarding.SubmitRequest{Kind: "grokbot", SpecVersion: "1999-01-01", RequestID: "req-1", Inputs: inputs}
	err = onboarding.Validate(req)
	checkFE(t, err, secret, onboarding.CodeStaleSpec, "")
	if !reflect.DeepEqual(inputs, before) {
		t.Fatal("Validate mutated inputs")
	}

	req.SpecVersion = onboarding.SpecVersion
	err = onboarding.Validate(req)
	checkFE(t, err, secret, onboarding.CodeUnknownField, "display_name")
	if !reflect.DeepEqual(inputs, before) {
		t.Fatal("Validate mutated inputs")
	}
	err = onboarding.Validate(onboarding.SubmitRequest{Kind: "operator", SpecVersion: onboarding.SpecVersion, RequestID: "req-1"})
	checkFE(t, err, "", onboarding.CodeUnknownKind, "kind")

	// Operator-set names stay free-form: one rune and a long name are both
	// legal, and there is no second display-name field. Open suggestions
	// stay on the 2–32 agent-name rule (PROTOCOL §2.12).
	longName := strings.Repeat("界", 200)
	if err := onboarding.Validate(inviteReq("session", map[string]any{"name": "a"})); err != nil {
		t.Fatalf("operator-set name: %v", err)
	}
	if err := onboarding.Validate(inviteReq("session", map[string]any{"name": longName})); err != nil {
		t.Fatalf("long operator-set name: %v", err)
	}
	if err := onboarding.Validate(inviteReq("session", map[string]any{"name_open": true})); err != nil {
		t.Fatalf("open invite without a suggestion: %v", err)
	}
	if err := onboarding.Validate(inviteReq("session", map[string]any{"name_open": true, "name": "ab"})); err != nil {
		t.Fatalf("open suggestion: %v", err)
	}
	err = onboarding.Validate(inviteReq("session", map[string]any{"name_open": true, "name": "a"}))
	checkFE(t, err, "", onboarding.CodeInvalidValue, "name")
	err = onboarding.Validate(inviteReq("session", map[string]any{}))
	checkFE(t, err, "", onboarding.CodeInvalidValue, "name")
	err = onboarding.Validate(inviteReq("dot", map[string]any{"name": "a", "name_open": true}))
	checkFE(t, err, "", onboarding.CodeUnknownField, "name_open")
	err = onboarding.Validate(inviteReq("dot", map[string]any{}))
	checkFE(t, err, "", onboarding.CodeInvalidValue, "name")
	if err := onboarding.Validate(inviteReq("dot", map[string]any{"name": "a"})); err != nil {
		t.Fatalf("dot name: %v", err)
	}

	for _, good := range []any{60, 86400, 604800, 0, float64(86400)} {
		if err := onboarding.Validate(inviteReq("launcher", map[string]any{"name": "bot", "enrol_ttl_seconds": good})); err != nil {
			t.Fatalf("ttl %v: %v", good, err)
		}
	}
	if err := onboarding.Validate(inviteReq("launcher", map[string]any{"name": "bot"})); err != nil {
		t.Fatalf("default ttl: %v", err)
	}
	for _, bad := range []any{59, 604801, 86400.5} {
		err := onboarding.Validate(inviteReq("launcher", map[string]any{"name": "bot", "enrol_ttl_seconds": bad}))
		checkFE(t, err, "", onboarding.CodeInvalidValue, "enrol_ttl_seconds")
	}
	n, err := onboarding.Normalize(inviteReq("openclaw", map[string]any{"name": "bot"}))
	if err != nil {
		t.Fatal(err)
	}
	if n.EnrolTTLSeconds != 86400 || n.NameOpen || n.Name != "bot" {
		t.Fatalf("normalized invite = %s", n)
	}
	n, err = onboarding.Normalize(inviteReq("openclaw", map[string]any{"name_open": true, "enrol_ttl_seconds": 0}))
	if err != nil || n.EnrolTTLSeconds != 86400 || !n.NameOpen {
		t.Fatalf("open ttl normalize: %v %s", err, n)
	}
}

func TestGrokbotFieldsAndRedaction(t *testing.T) {
	const validURL = "https://api2.cursor.sh/automations/webhook/01234567-89ab-cdef-0123-456789abcdef"
	secret := fakeKey(t)

	forms := []string{
		secret,
		"  " + secret + "\n",
		"\r\n" + secret + " \t",
		"Bearer " + secret,
		"bearer " + secret,
		"BEARER " + secret,
		"  Bearer " + secret + "\n",
	}
	var got string
	for i, form := range forms {
		n, err := onboarding.Normalize(grokReq(map[string]any{
			"name": "Helper Bot", "webhook_url": validURL, "webhook_key": form,
		}))
		if err != nil {
			t.Fatal("expected the routine key form to validate")
		}
		if strings.Contains(n.String(), secret) || strings.Contains(n.GoString(), secret) {
			t.Fatal("normalized dump contains secret material")
		}
		if n.WebhookURL != validURL {
			t.Fatal("webhook_url changed")
		}
		if n.ConnectorName != "vecnet-helper-bot" || n.SkipMCP {
			t.Fatalf("defaults = %s", n)
		}
		if i == 0 {
			got = n.WebhookKey
		} else if !sameBytes(got, n.WebhookKey) {
			t.Fatal("normalized keys differ")
		}
	}
	if !sameBytes(got, secret) {
		t.Fatal("normalized key is not the raw key")
	}

	// A 36-character hex blob matches the legacy route pattern and must
	// still fail the grouped-UUID check. So must a hyphenated string whose
	// groups are not 8-4-4-4-12.
	badURLs := []string{
		"https://api2.cursor.sh/automations/webhook/0123456789abcdef0123456789abcdef1234",
		"https://api2.cursor.sh/automations/webhook/012345678-9abc-def0-1234-56789abcdef",
		"https://api2.cursor.sh/automations/webhook/01234567-89AB-cdef-0123-456789abcdef",
		validURL + "?q=" + secret,
		validURL + "#frag",
		"https://user:pass@api2.cursor.sh/automations/webhook/01234567-89ab-cdef-0123-456789abcdef",
		"https://api2.cursor.sh:443/automations/webhook/01234567-89ab-cdef-0123-456789abcdef",
		"http://api2.cursor.sh/automations/webhook/01234567-89ab-cdef-0123-456789abcdef",
		"https://example.com/automations/webhook/01234567-89ab-cdef-0123-456789abcdef",
		validURL + "/",
		validURL + "/extra",
		" " + validURL,
		"",
	}
	for _, u := range badURLs {
		err := onboarding.Validate(grokReq(map[string]any{
			"name": "Helper Bot", "webhook_url": u, "webhook_key": secret,
		}))
		checkFE(t, err, secret, onboarding.CodeInvalidValue, "webhook_url")
		if strings.Contains(err.Error(), "01234567-89ab-cdef-0123-456789abcdef") && strings.Contains(u, "?") {
			t.Fatal("webhook_url error echoes the rejected URL")
		}
	}
	if err := onboarding.Validate(grokReq(map[string]any{
		"name": "Helper Bot", "webhook_url": validURL, "webhook_key": secret,
	})); err != nil {
		t.Fatal("strict grouped URL should validate")
	}

	rejects := []string{
		"Bearer Bearer " + secret,
		"Bearer bearer " + secret,
		"crsr_abc def",
		"crsr_abc\ndef",
		"crsr_abc\x01def",
		"crsr_abc\x7f",
		"Bearer " + secret + " tail",
		strings.Repeat("a", 4097),
		strings.Repeat(" ", 2000) + secret + strings.Repeat(" ", 2097),
		"Bearer " + strings.Repeat("a", 24),
		"",
		"crsr_",
	}
	// The space-padded secret is over 4096 only when the pads sum that way.
	// Pin the bound: 4096 bytes of a valid key is accepted, 4097 is not,
	// and neither error nor a too-long paste may echo the key.
	wide := secret + strings.Repeat("a", 4096-len(secret))
	if len(wide) != 4096 {
		t.Fatalf("fixture width %d", len(wide))
	}
	if err := onboarding.Validate(grokReq(map[string]any{
		"name": "Helper Bot", "webhook_url": validURL, "webhook_key": wide,
	})); err != nil {
		t.Fatal("4096-byte raw key should validate")
	}
	rejects = append(rejects, wide+"a")

	for _, bad := range rejects {
		err := onboarding.Validate(grokReq(map[string]any{
			"name": "Helper Bot", "webhook_url": validURL, "webhook_key": bad,
		}))
		checkFE(t, err, secret, onboarding.CodeInvalidValue, "webhook_key")
		var fe *onboarding.FieldError
		if !errors.As(err, &fe) || fe.Message != "a routine key is printable ASCII starting crsr_" {
			t.Fatal("webhook_key message is not the fixed text")
		}
	}

	n, err := onboarding.Normalize(grokReq(map[string]any{
		"name": "!!!", "webhook_url": validURL, "webhook_key": secret, "connector_name": "",
	}))
	if err != nil || n.ConnectorName != "vecnet-agent" {
		t.Fatalf("slug fallback connector: %v %s", err, n)
	}
	n, err = onboarding.Normalize(grokReq(map[string]any{
		"name": "Helper Bot", "webhook_url": validURL, "webhook_key": secret,
		"connector_name": "vecnet-custom-1", "skip_mcp": true,
	}))
	if err != nil || n.ConnectorName != "vecnet-custom-1" || !n.SkipMCP {
		t.Fatalf("override = %v %s", err, n)
	}
	if err := onboarding.Validate(grokReq(map[string]any{
		"name": "Helper Bot", "webhook_url": validURL, "webhook_key": secret, "skip_mcp": false,
	})); err != nil {
		t.Fatal(err)
	}
	err = onboarding.Validate(grokReq(map[string]any{
		"name": "Helper Bot", "webhook_url": validURL, "webhook_key": secret, "skip_mcp": "false",
	}))
	checkFE(t, err, secret, onboarding.CodeInvalidValue, "skip_mcp")

	longOK := "vecnet-" + strings.Repeat("a", 48)
	if got, err := onboarding.ConnectorName("ignored", longOK); err != nil || got != longOK {
		t.Fatalf("48-char override: %v %q", err, got)
	}
	for _, bad := range []string{
		"vecnet-",
		"Vecnet-ab",
		"vecnet-Ab",
		"vecnet_ab",
		"notvecnet-ab",
		"vecnet-" + strings.Repeat("a", 49),
		secret,
	} {
		_, err := onboarding.ConnectorName("Helper Bot", bad)
		checkFE(t, err, secret, onboarding.CodeInvalidValue, "connector_name")
		err = onboarding.Validate(grokReq(map[string]any{
			"name": "Helper Bot", "webhook_url": validURL, "webhook_key": secret, "connector_name": bad,
		}))
		checkFE(t, err, secret, onboarding.CodeInvalidValue, "connector_name")
	}
	if err := onboarding.ConnectorCollision("vecnet-helper", []string{"vecnet-other"}); err != nil {
		t.Fatal(err)
	}
	err = onboarding.ConnectorCollision("vecnet-helper", []string{"vecnet-other", "vecnet-helper"})
	checkFE(t, err, "", onboarding.CodeConnectorNameConflict, "connector_name")
}

func fieldNames(spec onboarding.KindSpec) []string {
	out := make([]string, len(spec.Fields))
	for i, f := range spec.Fields {
		out[i] = f.Name
	}
	return out
}

func mustField(t *testing.T, spec onboarding.KindSpec, name string) onboarding.FieldSpec {
	t.Helper()
	for _, f := range spec.Fields {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("%s missing field %s", spec.Kind, name)
	return onboarding.FieldSpec{}
}

func jsonNumber(t *testing.T, f onboarding.FieldSpec, want int) bool {
	t.Helper()
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m["default"] == float64(want)
}

func inviteReq(kind string, inputs map[string]any) onboarding.SubmitRequest {
	return onboarding.SubmitRequest{
		Kind: kind, SpecVersion: onboarding.SpecVersion, RequestID: "req-1", Inputs: inputs,
	}
}

func grokReq(inputs map[string]any) onboarding.SubmitRequest {
	return inviteReq("grokbot", inputs)
}

func cloneInputs(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func fakeKey(t *testing.T) string {
	t.Helper()
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal("rand")
	}
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	buf := make([]byte, len(raw))
	for i, b := range raw {
		buf[i] = alphabet[int(b)%len(alphabet)]
	}
	return "crsr_" + string(buf)
}

func sameBytes(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	diff := 0
	for i := 0; i < len(a); i++ {
		diff |= int(a[i] ^ b[i])
	}
	return diff == 0
}

func checkFE(t *testing.T, err error, secret, code, field string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s", code)
	}
	if secret != "" && containsSecret(err.Error(), secret) {
		t.Fatal("error contains secret material")
	}
	var fe *onboarding.FieldError
	if !errors.As(err, &fe) {
		t.Fatal("error is not a field error")
	}
	if secret != "" && (containsSecret(fe.Message, secret) || containsSecret(fe.Field, secret)) {
		t.Fatal("field error contains secret material")
	}
	if fe.Code != code || fe.Field != field {
		t.Fatalf("got code=%s field=%s", fe.Code, fe.Field)
	}
}

func containsSecret(msg, secret string) bool {
	if secret == "" || strings.Contains(msg, secret) {
		return secret != "" && strings.Contains(msg, secret)
	}
	rest, ok := strings.CutPrefix(secret, "crsr_")
	return ok && len(rest) >= 8 && strings.Contains(msg, rest)
}
