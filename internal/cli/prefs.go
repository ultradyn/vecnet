package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/XertroV/vecnet/internal/prefs"
)

const prefsUsage = "usage: vecnet prefs list | prefs get KEY | prefs set KEY VALUE [--preview] [--apply-existing] [--confirm-risk CODE]… | prefs set --reset KEY"

// prefsConfirmRefused is the stable JSON code for a local risk refusal.
// The hub's own confirmation codes are passed through unchanged.
const prefsConfirmRefused = "prefs_confirmation_refused"

func (a *app) cmdPrefs(args []string) error {
	a.prefsCmd = true
	if len(args) == 0 {
		return usageErr(prefsUsage)
	}
	switch args[0] {
	case "list":
		return a.prefsList(args[1:])
	case "get":
		return a.prefsGet(args[1:])
	case "set":
		return a.prefsSet(args[1:])
	default:
		return usageErr(prefsUsage)
	}
}

func (a *app) prefsList(args []string) error {
	o, err := parseFlags("prefs list", args, map[string]flagKind{})
	if err != nil {
		return err
	}
	if len(o.pos) != 0 {
		return usageErr(prefsUsage)
	}
	c, err := a.operatorPrefs()
	if err != nil {
		return err
	}
	snap, err := a.readPrefs(c)
	if err != nil {
		return err
	}
	if a.json {
		return a.emitValue(snap)
	}
	for _, d := range snap.Descriptors() {
		fmt.Fprintln(a.stdout, prefLine(d))
	}
	return nil
}

func (a *app) prefsGet(args []string) error {
	o, err := parseFlags("prefs get", args, map[string]flagKind{})
	if err != nil {
		return err
	}
	if len(o.pos) != 1 {
		return usageErr(prefsUsage)
	}
	if _, err := prefs.Lookup(o.pos[0]); err != nil {
		return usageErr("unknown preference key")
	}
	c, err := a.operatorPrefs()
	if err != nil {
		return err
	}
	snap, err := a.readPrefs(c)
	if err != nil {
		return err
	}
	d, err := prefDescriptor(snap, o.pos[0])
	if err != nil {
		return err
	}
	if a.json {
		return a.emitValue(d)
	}
	fmt.Fprintln(a.stdout, d.Key)
	fmt.Fprintf(a.stdout, "value: %s\n", formatPrefValue(d))
	if d.Type == prefs.TypeDuration {
		if n, ok := d.Value.Int64(); ok {
			fmt.Fprintf(a.stdout, "seconds: %d\n", n)
		}
	}
	fmt.Fprintf(a.stdout, "source: %s\n", d.Source)
	if d.Origin != "" {
		fmt.Fprintf(a.stdout, "origin: %s\n", prefText(d.Origin))
	}
	if r := prefRange(d); r != "" {
		fmt.Fprintf(a.stdout, "range: %s\n", r)
	}
	if d.Description != "" {
		fmt.Fprintf(a.stdout, "effect: %s\n", prefText(d.Description))
	}
	fmt.Fprintf(a.stdout, "status: %s\n", prefLive(d))
	if d.UnavailableReason != "" {
		fmt.Fprintf(a.stdout, "lock: %s\n", prefText(d.UnavailableReason))
	}
	return nil
}

