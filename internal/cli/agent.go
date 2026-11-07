package cli

// Agent onboarding commands (PROTOCOL §§2.12, 12, 13.7 and 20.1).
// Kinds, submit and status go through the operator HTTP service. This file
// does not import the hub or the store, and it does not special-case any peer.
// Send and reply still require an explicit priority; nothing here supplies one.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strings"

	"github.com/oklog/ulid/v2"

	"github.com/XertroV/vecnet/internal/onboarding"
)

const agentUsage = "usage: vecnet agent kinds [--kind KIND] | " +
	"agent add --kind KIND [--name N] [--open-name] [--ttl 24h] [--principal PRN] [--url URL] [--key-file PATH] [--connector-name NAME] [--skip-mcp] [--request-id ID] [--confirm-revoke] | " +
	"agent link <peer> [--confirm-revoke] | agent status REQUEST_ID"

const (
	modeCLIBearer = "credential mode: CLI bearer; no link was requested"
	modeHosted    = "credential mode: hosted OAuth"
)

// connectorNameRE is the locked connector override. The hub applies the same
// rule; the CLI refuses a mismatch before it reads a key file.
var connectorNameRE = regexp.MustCompile(`^vecnet-[a-z0-9-]{1,48}$`)

var requestIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func (a *app) cmdAgent(args []string) error {
	o, err := parseFlags("agent", args, map[string]flagKind{
		"kind": strFlag, "name": strFlag, "open-name": boolFlag, "ttl": strFlag,
		"principal": strFlag, "url": strFlag, "key-file": strFlag,
		"connector-name": strFlag, "skip-mcp": boolFlag, "request-id": strFlag,
		"confirm-revoke": boolFlag,
	})
	if err != nil {
		return err
	}
	sub := ""
	if len(o.pos) > 0 {
		sub = o.pos[0]
		o.pos = o.pos[1:]
	}
	switch sub {
	case "kinds":
		return a.agentKinds(o)
	case "add":
		return a.agentAdd(o)
	case "link":
		return a.agentLink(o)
	case "status":
		return a.agentStatus(o)
	default:
		return usageErr(agentUsage)
	}
}

func (a *app) agentKinds(o opts) error {
	if err := rejectAgentFlags(o, "kind"); err != nil {
		return err
	}
	if len(o.pos) != 0 {
		return usageErr(agentUsage)
	}
	specs, err := a.fetchAgentKinds()
	if err != nil {
		return err
	}
	if kind := o.str("kind"); kind != "" {
		spec, ok := findAgentKind(specs, kind)
		if !ok {
			return usageErr("agent kinds: unknown kind " + kind)
		}
		if a.json {
			return a.emitValue(spec)
		}
		fmt.Fprintf(a.stdout, "%s %s %s %s\n", spec.Kind, spec.SpecVersion, spec.SubmitAction, spec.Label)
		if spec.PromptTemplate != "" {
			fmt.Fprintln(a.stdout, spec.PromptTemplate)
		}
		return nil
	}
	if a.json {
		return a.emitValue(struct {
			Kinds []onboarding.KindSpec `json:"kinds"`
		}{Kinds: specs})
	}
	for _, spec := range specs {
		fmt.Fprintf(a.stdout, "%s %s %s %s\n", spec.Kind, spec.SpecVersion, spec.SubmitAction, spec.Label)
	}
	return nil
}

