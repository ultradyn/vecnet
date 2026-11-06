package cli_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/cli"
	"github.com/XertroV/vecnet/internal/prefs"
)

const (
	prefsToken = "tok_prefs_do_not_print"
	prefsETag  = `"prefs-3-abc"`
	prefsETag2 = `"prefs-4-def"`
)

// prefsScript is a disposable preferences hub. It never reads a real profile.
type prefsScript struct {
	t              *testing.T
	secret         string
	snap           []byte
	preview        []byte
	after          []byte
	conflict       string // "patch" or "preview"
	conflictLeft   int    // how many of those calls return 412
	rejectTen      bool
	fail           *prefsFail
	mu             sync.Mutex
	gets           int
	previews       int
	patches        int
	paths          []string
	ifMatch        []string
	previewBody    []byte
	patchBody      []byte
	authorizations []string
}

func newPrefsScript(t *testing.T, secret string) *prefsScript {
	t.Helper()
	s := &prefsScript{
		t: t, secret: secret,
		snap:    prefsSnapshot(t, prefsReadValues()),
		preview: prefsPreviewJSON(t, nil),
		after:   prefsSnapshot(t, prefsReadValues()),
	}
	return s
}

func (s *prefsScript) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paths = append(s.paths, r.Method+" "+r.URL.Path)
	if r.URL.Path != "/v1/admin/prefs" && r.URL.Path != "/v1/admin/prefs/preview" {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+s.secret {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	s.authorizations = append(s.authorizations, r.Header.Get("Authorization"))
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/admin/prefs":
		s.gets++
		if s.failNow(w, "get") {
			return
		}
		w.Header().Set("ETag", prefsETag)
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(s.snap)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/prefs/preview":
		s.previews++
		s.ifMatch = append(s.ifMatch, r.Header.Get("If-Match"))
		s.previewBody = readBody(r)
		if rejectApplyExisting(w, s.previewBody) || s.failNow(w, "preview") {
			return
		}
		if s.conflict == "preview" && s.conflictLeft > 0 {
			s.conflictLeft--
			writePrefsErr(w, http.StatusPreconditionFailed, prefs.CodeRevisionConflict, "Preferences changed; reload and reconcile.", nil)
			return
		}
		w.Header().Set("ETag", prefsETag)
		_, _ = w.Write(s.preview)
	case r.Method == http.MethodPatch && r.URL.Path == "/v1/admin/prefs":
		s.patches++
		s.ifMatch = append(s.ifMatch, r.Header.Get("If-Match"))
		s.patchBody = readBody(r)
		if rejectApplyExisting(w, s.patchBody) || s.failNow(w, "patch") {
			return
		}
		if s.rejectTen && prefsPatchSets(s.patchBody, "events.refresh_interval", 120) {
			writePrefsErr(w, http.StatusUnprocessableEntity, prefs.CodeInvalidValue, "Invalid preference value.", map[string]any{"field": "events.refresh_interval", "constraint": "range"})
			return
		}
		if s.conflict == "patch" && s.conflictLeft > 0 {
			s.conflictLeft--
			writePrefsErr(w, http.StatusPreconditionFailed, prefs.CodeRevisionConflict, "Preferences changed; reload and reconcile.", nil)
			return
		}
		w.Header().Set("ETag", prefsETag2)
		_, _ = w.Write(s.after)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *prefsScript) counts() (gets, previews, patches int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gets, s.previews, s.patches
}

func (s *prefsScript) matched() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.ifMatch...)
}

func (s *prefsScript) lastPatch() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte{}, s.patchBody...)
}

func (s *prefsScript) lastPreview() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte{}, s.previewBody...)
}

func readBody(r *http.Request) []byte {
	defer r.Body.Close()
	raw, _ := io.ReadAll(r.Body)
	return raw
}

// prefsFail is one scripted hub error for get, preview, or patch.
type prefsFail struct {
	on      string
	status  int
	code    string
	message string
	details map[string]any
}

func (s *prefsScript) failNow(w http.ResponseWriter, on string) bool {
	if s.fail == nil || s.fail.on != on {
		return false
	}
	writePrefsErr(w, s.fail.status, s.fail.code, s.fail.message, s.fail.details)
	return true
}

func writePrefsErr(w http.ResponseWriter, status int, code, message string, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"code": code, "message": message, "details": details},
	})
}

// rejectApplyExisting mimics the hub: retrospective apply is only valid for
// retention.* keys, and a refusal writes nothing.
func rejectApplyExisting(w http.ResponseWriter, raw []byte) bool {
	var body struct {
		Set           map[string]json.RawMessage `json:"set"`
		Reset         []string                   `json:"reset"`
		ApplyExisting bool                       `json:"apply_existing"`
	}
	if json.Unmarshal(raw, &body) != nil || !body.ApplyExisting {
		return false
	}
	bad := ""
	for _, key := range body.Reset {
		if !strings.HasPrefix(key, "retention.") {
			bad = key
			break
		}
	}
	if bad == "" {
		for key := range body.Set {
			if !strings.HasPrefix(key, "retention.") {
				bad = key
				break
			}
		}
	}
	if bad == "" {
		return false
	}
	writePrefsErr(w, http.StatusUnprocessableEntity, prefs.CodeInvalidValue, "apply_existing is only supported for retention keys.", map[string]any{"field": bad, "constraint": "apply_existing"})
	return true
}