func (a *app) prefsSet(args []string) error {
	o, err := parseFlags("prefs set", args, map[string]flagKind{
		"reset": boolFlag, "preview": boolFlag, "apply-existing": boolFlag, "confirm-risk": multiFlag,
	})
	if err != nil {
		return err
	}
	var key string
	var value prefs.Value
	reset := o.has("reset")
	switch {
	case reset && len(o.pos) == 1:
		key = o.pos[0]
		if _, err := prefs.Lookup(key); err != nil {
			return usageErr("unknown preference key")
		}
	case !reset && len(o.pos) == 2:
		key = o.pos[0]
		def, err := prefs.Lookup(key)
		if err != nil {
			return usageErr("unknown preference key")
		}
		value, err = parsePrefScalar(def, o.pos[1])
		if err != nil {
			return err
		}
	default:
		return usageErr(prefsUsage)
	}
	c, err := a.operatorPrefs()
	if err != nil {
		return err
	}
	snap, err := a.readPrefs(c)
	if err != nil {
		return err
	}
	patch := prefs.Patch{Set: map[string]prefs.Value{}, Reset: []string{}, Confirm: []string{}, ApplyExisting: o.has("apply-existing")}
	if reset {
		patch.Reset = []string{key}
	} else {
		patch.Set[key] = value
	}
	preview, err := a.previewPrefs(c, snap.ETag, patch)
	if err != nil {
		return err
	}
	if o.has("preview") {
		return a.printPreview(preview)
	}
	// An unnamed risk is part of the hub's signed set. Dropping it and then
	// accepting the token would approve more than the operator named.
	if err := a.refuseUnusableRisks(preview.Risks); err != nil {
		return err
	}
	codes := riskCodes(preview.Risks)
	if len(codes) > 0 {
		if err := a.authorizeRisks(o.all("confirm-risk"), codes, preview); err != nil {
			return err
		}
		if preview.ConfirmToken == "" {
			return &refusedError{code: prefsConfirmRefused, msg: "prefs: preview did not include a confirmation; no change was written", details: map[string]any{"constraint": "confirm_token"}}
		}
		patch.Confirm = []string{preview.ConfirmToken}
	}
	next, err := a.patchPrefs(c, snap.ETag, patch)
	if err != nil {
		return err
	}
	if a.json {
		return a.emitValue(next)
	}
	d, err := prefDescriptor(next, key)
	if err != nil {
		return err
	}
	fmt.Fprintln(a.stdout, prefLine(d))
	return nil
}

func (a *app) operatorPrefs() (*client, error) {
	c, _, _, err := a.connect()
	if err != nil {
		return nil, err
	}
	if !isOperatorSecret(c.secret) {
		return nil, usageErr("forbidden: operator credential required")
	}
	return c, nil
}

func (a *app) readPrefs(c *client) (prefs.Snapshot, error) {
	var snap prefs.Snapshot
	etag, err := c.prefsCall(a.ctx, http.MethodGet, "/v1/admin/prefs", "", nil, &snap)
	if err != nil {
		return prefs.Snapshot{}, err
	}
	if etag == "" {
		return prefs.Snapshot{}, &hubError{Status: http.StatusPreconditionRequired, Code: prefs.CodePreconditionRequired, Message: "If-Match is required; read preferences first."}
	}
	snap.ETag = etag
	return snap, nil
}

func (a *app) previewPrefs(c *client, etag string, patch prefs.Patch) (prefs.Preview, error) {
	var out prefs.Preview
	_, err := c.prefsCall(a.ctx, http.MethodPost, "/v1/admin/prefs/preview", etag, patch, &out)
	return out, err
}

func (a *app) patchPrefs(c *client, etag string, patch prefs.Patch) (prefs.Snapshot, error) {
	var snap prefs.Snapshot
	next, err := c.prefsCall(a.ctx, http.MethodPatch, "/v1/admin/prefs", etag, patch, &snap)
	if err != nil {
		return prefs.Snapshot{}, err
	}
	snap.ETag = next
	return snap, nil
}

// prefsCall performs one preferences request. A non-2xx response, including a
// stale ETag, is returned once and not retried.
func (c *client) prefsCall(ctx context.Context, method, path, ifMatch string, body, out any) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := c.newRequest(ctx, method, path, nil, body)
	if err != nil {
		return "", err
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	resp, err := c.send(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	etag := resp.Header.Get("ETag")
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", &netError{err: err}
	}
	if out == nil {
		return etag, nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return "", &hubError{Status: resp.StatusCode, Code: "bad_response", Message: "the hub answered with unexpected JSON: " + err.Error()}
	}
	return etag, nil
}

func (a *app) printPreview(preview prefs.Preview) error {
	view := struct {
		BaseRevision uint64       `json:"base_revision"`
		Risks        []prefs.Risk `json:"risks"`
		Impact       prefs.Impact `json:"impact"`
		ExpiresAt    string       `json:"expires_at"`
	}{preview.BaseRevision, preview.Risks, preview.Impact, preview.ExpiresAt}
	if view.Risks == nil {
		view.Risks = []prefs.Risk{}
	}
	if a.json {
		return a.emitValue(view)
	}
	a.writePreviewText(preview)
	return nil
}