func (a *app) agentAdd(o opts) error {
	if err := rejectAgentFlags(o, "kind", "name", "open-name", "ttl", "principal", "url", "key-file", "connector-name", "skip-mcp", "request-id", "confirm-revoke"); err != nil {
		return err
	}
	if len(o.pos) != 0 || o.str("kind") == "" {
		return usageErr(agentUsage)
	}
	specs, err := a.fetchAgentKinds()
	if err != nil {
		return err
	}
	spec, ok := findAgentKind(specs, o.str("kind"))
	if !ok {
		return usageErr("agent add: unknown kind " + o.str("kind"))
	}
	inputs, key, routine, err := a.agentInputs(o, spec)
	if err != nil {
		return err
	}
	id := o.str("request-id")
	if id == "" {
		id = a.retainedRequestID
	}
	if id == "" {
		id = "req-" + ulid.Make().String()
	}
	if !requestIDRE.MatchString(id) {
		return usageErr("agent add: --request-id must be 1 to 128 characters: letters, digits, and . _ : -")
	}
	// Keep this id for a relocation redispatch of the same command. A new
	// id after a verified follow would create a second peer.
	a.retainedRequestID = id
	body := map[string]any{
		"kind": spec.Kind, "spec_version": spec.SpecVersion, "request_id": id, "inputs": inputs,
	}
	// The id is on stderr before the POST so a committed submit with a lost
	// response can still be recovered. Automatic redispatch reuses this same id.
	if err := a.exposeAgentRequestID(id); err != nil {
		return err
	}
	var res onboarding.SubmitResult
	if err := a.postAgent(body, &res); err != nil {
		err = redactAgentError(err, key, routine)
		var he *hubError
		if errors.As(err, &he) && he.Code == onboarding.CodeStaleSpec {
			return a.reportStaleSpec(spec.Kind, spec.SpecVersion, he)
		}
		return a.noteLostAgentSubmit(id, err)
	}
	if res.Status != "created" {
		res.HumanOutput = ""
	}
	mode := credentialModeLine(spec.SubmitAction, inputs["skip_mcp"] == true)
	keepHuman := res.HumanOutput != ""
	if !wantsGuidedLink(spec, inputs, res.Status) {
		return a.showAgent(res, mode, keepHuman)
	}
	if err := a.confirmBearerRevoke(res.PeerID, o.has("confirm-revoke")); err != nil {
		if showErr := a.showAgent(res, mode, keepHuman); showErr != nil {
			return showErr
		}
		return err
	}
	var link map[string]any
	if err := a.call(http.MethodPost, "/v1/admin/dot-links", map[string]string{"peer_id": res.PeerID}, &link); err != nil {
		if showErr := a.showAgent(res, mode, keepHuman); showErr != nil {
			return showErr
		}
		return err
	}
	if a.json {
		return a.emitValue(agentView(res, mode, keepHuman, link))
	}
	if err := a.showAgent(res, mode, keepHuman); err != nil {
		return err
	}
	return a.emitObject(link, "code", "expires_at")
}

func (a *app) agentLink(o opts) error {
	if err := rejectAgentFlags(o, "confirm-revoke"); err != nil {
		return err
	}
	if len(o.pos) != 1 || o.pos[0] == "" {
		return usageErr(agentUsage)
	}
	if err := a.confirmBearerRevoke(o.pos[0], o.has("confirm-revoke")); err != nil {
		return err
	}
	return a.peerLink(o.pos[0])
}

func (a *app) agentStatus(o opts) error {
	if err := rejectAgentFlags(o); err != nil {
		return err
	}
	if len(o.pos) != 1 || !requestIDRE.MatchString(o.pos[0]) {
		return usageErr(agentUsage)
	}
	var res onboarding.SubmitResult
	if err := a.call(http.MethodGet, "/v1/admin/agents/onboarding/"+pathEscape(o.pos[0]), nil, &res); err != nil {
		return err
	}
	res.HumanOutput = ""
	return a.showAgent(res, "", false)
}

func (a *app) fetchAgentKinds() ([]onboarding.KindSpec, error) {
	var body struct {
		Kinds []onboarding.KindSpec `json:"kinds"`
	}
	if err := a.call(http.MethodGet, "/v1/admin/agent-kinds", nil, &body); err != nil {
		return nil, err
	}
	if body.Kinds == nil {
		body.Kinds = []onboarding.KindSpec{}
	}
	return body.Kinds, nil
}

func (a *app) exposeAgentRequestID(id string) error {
	_, err := fmt.Fprintf(a.stderr, "request_id %s\n", id)
	return err
}

// noteLostAgentSubmit tells the operator how to recover one id when the
// submit's outcome is ambiguous. Retry eligibility is separate: a malformed
// success or a deadline is not redriven, but either can follow a commit.
// A definite hub rejection is unchanged.
func (a *app) noteLostAgentSubmit(id string, err error) error {
	if err == nil || !ambiguousAgentSubmit(err) {
		return err
	}
	fmt.Fprintf(a.stderr, "request_id %s was not answered and may already be committed; recover with %s\n", id, a.agentRecoveryHint(id))
	return err
}

// agentRecoveryHint is the copy-paste status command and the same-id retry.
// The selected profile (--net, else VECNET_PROFILE) is single-quoted into
// both so a later run does not refuse or query another hub. A value that
// cannot be quoted safely is not interpolated; the operator must pass the
// original --net selection.
func (a *app) agentRecoveryHint(id string) string {
	profile := a.namedProfile()
	if profile == "" {
		return fmt.Sprintf("`vecnet agent status %s` or the same agent add with --request-id %s", id, id)
	}
	qp, okP := shellQuote(profile)
	qid, okID := shellQuote(id)
	if !okP || !okID {
		return fmt.Sprintf("`vecnet agent status %s` or the same agent add with --request-id %s; pass the original --net selection", id, id)
	}
	return fmt.Sprintf("`vecnet --net %s agent status %s` or the same agent add with --net %s --request-id %s", qp, qid, qp, qid)
}