func prefsPatchSets(raw []byte, key string, want int64) bool {
	var body struct {
		Set map[string]json.RawMessage `json:"set"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return false
	}
	n, err := strconv.ParseInt(string(body.Set[key]), 10, 64)
	return err == nil && n == want
}

func prefsReadValues() map[string]prefs.Effective {
	return map[string]prefs.Effective{
		"events.refresh_interval": {Value: prefs.Duration(1800), Source: prefs.SourceFlag, Origin: "VECNET_EVENTS_REFRESH_INTERVAL"},
		"agents.thread_budget":    {Value: prefs.Int(500), Source: prefs.SourceDefault},
		"presence.default_ttl":    {Value: prefs.Duration(3600), Source: prefs.SourceStored},
		"retention.ack_events":    {Value: prefs.Duration(604800), Source: prefs.SourceDefault},
	}
}

func prefsSnapshot(t *testing.T, values map[string]prefs.Effective) []byte {
	t.Helper()
	defs := make([]prefs.Definition, 0, len(values))
	for key := range values {
		d, err := prefs.Lookup(key)
		if err != nil {
			t.Fatal(err)
		}
		defs = append(defs, d)
	}
	snap, err := prefs.NewSnapshot(3, prefsETag, defs, values)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func prefsPreviewJSON(t *testing.T, risks []prefs.Risk) []byte {
	t.Helper()
	eligible, pinned, bytesN := int64(2), int64(1), int64(30)
	body := prefs.Preview{
		BaseRevision: 3,
		Risks:        risks,
		Impact:       prefs.Impact{Eligible: &eligible, Pinned: &pinned, EstimatedBytes: &bytesN},
		ConfirmToken: prefsToken,
		ExpiresAt:    "2026-11-06T00:05:00.000Z",
	}
	if risks == nil {
		body.Risks = []prefs.Risk{}
		body.Impact = prefs.Impact{}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func prefsRisk(code, key, message string) prefs.Risk {
	return prefs.Risk{Code: code, Keys: []string{key}, Message: message}
}

// prefsUser writes an injected config and a decoy under HOME. The CLI must
// read VECNET_CONFIG and never the decoy, and it must not touch a real profile.
func prefsUser(t *testing.T, profiles string) *user {
	t.Helper()
	u := newUser(t)
	decoyDir := filepath.Join(u.env["HOME"], ".config", "vecnet")
	if err := os.MkdirAll(decoyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	decoy := "[profiles.p]\nhub = \"http://127.0.0.1:1\"\nsecret = \"vn_operator_decoy\"\nhub_pubkey = \"ed25519:decoy\"\n"
	if err := os.WriteFile(filepath.Join(decoyDir, "config.toml"), []byte(decoy), 0o600); err != nil {
		t.Fatal(err)
	}
	xdg := filepath.Join(t.TempDir(), "xdg")
	if err := os.MkdirAll(filepath.Join(xdg, "vecnet"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "vecnet", "config.toml"), []byte(decoy), 0o600); err != nil {
		t.Fatal(err)
	}
	u.env["XDG_CONFIG_HOME"] = xdg
	if err := os.MkdirAll(filepath.Dir(u.conf), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(u.conf, []byte(profiles), 0o600); err != nil {
		t.Fatal(err)
	}
	return u
}

func prefsProfile(name, hub, secret string) string {
	return "[profiles." + name + "]\nhub = " + jsonString(hub) + "\nsecret = " + jsonString(secret) + "\nhub_pubkey = \"ed25519:" + name + "\"\n"
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func startPrefs(t *testing.T, s *prefsScript) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(srv.Close)
	return srv
}

func assertNoToken(t *testing.T, out, errs string) {
	t.Helper()
	if strings.Contains(out, prefsToken) || strings.Contains(errs, prefsToken) || strings.Contains(out+errs, "confirm_token") {
		t.Fatalf("confirmation token leaked\nstdout: %s\nstderr: %s", out, errs)
	}
}

func TestPrefsUsageMentionsCommands(t *testing.T) {
	u := newUser(t)
	code, out, errs := u.run("help")
	if code != 0 || !strings.Contains(out, "prefs list") || !strings.Contains(out, "prefs get") || !strings.Contains(out, "--confirm-risk") || !strings.Contains(out, "--reset") {
		t.Fatalf("help: exit %d\nstdout: %s\nstderr: %s", code, out, errs)
	}
}

func TestPrefsProfileAndOperator(t *testing.T) {
	operator := newPrefsScript(t, "vn_operator_a")
	other := newPrefsScript(t, "vn_operator_b")
	a := startPrefs(t, operator)
	b := startPrefs(t, other)
	u := prefsUser(t, prefsProfile("a", a.URL, "vn_operator_a")+prefsProfile("b", b.URL, "vn_operator_b"))

	code, out, errs := u.run("--net", "a", "prefs", "list")
	if code != 0 || !strings.Contains(out, "events.refresh_interval") {
		t.Fatalf("profile a: exit %d\n%s\n%s", code, out, errs)
	}
	code, out, errs = u.run("--net", "b", "prefs", "list")
	if code != 0 || !strings.Contains(out, "events.refresh_interval") {
		t.Fatalf("profile b: exit %d\n%s\n%s", code, out, errs)
	}
	if g, _, _ := operator.counts(); g != 1 {
		t.Fatalf("profile a requests = %d, want 1", g)
	}
	if g, _, _ := other.counts(); g != 1 {
		t.Fatalf("profile b requests = %d, want 1", g)
	}

	peer := prefsUser(t, prefsProfile("p", a.URL, "vn_peer_not_operator"))
	before, _, _ := operator.counts()
	code, _, errs = peer.run("--net", "p", "prefs", "list")
	if code != 2 || !strings.Contains(errs, "operator credential") {
		t.Fatalf("peer: exit %d stderr %s", code, errs)
	}
	if g, p, w := operator.counts(); g != before || p != 0 || w != 0 {
		t.Fatalf("peer contacted preferences: gets %d previews %d patches %d", g, p, w)
	}
}

func TestPrefsListSourceAndGet(t *testing.T) {
	s := newPrefsScript(t, "vn_operator_prefs")
	srv := startPrefs(t, s)
	u := prefsUser(t, prefsProfile("p", srv.URL, "vn_operator_prefs"))

	code, out, errs := u.run("--net", "p", "prefs", "list")
	if code != 0 {
		t.Fatalf("list: exit %d\n%s\n%s", code, out, errs)
	}
	for _, parts := range [][]string{
		{"events.refresh_interval", "30m", "flag", "live"},
		{"agents.thread_budget", "500", "default", "live"},
		{"presence.default_ttl", "1h", "stored", "live"},
		{"retention.ack_events", "7d", "default", "unavailable"},
	} {
		if !lineHas(out, parts...) {
			t.Errorf("list missing %q\n%s", parts, out)
		}
	}

	code, out, errs = u.run("--net", "p", "--json", "prefs", "get", "events.refresh_interval")
	if code != 0 {
		t.Fatalf("get --json: exit %d\n%s\n%s", code, out, errs)
	}
	var got struct {
		Key    string `json:"key"`
		Source string `json:"source"`
		Origin string `json:"origin"`
		Value  int64  `json:"value"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Key != "events.refresh_interval" || got.Source != "flag" || got.Origin != "VECNET_EVENTS_REFRESH_INTERVAL" || got.Value != 1800 {
		t.Fatalf("get json = %+v", got)
	}
	if strings.Contains(out, prefsToken) {
		t.Fatalf("get printed a token: %s", out)
	}

	code, out, errs = u.run("--net", "p", "prefs", "get", "retention.ack_events")
	if code != 0 {
		t.Fatalf("get human: exit %d\n%s\n%s", code, out, errs)
	}
	for _, want := range []string{"retention.ack_events", "7d", "source: default", "unavailable", "DB retention adapter is not installed.", "604800"} {
		if !strings.Contains(out, want) {
			t.Errorf("get missing %q\n%s", want, out)
		}
	}
	code, out, errs = u.run("--net", "p", "prefs", "get", "events.refresh_interval")
	if code != 0 || !strings.Contains(out, "VECNET_EVENTS_REFRESH_INTERVAL") || !strings.Contains(out, "60") || !strings.Contains(out, "Refresh cadence") {
		t.Fatalf("get flag help: exit %d\n%s\n%s", code, out, errs)
	}
}

func TestPrefsSetDurationResetAndExitCodes(t *testing.T) {
	s := newPrefsScript(t, "vn_operator_prefs")
	s.rejectTen = true
	values := prefsReadValues()
	values["events.refresh_interval"] = prefs.Effective{Value: prefs.Duration(1800), Source: prefs.SourceStored}
	s.after = prefsSnapshot(t, values)
	srv := startPrefs(t, s)
	u := prefsUser(t, prefsProfile("p", srv.URL, "vn_operator_prefs"))

	code, out, errs := u.run("--net", "p", "prefs", "set", "events.refresh_interval", "30m")
	if code != 0 || !lineHas(out, "events.refresh_interval", "30m", "stored") {
		t.Fatalf("set 30m: exit %d\n%s\n%s", code, out, errs)
	}
	assertNoToken(t, out, errs)
	var patch prefs.Patch
	if err := json.Unmarshal(s.lastPatch(), &patch); err != nil {
		t.Fatal(err)
	}
	if n, ok := patch.Set["events.refresh_interval"].Int64(); !ok || n != 1800 || len(patch.Reset) != 0 || len(patch.Confirm) != 0 || patch.ApplyExisting {
		t.Fatalf("duration patch = %+v", patch)
	}
	if got := s.matched(); len(got) != 2 || got[0] != prefsETag || got[1] != prefsETag {
		t.Fatalf("If-Match = %q", got)
	}
	if g, p, w := s.counts(); g != 1 || p != 1 || w != 1 {
		t.Fatalf("counts get/preview/patch = %d/%d/%d", g, p, w)
	}

	code, out, errs = u.run("--net", "p", "prefs", "set", "--reset", "events.refresh_interval")
	if code != 0 {
		t.Fatalf("reset: exit %d\n%s\n%s", code, out, errs)
	}
	if err := json.Unmarshal(s.lastPatch(), &patch); err != nil {
		t.Fatal(err)
	}
	if len(patch.Set) != 0 || len(patch.Reset) != 1 || patch.Reset[0] != "events.refresh_interval" {
		t.Fatalf("reset patch = %+v", patch)
	}

	cases := []struct {
		args []string
		code int
		text string
	}{
		{[]string{"prefs"}, 2, "usage"},
		{[]string{"prefs", "set"}, 2, "usage"},
		{[]string{"prefs", "set", "--reset", "events.refresh_interval", "30m"}, 2, ""},
		{[]string{"prefs", "set", "events.refresh_interval", "1.5h"}, 2, "duration"},
		{[]string{"prefs", "set", "events.refresh_interval", "999999999999999d"}, 2, "duration"},
		{[]string{"prefs", "set", "agents.thread_budget", "10m"}, 2, ""},
		{[]string{"prefs", "set", "not.a.key", "1"}, 2, ""},
		{[]string{"prefs", "get"}, 2, ""},
		{[]string{"--net", "p", "prefs", "set", "events.refresh_interval", "120"}, 1, "prefs_invalid_value"},
	}
	for _, tc := range cases {
		beforeG, beforeP, beforeW := s.counts()
		code, out, errs = u.run(tc.args...)
		if code != tc.code {
			t.Errorf("%v: exit %d, want %d\n%s\n%s", tc.args, code, tc.code, out, errs)
		}
		if tc.text != "" && !strings.Contains(errs, tc.text) && !strings.Contains(out, tc.text) {
			t.Errorf("%v: output missing %q\n%s\n%s", tc.args, tc.text, out, errs)
		}
		g, p, w := s.counts()
		switch {
		case tc.code == 2 && (g != beforeG || p != beforeP || w != beforeW):
			t.Errorf("%v: usage contacted the hub (%d/%d/%d -> %d/%d/%d)", tc.args, beforeG, beforeP, beforeW, g, p, w)
		case tc.args[len(tc.args)-1] == "120" && (w != beforeW+1 || g != beforeG+1):
			t.Errorf("%v: validation counts %d/%d/%d -> %d/%d/%d", tc.args, beforeG, beforeP, beforeW, g, p, w)
		}
	}

	down := prefsUser(t, prefsProfile("p", "http://127.0.0.1:1", "vn_operator_prefs"))
	code, _, errs = down.run("--net", "p", "prefs", "list")
	if code != 3 || !strings.Contains(errs, "network") {
		t.Fatalf("network: exit %d stderr %s", code, errs)
	}
}

func TestPrefsConflictDoesNotRetry(t *testing.T) {
	s := newPrefsScript(t, "vn_operator_prefs")
	s.conflict, s.conflictLeft = "patch", 1
	srv := startPrefs(t, s)
	u := prefsUser(t, prefsProfile("p", srv.URL, "vn_operator_prefs"))
	code, out, errs := u.run("--net", "p", "prefs", "set", "events.refresh_interval", "30m")
	if code != 1 || !strings.Contains(errs, "prefs_revision_conflict") {
		t.Fatalf("conflict: exit %d\n%s\n%s", code, out, errs)
	}
	assertNoToken(t, out, errs)
	if g, p, w := s.counts(); g != 1 || p != 1 || w != 1 {
		t.Fatalf("retried a 412: get/preview/patch = %d/%d/%d", g, p, w)
	}

	s.conflict, s.conflictLeft = "preview", 1
	code, out, errs = u.run("--net", "p", "prefs", "set", "events.refresh_interval", "30m")
	if code != 1 || !strings.Contains(errs, "prefs_revision_conflict") {
		t.Fatalf("preview conflict: exit %d\n%s\n%s", code, out, errs)
	}
	if _, p, w := s.counts(); p != 2 || w != 1 {
		t.Fatalf("preview 412 was retried or patched: preview %d patch %d", p, w)
	}
}

func TestPrefsJSONRedactsConfirmToken(t *testing.T) {
	s := newPrefsScript(t, "vn_operator_prefs")
	s.preview = prefsPreviewJSON(t, []prefs.Risk{prefsRisk("presence_shortened", "presence.default_ttl", "Shorter TTL may release held wakes.")})
	srv := startPrefs(t, s)
	u := prefsUser(t, prefsProfile("p", srv.URL, "vn_operator_prefs"))

	code, out, errs := u.run("--json", "--net", "p", "prefs", "set", "presence.default_ttl", "30m", "--preview")
	if code != 0 {
		t.Fatalf("preview json: exit %d\n%s\n%s", code, out, errs)
	}
	assertNoToken(t, out, errs)
	var view map[string]any
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		t.Fatalf("preview json %q: %v", out, err)
	}
	risks, _ := view["risks"].([]any)
	if len(risks) != 1 || view["confirm_token"] != nil || !strings.Contains(out, "presence_shortened") {
		t.Fatalf("preview view = %s", out)
	}
	if _, p, w := s.counts(); p != 1 || w != 0 {
		t.Fatalf("preview wrote: preview %d patch %d", p, w)
	}

	code, out, errs = u.run("--json", "--net", "p", "prefs", "list")
	if code != 0 || strings.Contains(out, prefsToken) || strings.Contains(out, "confirm_token") {
		t.Fatalf("list json: exit %d\n%s\n%s", code, out, errs)
	}
}