// writePreviewText is the token-free human preview used by --preview and by
// the interactive confirmation prompt. It shows every safe impact figure the
// hub signed so the operator can see it before approving.
func (a *app) writePreviewText(preview prefs.Preview) {
	fmt.Fprintf(a.stdout, "revision: %d\n", preview.BaseRevision)
	if len(preview.Risks) == 0 {
		fmt.Fprintln(a.stdout, "confirm: none")
	}
	for _, risk := range preview.Risks {
		code := prefText(risk.Code)
		msg := prefText(risk.Message)
		if code == "" {
			fmt.Fprintf(a.stdout, "risk: %s\n", msg)
		} else {
			fmt.Fprintf(a.stdout, "risk %s: %s\n", code, msg)
		}
		if len(risk.Keys) > 0 {
			keys := make([]string, len(risk.Keys))
			for i, key := range risk.Keys {
				keys[i] = prefText(key)
			}
			fmt.Fprintf(a.stdout, "keys: %s\n", strings.Join(keys, " "))
		}
	}
	if codes := riskCodes(preview.Risks); len(codes) > 0 {
		fmt.Fprintf(a.stdout, "confirm: %s\n", strings.Join(codes, " "))
	}
	imp := preview.Impact
	if imp.CutoffAt != "" {
		fmt.Fprintf(a.stdout, "cutoff_at: %s\n", prefText(imp.CutoffAt))
	}
	if imp.HighWater != nil {
		fmt.Fprintf(a.stdout, "high_water: %d\n", *imp.HighWater)
	}
	if imp.NotBefore != "" {
		fmt.Fprintf(a.stdout, "not_before: %s\n", prefText(imp.NotBefore))
	}
	if imp.Eligible != nil {
		fmt.Fprintf(a.stdout, "eligible: %d\n", *imp.Eligible)
	}
	if imp.Pinned != nil {
		fmt.Fprintf(a.stdout, "pinned: %d\n", *imp.Pinned)
	}
	if imp.EstimatedBytes != nil {
		fmt.Fprintf(a.stdout, "estimated_bytes: %d\n", *imp.EstimatedBytes)
	}
	if s := imp.Storage; s != nil {
		fmt.Fprintf(a.stdout, "retained_bytes: %d\n", s.RetainedBytes)
		fmt.Fprintf(a.stdout, "reserved_bytes: %d\n", s.ReservedBytes)
		fmt.Fprintf(a.stdout, "used_bytes: %d\n", s.UsedBytes)
		fmt.Fprintf(a.stdout, "limit_bytes: %d\n", s.LimitBytes)
	}
	if preview.ExpiresAt != "" {
		fmt.Fprintf(a.stdout, "expires_at: %s\n", prefText(preview.ExpiresAt))
	}
}