// shellQuote returns a single-quoted POSIX word. Control characters are
// refused so the one-line hint cannot embed a break.
func shellQuote(s string) (string, bool) {
	if strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "", false
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'", true
}

// ambiguousAgentSubmit reports an outcome that may already be committed.
// It is wider than retryAgent: bad_response, a request deadline and
// cancellation are not redriven, and they still need the same-id recovery
// instructions.
func ambiguousAgentSubmit(err error) bool {
	if err == nil {
		return false
	}
	if retryAgent(err) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var he *hubError
	return errors.As(err, &he) && he.Code == "bad_response"
}

func (a *app) postAgent(body, out any) error {
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		last = a.call(http.MethodPost, "/v1/admin/agents", body, out)
		if last == nil || !retryAgent(last) || attempt == 1 {
			return last
		}
	}
	return last
}

func retryAgent(err error) bool {
	var ne *netError
	if errors.As(err, &ne) {
		return true
	}
	var he *hubError
	if errors.As(err, &he) {
		switch he.Status {
		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		}
	}
	return false
}

func (a *app) reportStaleSpec(kind, sent string, he *hubError) error {
	fresh := "unknown"
	if specs, err := a.fetchAgentKinds(); err == nil {
		if spec, ok := findAgentKind(specs, kind); ok {
			fresh = spec.SpecVersion
		}
	}
	he.Hint = "kind " + kind + " spec_version " + sent + " is stale; the hub now serves " + fresh + "; not resubmitted"
	return he
}

func (a *app) agentInputs(o opts, spec onboarding.KindSpec) (map[string]any, string, string, error) {
	known := map[string]onboarding.FieldSpec{}
	for _, f := range spec.Fields {
		if !supportedAgentType(f.Type) {
			return nil, "", "", usageErr(fmt.Sprintf("agent add: kind %s spec_version %s field %s has unsupported type %s", spec.Kind, spec.SpecVersion, f.Name, f.Type))
		}
		if _, ok := agentFieldFlag[f.Name]; !ok {
			return nil, "", "", usageErr(fmt.Sprintf("agent add: kind %s spec_version %s field %s is not supported by this CLI", spec.Kind, spec.SpecVersion, f.Name))
		}
		known[f.Name] = f
	}
	for flag, field := range agentFlagField {
		if o.has(flag) {
			if _, ok := known[field]; !ok {
				return nil, "", "", usageErr(fmt.Sprintf("agent add: --%s is not a field of kind %s spec_version %s", flag, spec.Kind, spec.SpecVersion))
			}
		}
	}
	inputs := map[string]any{}
	if _, ok := known["name"]; ok && o.has("name") {
		inputs["name"] = o.str("name")
	}
	if _, ok := known["name_open"]; ok && o.has("open-name") {
		inputs["name_open"] = true
	}
	if _, ok := known["enrol_ttl_seconds"]; ok && o.has("ttl") {
		secs, err := parseTTL(o.str("ttl"))
		if err != nil {
			return nil, "", "", err
		}
		inputs["enrol_ttl_seconds"] = secs
	}
	if _, ok := known["principal_id"]; ok && o.str("principal") != "" {
		inputs["principal_id"] = o.str("principal")
	}
	if f, ok := known["connector_name"]; ok && o.has("connector-name") {
		name := o.str("connector-name")
		if !connectorNameRE.MatchString(name) {
			return nil, "", "", usageErr("agent add: --connector-name must match " + connectorNameRE.String())
		}
		inputs[f.Name] = name
	}
	routine := ""
	if _, ok := known["webhook_url"]; ok {
		routine = o.str("url")
		if routine == "" {
			return nil, "", "", usageErr("agent add: --url is required for kind " + spec.Kind)
		}
		inputs["webhook_url"] = routine
	}
	if _, ok := known["skip_mcp"]; ok && o.has("skip-mcp") {
		inputs["skip_mcp"] = true
	}
	for _, f := range spec.Fields {
		if !f.Required {
			continue
		}
		if _, ok := inputs[f.Name]; ok {
			continue
		}
		if f.Name == "webhook_key" {
			continue
		}
		return nil, "", "", usageErr(fmt.Sprintf("agent add: --%s is required for kind %s", agentFieldFlag[f.Name], spec.Kind))
	}
	key := ""
	if _, ok := known["webhook_key"]; ok {
		if o.str("key-file") == "" {
			return nil, "", "", usageErr("agent add: --key-file is required for kind " + spec.Kind)
		}
		var err error
		key, err = a.readOnboardingKey(o.str("key-file"))
		if err != nil {
			return nil, "", "", err
		}
		inputs["webhook_key"] = key
	}
	return inputs, key, routine, nil
}