func TestPrefsConfirmRiskAcceptsExactSet(t *testing.T) {
	s := newPrefsScript(t, "vn_operator_prefs")
	s.preview = prefsFullPreview(t, []prefs.Risk{
		prefsRisk("retention_shortened", "retention.ack_events", "New or shorter retention affects future rows; existing data needs separate approval."),
		prefsRisk("retention_apply_existing", "retention.ack_events", "Apply this bounded policy to existing data at the next nightly sweep."),
	}, retentionImpact())
	values := prefsReadValues()
	values["retention.ack_events"] = prefs.Effective{Value: prefs.Duration(86400), Source: prefs.SourceStored}
	s.after = prefsSnapshot(t, values)
	srv := startPrefs(t, s)
	u := prefsUser(t, prefsProfile("p", srv.URL, "vn_operator_prefs"))

	code, out, errs := u.run("--net", "p", "prefs", "set", "retention.ack_events", "1d", "--confirm-risk", "retention_shortened", "--confirm-risk", "retention_apply_existing", "--apply-existing")
	if code != 0 || !lineHas(out, "retention.ack_events", "1d", "stored") {
		t.Fatalf("confirm: exit %d\n%s\n%s", code, out, errs)
	}
	assertNoToken(t, out, errs)
	var patch prefs.Patch
	if err := json.Unmarshal(s.lastPatch(), &patch); err != nil {
		t.Fatal(err)
	}
	n, _ := patch.Set["retention.ack_events"].Int64()
	if n != 86400 || !patch.ApplyExisting || len(patch.Confirm) != 1 || patch.Confirm[0] != prefsToken {
		t.Fatalf("confirmed patch = %+v", patch)
	}
	var proposed prefs.Patch
	if err := json.Unmarshal(s.lastPreview(), &proposed); err != nil {
		t.Fatal(err)
	}
	if !proposed.ApplyExisting || len(proposed.Confirm) != 0 {
		t.Fatalf("preview body = %+v", proposed)
	}
	g, p, w := s.counts()
	if g != 1 || p != 1 || w != 1 {
		t.Fatalf("counts = %d/%d/%d", g, p, w)
	}

	beforeP, beforeW := p, w
	code, out, errs = u.run("--net", "p", "prefs", "set", "presence.default_ttl", "30m", "--apply-existing", "--confirm-risk", "presence_shortened")
	if code != 1 || !strings.Contains(errs, "prefs_invalid_value") || !strings.Contains(errs, "apply_existing") {
		t.Fatalf("non-retention apply_existing: exit %d\n%s\n%s", code, out, errs)
	}
	if _, p, w = s.counts(); p != beforeP+1 || w != beforeW {
		t.Fatalf("rejected apply_existing still patched: preview %d patch %d", p, w)
	}
}