// authorizeRisks accepts a noninteractive --confirm-risk set that covers every
// preview risk, or an exact phrase typed at a terminal after the safe preview
// is shown. Anything else writes nothing. The confirmation token is never printed.
func (a *app) authorizeRisks(accepted, required []string, preview prefs.Preview) error {
	named := make([]string, 0, len(accepted))
	have := map[string]bool{}
	for _, code := range accepted {
		if code == "" {
			continue
		}
		named = append(named, code)
		have[code] = true
	}
	if len(named) > 0 {
		var missing []string
		for _, code := range required {
			if !have[code] {
				missing = append(missing, code)
			}
		}
		if len(missing) > 0 {
			return &refusedError{code: prefsConfirmRefused, msg: "prefs: unexpected risk " + strings.Join(missing, ", ") + "; no change was written", details: map[string]any{"missing": missing}}
		}
		return nil
	}
	phrase := strings.Join(required, " ")
	refused := &refusedError{code: prefsConfirmRefused, msg: "prefs: confirmation required (" + phrase + "); re-run with --confirm-risk for each risk", details: map[string]any{"missing": append([]string{}, required...)}}
	// JSON mode never prompts. A terminal operator sees the full preview first.
	if a.json || !isTerminal(a.stdin) || !isTerminal(a.stdout) {
		return refused
	}
	if err := a.printPreview(preview); err != nil {
		return err
	}
	fmt.Fprintf(a.stderr, "type %s to confirm\n", phrase)
	line, err := bufio.NewReader(a.stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if strings.TrimSpace(line) != phrase {
		return &refusedError{code: prefsConfirmRefused, msg: "prefs: confirmation refused; no change was written", details: map[string]any{"constraint": "confirmation"}}
	}
	return nil
}

// refuseUnusableRisks rejects an empty or non-identifier risk code before any
// PATCH. The hub token binds the whole risk set, so an unnamed entry cannot
// be skipped just because --confirm-risk listed the others.
func (a *app) refuseUnusableRisks(risks []prefs.Risk) error {
	for _, risk := range risks {
		if usableRiskCode(risk.Code) {
			continue
		}
		return &refusedError{code: prefsConfirmRefused, msg: "prefs: preview included an unnamed risk; no change was written", details: map[string]any{"constraint": "risk_code"}}
	}
	return nil
}

func usableRiskCode(code string) bool {
	if code == "" || len(code) > 64 {
		return false
	}
	for i, r := range code {
		switch {
		case r >= 'a' && r <= 'z':
		case (r == '_' || r >= '0' && r <= '9') && i > 0:
		default:
			return false
		}
	}
	return true
}

// exitPrefs maps a preferences failure onto the preferences contract:
// usage exits 2, validation/conflict/refusal exit 1, and authentication and
// transport keep the process-wide codes. JSON mode prints one error envelope
// and never a confirmation token or raw hub body.
func (a *app) exitPrefs(err error) int {
	var ue *usageError
	var he *hubError
	var ne *netError
	var re *refusedError
	switch {
	case errors.As(err, &ne):
		a.reportPrefs("network", ne.Error(), nil, "", false)
		return exitNetwork
	case errors.As(err, &ue):
		a.reportPrefs("usage", ue.msg, nil, "", false)
		return 2
	case errors.As(err, &re):
		code := re.code
		if code == "" {
			code = prefsConfirmRefused
		}
		a.reportPrefs(code, re.msg, re.details, "", false)
		return 1
	case errors.As(err, &he):
		a.reportPrefs(he.Code, he.Message, he.Details, he.Hint, true)
		if prefsOperatorFault(he.Code) {
			return 1
		}
		return exitHub
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		a.reportPrefs("interrupted", "interrupted", nil, "", false)
		return exitUsage
	default:
		a.reportPrefs("error", err.Error(), nil, "", false)
		return exitUsage
	}
}

func prefsOperatorFault(code string) bool {
	switch code {
	case prefs.CodeInvalidValue, prefs.CodeUnknownKey, prefs.CodeRevisionConflict,
		prefs.CodeConfirmationRequired, prefs.CodeConfirmationStale,
		prefs.CodeUnavailable, prefs.CodePolicyLocked:
		return true
	default:
		return false
	}
}

func (a *app) reportPrefs(code, message string, details map[string]any, hint string, hubFormat bool) {
	message = prefText(message)
	hint = prefText(hint)
	details = safePrefsDetails(details)
	if a.json {
		_ = a.emitValue(map[string]any{
			"error": map[string]any{
				"code":    code,
				"message": message,
				"details": details,
			},
		})
		return
	}
	if hubFormat {
		fmt.Fprintf(a.stderr, "%s: %s\n", code, message)
		if hint != "" {
			fmt.Fprintln(a.stderr, hint)
		}
		return
	}
	fmt.Fprintf(a.stderr, "vecnet: %s\n", message)
}

// safePrefsDetails projects preference error details onto the closed vocabulary
// the CLI publishes: field, constraint, keys, and the local missing-risk list.
// Nested objects, non-string array elements, and every other key are dropped,
// so a hub cannot smuggle a token or secret through details.
func safePrefsDetails(in map[string]any) map[string]any {
	out := map[string]any{}
	if s, ok := prefsDetailString(in["field"]); ok {
		out["field"] = s
	}
	if s, ok := prefsDetailString(in["constraint"]); ok {
		out["constraint"] = s
	}
	if keys, ok := prefsDetailStrings(in["keys"]); ok {
		out["keys"] = keys
	}
	if missing, ok := prefsDetailStrings(in["missing"]); ok {
		out["missing"] = missing
	}
	return out
}

func prefsDetailString(v any) (string, bool) {
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	s = prefText(s)
	if s == "" {
		return "", false
	}
	return s, true
}

func prefsDetailStrings(v any) ([]string, bool) {
	var raw []string
	switch x := v.(type) {
	case []string:
		raw = x
	case []any:
		raw = make([]string, 0, len(x))
		for _, item := range x {
			s, ok := item.(string)
			if !ok {
				continue
			}
			raw = append(raw, s)
		}
	default:
		return nil, false
	}
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		s = prefText(s)
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

func riskCodes(risks []prefs.Risk) []string {
	seen := map[string]bool{}
	codes := make([]string, 0, len(risks))
	for _, risk := range risks {
		if risk.Code == "" || seen[risk.Code] {
			continue
		}
		seen[risk.Code] = true
		codes = append(codes, risk.Code)
	}
	sort.Strings(codes)
	return codes
}

func prefDescriptor(snap prefs.Snapshot, key string) (prefs.Descriptor, error) {
	for _, d := range snap.Descriptors() {
		if d.Key == key {
			return d, nil
		}
	}
	return prefs.Descriptor{}, usageErr("unknown preference key")
}

func prefLine(d prefs.Descriptor) string {
	return fmt.Sprintf("%s  %s  %s  %s", d.Key, formatPrefValue(d), d.Source, prefLive(d))
}

// prefText flattens operator-visible preference text. Descriptions and risk
// messages are hub text and must not inject terminal controls.
func prefText(s string) string { return unbidi(flatten(s)) }

func prefLive(d prefs.Descriptor) string {
	if d.Runtime == "live" && d.Editable && d.UnavailableReason == "" {
		return "live"
	}
	return "unavailable"
}

func formatPrefValue(d prefs.Descriptor) string {
	switch d.Type {
	case prefs.TypeBool:
		v, _ := d.Value.Bool()
		return strconv.FormatBool(v)
	case prefs.TypeEnum:
		v, _ := d.Value.Enum()
		return v
	case prefs.TypeDuration:
		n, _ := d.Value.Int64()
		return formatPrefDuration(n)
	default:
		n, _ := d.Value.Int64()
		return strconv.FormatInt(n, 10)
	}
}

func formatPrefDuration(sec int64) string {
	switch {
	case sec == 0:
		return "0s"
	case sec%86400 == 0:
		return fmt.Sprintf("%dd", sec/86400)
	case sec%3600 == 0:
		return fmt.Sprintf("%dh", sec/3600)
	case sec%60 == 0:
		return fmt.Sprintf("%dm", sec/60)
	default:
		return fmt.Sprintf("%ds", sec)
	}
}

func prefRange(d prefs.Descriptor) string {
	if len(d.AllowedValues) > 0 {
		return strings.Join(d.AllowedValues, "|")
	}
	if d.Min == nil || d.Max == nil {
		return ""
	}
	text := fmt.Sprintf("%d..%d", *d.Min, *d.Max)
	if d.Unit != "" {
		text += " " + d.Unit
	}
	if d.DisabledValue != nil && *d.DisabledValue == 0 {
		text += " (0 disables)"
	}
	return text
}

func parsePrefScalar(def prefs.Definition, raw string) (prefs.Value, error) {
	switch def.Type {
	case prefs.TypeBool:
		switch raw {
		case "true":
			return prefs.Bool(true), nil
		case "false":
			return prefs.Bool(false), nil
		default:
			return prefs.Value{}, usageErr("invalid boolean for " + def.Key)
		}
	case prefs.TypeEnum:
		return prefs.Enum(raw), nil
	case prefs.TypeInt:
		n, err := parseWhole(raw)
		if err != nil {
			return prefs.Value{}, usageErr("invalid integer for " + def.Key)
		}
		return prefs.Int(n), nil
	case prefs.TypeDuration:
		n, err := parseDurationSeconds(raw)
		if err != nil {
			return prefs.Value{}, usageErr("invalid duration for " + def.Key)
		}
		return prefs.Duration(n), nil
	default:
		return prefs.Value{}, usageErr("unknown preference type")
	}
}

func parseWhole(raw string) (int64, error) {
	if raw == "" {
		return 0, errors.New("empty")
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, errors.New("digit")
		}
	}
	return strconv.ParseInt(raw, 10, 64)
}

func parseDurationSeconds(raw string) (int64, error) {
	if raw == "" {
		return 0, errors.New("empty")
	}
	unit := int64(1)
	num := raw
	last := raw[len(raw)-1]
	if last < '0' || last > '9' {
		switch last {
		case 's', 'S':
			unit = 1
		case 'm', 'M':
			unit = 60
		case 'h', 'H':
			unit = 3600
		case 'd', 'D':
			unit = 86400
		default:
			return 0, errors.New("unit")
		}
		num = raw[:len(raw)-1]
	}
	n, err := parseWhole(num)
	if err != nil {
		return 0, err
	}
	if unit != 0 && uint64(n) > uint64(math.MaxInt64)/uint64(unit) {
		return 0, errors.New("overflow")
	}
	return n * unit, nil
}