// readOnboardingKey reads one routine key for agent add. It is separate from
// route add/rotate: those commands still require a raw crsr_ value and refuse
// a Bearer prefix. Onboarding accepts one leading Bearer prefix, trims paste
// whitespace, and never puts the bytes in an error. The file is bounded to
// the same 4 KiB cap. Mode 0600 is recommended.
func (a *app) readOnboardingKey(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxKeyFile+1))
	if err != nil {
		return "", err
	}
	if len(raw) > maxKeyFile {
		return "", usageErr("key file is larger than 4 KiB")
	}
	key, err := normalizeOnboardingKey(string(raw))
	if err != nil {
		return "", err
	}
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 && runtime.GOOS != "windows" {
		a.warn("key file %s has mode %04o, readable by others; use chmod 600", path, mode)
	}
	return key, nil
}

func normalizeOnboardingKey(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", usageErr("key file is empty")
	}
	const prefix = "bearer "
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		s = s[len(prefix):]
		if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
			return "", usageErr("key file does not hold a routine key")
		}
	}
	rest, ok := strings.CutPrefix(s, "crsr_")
	if !ok || rest == "" {
		return "", usageErr("key file does not hold a routine key")
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] <= ' ' || rest[i] > '~' {
			return "", usageErr("key file does not hold a routine key")
		}
	}
	return s, nil
}

func (a *app) confirmBearerRevoke(peer string, confirm bool) error {
	cred, err := a.peerBearer(peer)
	if err != nil {
		return err
	}
	if cred != "active" {
		return nil
	}
	if !confirm {
		return usageErr("peer " + peer + " has a live bearer; the bearer will be revoked. Re-run with --confirm-revoke to link. There is no keep-bearer option.")
	}
	fmt.Fprintf(a.stderr, "linking %s; the bearer will be revoked\n", peer)
	return nil
}

func (a *app) peerBearer(peer string) (string, error) {
	var body struct {
		Peers []struct {
			Credential string `json:"credential"`
			Peer       struct {
				PeerID string `json:"peer_id"`
			} `json:"peer"`
		} `json:"peers"`
	}
	if err := a.call(http.MethodGet, "/v1/admin/peers", nil, &body); err != nil {
		return "", err
	}
	for _, row := range body.Peers {
		if row.Peer.PeerID == peer {
			return row.Credential, nil
		}
	}
	return "none", nil
}

func wantsGuidedLink(spec onboarding.KindSpec, inputs map[string]any, status string) bool {
	if status != "created" || spec.SubmitAction != onboarding.ActionGrokbotSetup {
		return false
	}
	return inputs["skip_mcp"] != true
}

func credentialModeLine(action string, skip bool) string {
	if action != onboarding.ActionGrokbotSetup {
		return ""
	}
	if skip {
		return modeCLIBearer
	}
	return modeHosted
}

func outcomeLine(res onboarding.SubmitResult) string {
	state := ""
	if res.Setup != nil {
		state = res.Setup.State
	}
	if state == onboarding.SetupQueued || state == onboarding.SetupHeld {
		return "outcome: queued, not connected"
	}
	if res.AuthState == onboarding.AuthVerified && (res.Setup == nil || state == onboarding.SetupProcessed) {
		return "outcome: connected"
	}
	return "outcome: not connected"
}

type agentJSON struct {
	RequestID      string                `json:"request_id,omitempty"`
	Kind           string                `json:"kind,omitempty"`
	PeerID         string                `json:"peer_id,omitempty"`
	PrincipalID    string                `json:"principal_id,omitempty"`
	RouteID        string                `json:"route_id,omitempty"`
	ConnectorName  string                `json:"connector_name,omitempty"`
	Setup          *onboarding.SetupInfo `json:"setup,omitempty"`
	AuthState      string                `json:"auth_state,omitempty"`
	SendDefaults   map[string]string     `json:"send_defaults,omitempty"`
	Status         string                `json:"status,omitempty"`
	WebPath        string                `json:"web_path,omitempty"`
	HumanOutput    string                `json:"human_output,omitempty"`
	Outcome        string                `json:"outcome,omitempty"`
	CredentialMode string                `json:"credential_mode,omitempty"`
	LinkCode       string                `json:"link_code,omitempty"`
	LinkExpiresAt  string                `json:"link_expires_at,omitempty"`
}