func TestPrefsUnexpectedRiskRefuses(t *testing.T) {
	s := newPrefsScript(t, "vn_operator_prefs")
	s.preview = prefsPreviewJSON(t, []prefs.Risk{
		prefsRisk("presence_shortened", "presence.default_ttl", "Shorter TTL may release held wakes."),
		prefsRisk("archive_disabled", "retention.archive_mode", "Archive mode would stop protecting history."),
	})
	srv := startPrefs(t, s)
	u := prefsUser(t, prefsProfile("p", srv.URL, "vn_operator_prefs"))

	code, out, errs := u.run("--net", "p", "--json", "prefs", "set", "presence.default_ttl", "30m", "--confirm-risk", "presence_shortened")
	gotCode, message, details := decodePrefsErr(t, out)
	missing, _ := details["missing"].([]any)
	if code != 1 || gotCode != "prefs_confirmation_refused" || !strings.Contains(message, "archive_disabled") || !jsonListHas(missing, "archive_disabled") {
		t.Fatalf("unexpected: exit %d code %s message %s details %#v\n%s\n%s", code, gotCode, message, details, out, errs)
	}
	assertNoToken(t, out, errs)
	if _, p, w := s.counts(); p != 1 || w != 0 {
		t.Fatalf("unexpected risk wrote: preview %d patch %d", p, w)
	}

	code, out, errs = u.run("--net", "p", "prefs", "set", "presence.default_ttl", "30m")
	if code != 1 || !strings.Contains(errs, "presence_shortened") || !strings.Contains(errs, "archive_disabled") {
		t.Fatalf("noninteractive: exit %d\n%s\n%s", code, out, errs)
	}
	assertNoToken(t, out, errs)
	if _, p, w := s.counts(); p != 2 || w != 0 {
		t.Fatalf("noninteractive wrote: preview %d patch %d", p, w)
	}
}

