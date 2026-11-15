package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

type codexRuntimeInput struct {
	State    string `json:"state"`
	Delivery string `json:"delivery"`
	Reason   string `json:"reason,omitempty"`
	Version  string `json:"version,omitempty"`
}

var codexRuntimeReasons = []string{"connecting", "unreachable", "refused_version", "stopped", "expired", "archived", "not_loaded", "idle_stale", "approval_policy", "waiting_approval", "waiting_input", "over_limit", "identity_changed", "ownership_changed", "unverified_thread", "identity_unverified", "watch_stopped", "delivery_unresolved"}
var codexRuntimeVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

func validCodexRuntimeVersion(v string) bool {
	if len(v) > 128 {
		return false
	}
	m := codexRuntimeVersion.FindStringSubmatch(v)
	if m == nil {
		return false
	}
	for _, part := range strings.Split(m[4], ".") {
		if len(part) > 1 && part[0] == '0' && strings.Trim(part, "0123456789") == "" {
			return false
		}
	}
	return true
}
func readCodexRuntimeInput(r io.Reader) (codexRuntimeInput, error) {
	var v codexRuntimeInput
	raw, err := io.ReadAll(io.LimitReader(r, 1025))
	if err != nil || len(raw) > 1024 {
		return v, usageErr("invalid Codex runtime status")
	}
	node, err := parseJSONNode(raw)
	if err != nil || node.kind != '{' || validateCodexStateJSON(raw) != nil {
		return v, usageErr("invalid Codex runtime status")
	}
	for i, rawKey := range node.keys {
		var key, field string
		_ = json.Unmarshal(rawKey, &key)
		if !slices.Contains([]string{"state", "delivery", "reason", "version"}, key) {
			return v, usageErr("invalid Codex runtime status field")
		}
		if json.Unmarshal(node.vals[i].raw, &field) != nil || field == "" {
			return v, usageErr("invalid Codex runtime status field")
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&v) != nil {
		return v, usageErr("invalid Codex runtime status")
	}
	if !slices.Contains([]string{"idle", "working", "blocked", "offline"}, v.State) || !slices.Contains([]string{"ready", "unavailable", "unsupported"}, v.Delivery) || (v.Reason != "" && !slices.Contains(codexRuntimeReasons, v.Reason)) || (v.Version != "" && !validCodexRuntimeVersion(v.Version)) || (v.Delivery == "ready" && v.State != "idle" && v.State != "working") || (v.State == "offline" && v.Delivery != "unavailable") {
		return v, usageErr("invalid Codex runtime status")
	}
	return v, nil
}

// The daemon never reads credentials. Authenticate and publish while holding the
// same exact-owner lock as handoff; status grants no delivery or renewal rights.
func (a *app) cmdCodexReportStatus(args []string, nowTime func() time.Time) error {
	o, err := parseFlags("codex report-status", args, map[string]flagKind{"thread": strFlag, "codex-home": strFlag, "expect-generation": strFlag, "expect-hub": strFlag, "expect-caller": strFlag})
	if err != nil {
		return err
	}
	if len(o.pos) != 0 {
		return usageErr("codex report-status: unexpected arguments")
	}
	input, err := readCodexRuntimeInput(a.stdin)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Second)
	defer cancel()
	worker := *a
	worker.ctx = ctx
	worker.inHook = true
	_, unlock, err := worker.lockCodexOwner()
	if err != nil {
		return err
	}
	defer unlock()
	b, err := worker.codexOwner(o, nowTime())
	if err != nil {
		return err
	}
	c, _, _, err := worker.connect()
	if err != nil {
		return err
	}
	caller, err := worker.codexBindCaller(c)
	if err != nil {
		return err
	}
	if normalizeHub(c.base) != b.Hub || caller != b.Caller {
		return usageErr("Codex identity changed")
	}
	b, err = worker.codexOwner(o, nowTime())
	if err != nil {
		return err
	}
	ttl := int(b.ExpiresAt.Sub(nowTime())/time.Second) - 3
	if ttl > 90 {
		ttl = 90
	}
	if ttl < 1 {
		return usageErr("Codex ownership expires before status can be published")
	}
	// Leave room for the bounded network call near lease expiry rather than
	// extending a remote snapshot beyond the local owner's remaining authority.
	body := struct {
		codexRuntimeInput
		Runtime string `json:"runtime"`
		TTL     int    `json:"ttl_seconds"`
	}{input, "codex", ttl}
	var q url.Values
	if worker.as != "" {
		q = url.Values{"as": {worker.as}}
	}
	if err := c.do(ctx, http.MethodPost, "/v1/runtime-status", q, body, nil); err != nil {
		return err
	}
	return hookWrite(ctx, a.stdout, []byte("{\"reported\":true}\n"))
}