func agentView(res onboarding.SubmitResult, mode string, keepHuman bool, link map[string]any) agentJSON {
	if !keepHuman {
		res.HumanOutput = ""
	}
	view := agentJSON{
		RequestID: res.RequestID, Kind: res.Kind, PeerID: res.PeerID, PrincipalID: res.PrincipalID,
		RouteID: res.RouteID, ConnectorName: res.ConnectorName, Setup: res.Setup, AuthState: res.AuthState,
		SendDefaults: res.SendDefaults, Status: res.Status, WebPath: res.WebPath, HumanOutput: res.HumanOutput,
		Outcome: outcomeLine(res), CredentialMode: mode,
	}
	if link != nil {
		view.LinkCode, _ = link["code"].(string)
		view.LinkExpiresAt, _ = link["expires_at"].(string)
	}
	return view
}

func (a *app) showAgent(res onboarding.SubmitResult, mode string, keepHuman bool) error {
	if !keepHuman {
		res.HumanOutput = ""
	}
	if a.json {
		return a.emitValue(agentView(res, mode, keepHuman, nil))
	}
	if res.HumanOutput != "" {
		if _, err := fmt.Fprint(a.stdout, res.HumanOutput); err != nil {
			return err
		}
		a.writeAgentLines(a.stderr, res, mode)
		return nil
	}
	a.writeAgentLines(a.stdout, res, mode)
	return nil
}

func (a *app) writeAgentLines(w io.Writer, res onboarding.SubmitResult, mode string) {
	fmt.Fprintf(w, "request_id %s\n", res.RequestID)
	if res.PeerID != "" {
		fmt.Fprintf(w, "peer_id %s\n", res.PeerID)
	}
	if res.PrincipalID != "" {
		fmt.Fprintf(w, "principal_id %s\n", res.PrincipalID)
	}
	if res.RouteID != "" {
		fmt.Fprintf(w, "route_id %s\n", res.RouteID)
	}
	if res.ConnectorName != "" {
		fmt.Fprintf(w, "connector_name %s\n", res.ConnectorName)
	}
	if res.AuthState != "" {
		fmt.Fprintf(w, "auth_state %s\n", res.AuthState)
	}
	if res.Setup != nil {
		fmt.Fprintf(w, "setup thread_id %s message_id %s delivery_id %s state %s\n",
			res.Setup.ThreadID, res.Setup.MessageID, res.Setup.DeliveryID, res.Setup.State)
	}
	fmt.Fprintln(w, outcomeLine(res))
	if mode != "" {
		fmt.Fprintln(w, mode)
	}
}

func redactAgentError(err error, secrets ...string) error {
	var he *hubError
	if !errors.As(err, &he) {
		return err
	}
	blob := he.Message + fmt.Sprint(he.Details)
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		if strings.Contains(blob, secret) || strings.Contains(strings.ToLower(blob), strings.ToLower("bearer "+secret)) {
			he.Message = "the hub rejected the onboarding request"
			he.Details = nil
			return he
		}
	}
	return he
}

func supportedAgentType(t string) bool {
	switch t {
	case onboarding.FieldString, onboarding.FieldSecret, onboarding.FieldBoolean, onboarding.FieldInteger, onboarding.FieldEnum:
		return true
	default:
		return false
	}
}

var agentFieldFlag = map[string]string{
	"name": "name", "name_open": "open-name", "enrol_ttl_seconds": "ttl",
	"principal_id": "principal", "webhook_url": "url", "webhook_key": "key-file",
	"connector_name": "connector-name", "skip_mcp": "skip-mcp",
}

var agentFlagField = map[string]string{
	"name": "name", "open-name": "name_open", "ttl": "enrol_ttl_seconds",
	"principal": "principal_id", "url": "webhook_url", "key-file": "webhook_key",
	"connector-name": "connector_name", "skip-mcp": "skip_mcp",
}

func findAgentKind(specs []onboarding.KindSpec, kind string) (onboarding.KindSpec, bool) {
	for _, spec := range specs {
		if spec.Kind == kind {
			return spec, true
		}
	}
	return onboarding.KindSpec{}, false
}

func rejectAgentFlags(o opts, allowed ...string) error {
	ok := map[string]bool{}
	for _, name := range allowed {
		ok[name] = true
	}
	for name := range o.vals {
		if !ok[name] {
			return usageErr("agent: unknown flag --" + name)
		}
	}
	return nil
}