func prefsFullPreview(t *testing.T, risks []prefs.Risk, impact prefs.Impact) []byte {
	t.Helper()
	body := prefs.Preview{
		BaseRevision: 3,
		Risks:        risks,
		Impact:       impact,
		ConfirmToken: prefsToken,
		ExpiresAt:    "2026-11-06T00:05:00.000Z",
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func retentionImpact() prefs.Impact {
	eligible, pinned, bytesN, high := int64(4), int64(1), int64(2048), int64(90)
	return prefs.Impact{
		CutoffAt:       "2026-11-05T12:00:00.000Z",
		HighWater:      &high,
		NotBefore:      "2026-11-06T02:00:00.000Z",
		Eligible:       &eligible,
		Pinned:         &pinned,
		EstimatedBytes: &bytesN,
		Storage:        &prefs.StorageUsage{RetainedBytes: 100, ReservedBytes: 20, UsedBytes: 120, LimitBytes: 5000},
	}
}

func decodePrefsErr(t *testing.T, out string) (string, string, map[string]any) {
	t.Helper()
	var env struct {
		Error struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("error json %q: %v", out, err)
	}
	if env.Error.Details == nil {
		t.Fatalf("details encoded as null: %s", out)
	}
	if strings.Contains(out, prefsToken) || strings.Contains(out, "confirm_token") {
		t.Fatalf("token in error json: %s", out)
	}
	return env.Error.Code, env.Error.Message, env.Error.Details
}

func jsonListHas(items []any, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func detailString(details map[string]any, key string) string {
	s, _ := details[key].(string)
	return s
}

func TestPrefsPreviewShowsImpact(t *testing.T) {
	s := newPrefsScript(t, "vn_operator_prefs")
	s.preview = prefsFullPreview(t, []prefs.Risk{
		prefsRisk("retention_apply_existing", "retention.history", "Apply this bounded policy to existing data at the next nightly sweep."),
	}, retentionImpact())
	values := prefsReadValues()
	values["retention.history"] = prefs.Effective{Value: prefs.Duration(604800), Source: prefs.SourceDefault}
	s.snap = prefsSnapshot(t, values)
	srv := startPrefs(t, s)
	u := prefsUser(t, prefsProfile("p", srv.URL, "vn_operator_prefs"))

	code, out, errs := u.run("--net", "p", "prefs", "set", "retention.history", "7d", "--preview", "--apply-existing")
	if code != 0 {
		t.Fatalf("preview: exit %d\n%s\n%s", code, out, errs)
	}
	assertNoToken(t, out, errs)
	for _, want := range []string{
		"risk retention_apply_existing:", "Apply this bounded policy", "keys: retention.history",
		"confirm: retention_apply_existing", "cutoff_at: 2026-11-05T12:00:00.000Z", "high_water: 90",
		"not_before: 2026-11-06T02:00:00.000Z", "eligible: 4", "pinned: 1", "estimated_bytes: 2048",
		"retained_bytes: 100", "reserved_bytes: 20", "used_bytes: 120", "limit_bytes: 5000",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("human preview missing %q\n%s", want, out)
		}
	}
	if _, p, w := s.counts(); p != 1 || w != 0 {
		t.Fatalf("preview wrote: preview %d patch %d", p, w)
	}

	code, out, errs = u.run("--json", "--net", "p", "prefs", "set", "retention.history", "7d", "--preview", "--apply-existing")
	if code != 0 {
		t.Fatalf("json preview: exit %d\n%s\n%s", code, out, errs)
	}
	assertNoToken(t, out, errs)
	var view struct {
		Impact struct {
			CutoffAt       string `json:"cutoff_at"`
			HighWater      *int64 `json:"high_water"`
			NotBefore      string `json:"not_before"`
			Eligible       *int64 `json:"eligible"`
			EstimatedBytes *int64 `json:"estimated_bytes"`
			Storage        *struct {
				RetainedBytes int64 `json:"retained_bytes"`
				ReservedBytes int64 `json:"reserved_bytes"`
				UsedBytes     int64 `json:"used_bytes"`
				LimitBytes    int64 `json:"limit_bytes"`
			} `json:"storage"`
		} `json:"impact"`
		ConfirmToken string `json:"confirm_token"`
	}
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		t.Fatal(err)
	}
	if view.ConfirmToken != "" || view.Impact.CutoffAt == "" || view.Impact.HighWater == nil || *view.Impact.HighWater != 90 || view.Impact.NotBefore == "" || view.Impact.Storage == nil || view.Impact.Storage.LimitBytes != 5000 || view.Impact.Eligible == nil || view.Impact.EstimatedBytes == nil {
		t.Fatalf("json preview impact = %s", out)
	}
}

func TestPrefsInteractiveConfirmShowsPreview(t *testing.T) {
	impact := retentionImpact()
	risk := prefsRisk("retention_apply_existing", "retention.history", "Apply this bounded policy to existing data at the next nightly sweep.")
	values := prefsReadValues()
	values["retention.history"] = prefs.Effective{Value: prefs.Duration(604800), Source: prefs.SourceStored}

	// beforePrompt is every impact and quota figure the operator must see,
	// plus the risk lines that name what they are approving. runPrefsTTY
	// writes the reply only after each of these precedes the prompt in the
	// one terminal stream.
	beforePrompt := []string{
		"risk retention_apply_existing:",
		"Apply this bounded policy",
		"keys: retention.history",
		"confirm: retention_apply_existing",
		"cutoff_at: 2026-11-05T12:00:00.000Z",
		"high_water: 90",
		"not_before: 2026-11-06T02:00:00.000Z",
		"eligible: 4",
		"pinned: 1",
		"estimated_bytes: 2048",
		"retained_bytes: 100",
		"reserved_bytes: 20",
		"used_bytes: 120",
		"limit_bytes: 5000",
	}
	const prompt = "type retention_apply_existing to confirm"

	t.Run("accepts the phrase after the preview", func(t *testing.T) {
		s := newPrefsScript(t, "vn_operator_prefs")
		s.preview = prefsFullPreview(t, []prefs.Risk{risk}, impact)
		s.after = prefsSnapshot(t, values)
		s.snap = prefsSnapshot(t, prefsReadValues())
		srv := startPrefs(t, s)
		u := prefsUser(t, prefsProfile("p", srv.URL, "vn_operator_prefs"))
		code, out, errs := runPrefsTTY(t, s, u, "retention_apply_existing\n", beforePrompt, prompt, "--net", "p", "prefs", "set", "retention.history", "7d", "--apply-existing")
		if code != 0 {
			t.Fatalf("accept: exit %d\n%s\n%s", code, out, errs)
		}
		assertNoToken(t, out, errs)
		for _, want := range beforePrompt {
			if !strings.Contains(out, want) {
				t.Errorf("terminal flow missing %q\n%s", want, out)
			}
		}
		if !strings.Contains(out, prompt) {
			t.Errorf("terminal flow missing %q\n%s", prompt, out)
		}
		var patch prefs.Patch
		if err := json.Unmarshal(s.lastPatch(), &patch); err != nil {
			t.Fatal(err)
		}
		if !patch.ApplyExisting || len(patch.Confirm) != 1 || patch.Confirm[0] != prefsToken {
			t.Fatalf("patch = %+v", patch)
		}
	})
	t.Run("a wrong phrase writes nothing", func(t *testing.T) {
		s := newPrefsScript(t, "vn_operator_prefs")
		s.preview = prefsFullPreview(t, []prefs.Risk{risk}, impact)
		srv := startPrefs(t, s)
		u := prefsUser(t, prefsProfile("p", srv.URL, "vn_operator_prefs"))
		code, out, errs := runPrefsTTY(t, s, u, "no\n", beforePrompt, prompt, "--net", "p", "prefs", "set", "retention.history", "7d", "--apply-existing")
		if code != 1 || !strings.Contains(out, "confirmation refused") {
			t.Fatalf("refuse: exit %d\n%s\n%s", code, out, errs)
		}
		assertNoToken(t, out, errs)
		for _, want := range beforePrompt {
			if !strings.Contains(out, want) {
				t.Errorf("refused transcript missing %q\n%s", want, out)
			}
		}
		if _, p, w := s.counts(); p != 1 || w != 0 {
			t.Fatalf("refused phrase wrote: preview %d patch %d", p, w)
		}
	})
}

// runPrefsTTY runs prefs with stdin, stdout, and stderr attached to one
// pseudoterminal and answers the confirmation prompt with reply.
//
// Preview lines and the prompt are writes to that single slave, in program
// order. The master is read into one buffer. A snapshot that contains prompt
// therefore already contains every byte written before it; the reply is not
// written until each beforePrompt entry starts earlier in that snapshot than
// prompt. There is no second stderr buffer that can show the prompt while the
// preview is still sitting in the terminal queue. The poll only waits until
// that snapshot exists. If the prompt is present and a required field is not
// already before it, the test fails immediately.
func runPrefsTTY(t *testing.T, s *prefsScript, u *user, reply string, beforePrompt []string, prompt string, args ...string) (int, string, string) {
	t.Helper()
	master, stdin, stdout := prefsPTY(t)
	var term safeBuf
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, 256)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				_, _ = term.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	codeCh := make(chan int, 1)
	go func() {
		// Same slave for stdout and stderr: one ordered terminal stream.
		codeCh <- cli.Run(args, stdin, stdout, stdout, u.getenv)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		seen := term.String()
		promptAt := strings.Index(seen, prompt)
		if promptAt >= 0 {
			for _, want := range beforePrompt {
				at := strings.Index(seen, want)
				if at < 0 || at >= promptAt {
					t.Fatalf("confirmation prompt before %q\n%s", want, seen)
				}
			}
			if _, _, w := s.counts(); w != 0 {
				t.Fatal("patched before confirmation")
			}
			break
		}
		select {
		case code := <-codeCh:
			t.Fatalf("exited %d before confirmation\n%s", code, seen)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the preview\n%s", seen)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := master.Write([]byte(reply)); err != nil {
		t.Fatal(err)
	}
	var code int
	select {
	case code = <-codeCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out after the confirmation phrase")
	}
	// Drop the slave so the master read returns the bytes already written,
	// including the line printed as the command exits.
	_ = stdin.Close()
	_ = stdout.Close()
	select {
	case <-readDone:
	case <-time.After(5 * time.Second):
		_ = master.Close()
		<-readDone
		t.Fatal("timed out draining the terminal")
	}
	_ = master.Close()
	return code, term.String(), ""
}

type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestPrefsUnnamedRiskRefuses(t *testing.T) {
	s := newPrefsScript(t, "vn_operator_prefs")
	s.preview = prefsPreviewJSON(t, []prefs.Risk{{Code: "", Keys: []string{"presence.default_ttl"}, Message: "wipe\x1b[2J"}})
	srv := startPrefs(t, s)
	u := prefsUser(t, prefsProfile("p", srv.URL, "vn_operator_prefs"))

	code, out, errs := u.run("--json", "--net", "p", "prefs", "set", "presence.default_ttl", "30m")
	got, message, details := decodePrefsErr(t, out)
	if code != 1 || got != "prefs_confirmation_refused" || !strings.Contains(message, "unnamed") || detailString(details, "constraint") != "risk_code" {
		t.Fatalf("sole unnamed: exit %d code %s message %s details %#v\n%s", code, got, message, details, errs)
	}
	if strings.Contains(out+errs, "\x1b") {
		t.Fatalf("hostile unnamed risk reached the terminal\n%s%s", out, errs)
	}
	if _, p, w := s.counts(); p != 1 || w != 0 {
		t.Fatalf("sole unnamed wrote: preview %d patch %d", p, w)
	}

	s.preview = prefsPreviewJSON(t, []prefs.Risk{
		prefsRisk("presence_shortened", "presence.default_ttl", "Shorter TTL may release held wakes."),
		{Code: "\x1b[31mbad", Keys: []string{"presence.default_ttl"}, Message: "hidden"},
	})
	code, out, errs = u.run("--json", "--net", "p", "prefs", "set", "presence.default_ttl", "30m", "--confirm-risk", "presence_shortened")
	got, message, details = decodePrefsErr(t, out)
	if code != 1 || got != "prefs_confirmation_refused" || detailString(details, "constraint") != "risk_code" || strings.Contains(message, "presence_shortened") {
		t.Fatalf("mixed unnamed: exit %d code %s message %s details %#v", code, got, message, details)
	}
	if strings.Contains(out+errs, "\x1b") {
		t.Fatalf("hostile mixed risk reached the terminal\n%s%s", out, errs)
	}
	if _, p, w := s.counts(); p != 2 || w != 0 {
		t.Fatalf("mixed unnamed wrote: preview %d patch %d", p, w)
	}
}

func TestPrefsJSONStructuredErrors(t *testing.T) {
	s := newPrefsScript(t, "vn_operator_prefs")
	srv := startPrefs(t, s)
	u := prefsUser(t, prefsProfile("p", srv.URL, "vn_operator_prefs"))

	check := func(t *testing.T, exit int, out, errs, wantCode, detailKey, detailVal string) {
		t.Helper()
		got, _, details := decodePrefsErr(t, out)
		if got != wantCode || detailString(details, detailKey) != detailVal {
			t.Fatalf("code %s details %#v\nstderr %s", got, details, errs)
		}
		assertNoToken(t, out, errs)
	}

	s.rejectTen = true
	code, out, errs := u.run("--json", "--net", "p", "prefs", "set", "events.refresh_interval", "120")
	if code != 1 {
		t.Fatalf("validation exit %d\n%s\n%s", code, out, errs)
	}
	check(t, code, out, errs, prefs.CodeInvalidValue, "constraint", "range")
	s.rejectTen = false

	s.conflict, s.conflictLeft = "patch", 1
	code, out, errs = u.run("--json", "--net", "p", "prefs", "set", "events.refresh_interval", "30m")
	if code != 1 {
		t.Fatalf("conflict exit %d\n%s\n%s", code, out, errs)
	}
	check(t, code, out, errs, prefs.CodeRevisionConflict, "code", "")
	if !strings.Contains(out, "prefs_revision_conflict") {
		t.Fatalf("conflict body %s", out)
	}
	s.conflict, s.conflictLeft = "", 0

	s.fail = &prefsFail{on: "preview", status: http.StatusConflict, code: prefs.CodeUnavailable, message: "adapter missing", details: map[string]any{"field": "retention.ack_events"}}
	code, out, errs = u.run("--json", "--net", "p", "prefs", "set", "retention.ack_events", "1d")
	if code != 1 {
		t.Fatalf("unavailable exit %d\n%s\n%s", code, out, errs)
	}
	check(t, code, out, errs, prefs.CodeUnavailable, "field", "retention.ack_events")

	s.fail = &prefsFail{on: "preview", status: http.StatusConflict, code: prefs.CodePolicyLocked, message: "hosted policy", details: map[string]any{"field": "blobs.expiry", "constraint": "policy"}}
	code, out, errs = u.run("--json", "--net", "p", "prefs", "set", "blobs.expiry", "off")
	if code != 1 {
		t.Fatalf("policy lock exit %d\n%s\n%s", code, out, errs)
	}
	check(t, code, out, errs, prefs.CodePolicyLocked, "constraint", "policy")

	s.fail = &prefsFail{on: "patch", status: http.StatusConflict, code: prefs.CodeConfirmationStale, message: "preview again", details: map[string]any{"constraint": "confirm"}}
	s.preview = prefsPreviewJSON(t, []prefs.Risk{prefsRisk("presence_shortened", "presence.default_ttl", "Shorter TTL may release held wakes.")})
	code, out, errs = u.run("--json", "--net", "p", "prefs", "set", "presence.default_ttl", "30m", "--confirm-risk", "presence_shortened")
	if code != 1 {
		t.Fatalf("stale exit %d\n%s\n%s", code, out, errs)
	}
	check(t, code, out, errs, prefs.CodeConfirmationStale, "constraint", "confirm")
	if _, _, w := s.counts(); w == 0 {
		t.Fatal("stale confirmation never reached PATCH")
	}

	s.fail = &prefsFail{on: "get", status: http.StatusUnauthorized, code: "unauthorized", message: "no"}
	code, out, errs = u.run("--json", "--net", "p", "prefs", "list")
	if code != 2 {
		t.Fatalf("auth exit %d\n%s\n%s", code, out, errs)
	}
	check(t, code, out, errs, "unauthorized", "code", "")

	code, out, errs = u.run("--json", "prefs", "set")
	if code != 2 {
		t.Fatalf("usage exit %d\n%s\n%s", code, out, errs)
	}
	got, _, details := decodePrefsErr(t, out)
	if got != "usage" || details == nil {
		t.Fatalf("usage json %s", out)
	}
}

// Nested objects and arrays in hub error details must not carry token or
// secret fields out through the JSON envelope. Safe siblings stay.
func TestPrefsNestedDetailsOmitSecrets(t *testing.T) {
	const (
		nestedSecret = "nested-secret-value"
		arraySecret  = "array-secret-value"
		keySecret    = "key-object-secret"
		nestedAuth   = "Bearer nested-auth"
		rawBody      = "raw-hub-body-secret"
	)
	s := newPrefsScript(t, "vn_operator_prefs")
	s.fail = &prefsFail{
		on: "preview", status: http.StatusConflict, code: prefs.CodePolicyLocked, message: "hosted policy",
		details: map[string]any{
			"field":         "blobs.expiry",
			"constraint":    "policy",
			"confirm_token": prefsToken,
			"secret":        "top-secret-value",
			"authorization": nestedAuth,
			"body":          rawBody,
			"nested": map[string]any{
				"confirm_token": prefsToken,
				"secret":        nestedSecret,
				"authorization": nestedAuth,
				"field":         "retention.history",
			},
			"items": []any{
				map[string]any{"token": prefsToken, "secret": arraySecret, "constraint": "range"},
				prefsToken,
			},
			"keys": []any{"retention.history", map[string]any{"token": keySecret, "secret": arraySecret}},
		},
	}
	srv := startPrefs(t, s)
	u := prefsUser(t, prefsProfile("p", srv.URL, "vn_operator_prefs"))

	code, out, errs := u.run("--json", "--net", "p", "prefs", "set", "blobs.expiry", "off")
	if code != 1 {
		t.Fatalf("policy lock exit %d\n%s\n%s", code, out, errs)
	}
	got, _, details := decodePrefsErr(t, out)
	if got != prefs.CodePolicyLocked || detailString(details, "field") != "blobs.expiry" || detailString(details, "constraint") != "policy" {
		t.Fatalf("safe details dropped: %#v\n%s", details, out)
	}
	keys, _ := details["keys"].([]any)
	if !jsonListHas(keys, "retention.history") {
		t.Fatalf("keys = %#v", details["keys"])
	}
	if _, ok := details["nested"]; ok {
		t.Fatalf("nested object survived: %#v", details["nested"])
	}
	if _, ok := details["items"]; ok {
		t.Fatalf("nested array survived: %#v", details["items"])
	}
	for _, secret := range []string{prefsToken, "top-secret-value", nestedSecret, arraySecret, keySecret, nestedAuth, rawBody, "confirm_token"} {
		if strings.Contains(out, secret) || strings.Contains(errs, secret) {
			t.Fatalf("secret %q leaked\nstdout %s\nstderr %s", secret, out, errs)
		}
	}

	code, out, errs = u.run("--net", "p", "prefs", "set", "blobs.expiry", "off")
	if code != 1 {
		t.Fatalf("human policy lock exit %d\n%s\n%s", code, out, errs)
	}
	for _, secret := range []string{prefsToken, nestedSecret, arraySecret, keySecret, nestedAuth, rawBody, "confirm_token"} {
		if strings.Contains(out, secret) || strings.Contains(errs, secret) {
			t.Fatalf("human secret %q leaked\nstdout %s\nstderr %s", secret, out, errs)
		}
	}
}

func lineHas(out string, parts ...string) bool {
	for _, line := range strings.Split(out, "\n") {
		ok := true
		for _, p := range parts {
			if !strings.Contains(line, p) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}
