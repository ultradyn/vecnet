package cli_test

import (
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
		w.Header().Set("ETag", prefsETag)
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(s.snap)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/prefs/preview":
		s.previews++
		s.ifMatch = append(s.ifMatch, r.Header.Get("If-Match"))
		s.previewBody = readBody(r)
		if s.conflict == "preview" && s.conflictLeft > 0 {
			s.conflictLeft--
			writePrefsErr(w, http.StatusPreconditionFailed, prefs.CodeRevisionConflict, "Preferences changed; reload and reconcile.")
			return
		}
		w.Header().Set("ETag", prefsETag)
		_, _ = w.Write(s.preview)
	case r.Method == http.MethodPatch && r.URL.Path == "/v1/admin/prefs":
		s.patches++
		s.ifMatch = append(s.ifMatch, r.Header.Get("If-Match"))
		s.patchBody = readBody(r)
		if s.rejectTen && prefsPatchSets(s.patchBody, "events.refresh_interval", 120) {
			writePrefsErr(w, http.StatusUnprocessableEntity, prefs.CodeInvalidValue, "Invalid preference value.")
			return
		}
		if s.conflict == "patch" && s.conflictLeft > 0 {
			s.conflictLeft--
			writePrefsErr(w, http.StatusPreconditionFailed, prefs.CodeRevisionConflict, "Preferences changed; reload and reconcile.")
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

func writePrefsErr(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"code": code, "message": message, "details": map[string]any{}},
	})
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
	if code != 1 || !strings.Contains(errs, "operator credential") {
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
		{[]string{"prefs"}, 1, "usage"},
		{[]string{"prefs", "set"}, 1, "usage"},
		{[]string{"prefs", "set", "--reset", "events.refresh_interval", "30m"}, 1, ""},
		{[]string{"prefs", "set", "events.refresh_interval", "1.5h"}, 1, "duration"},
		{[]string{"prefs", "set", "events.refresh_interval", "999999999999999d"}, 1, "duration"},
		{[]string{"prefs", "set", "agents.thread_budget", "10m"}, 1, ""},
		{[]string{"prefs", "set", "not.a.key", "1"}, 1, ""},
		{[]string{"prefs", "get"}, 1, ""},
		{[]string{"--net", "p", "prefs", "set", "events.refresh_interval", "120"}, 2, "prefs_invalid_value"},
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
		case tc.code == 1 && (g != beforeG || p != beforeP || w != beforeW):
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
	if code != 2 || !strings.Contains(errs, "prefs_revision_conflict") {
		t.Fatalf("conflict: exit %d\n%s\n%s", code, out, errs)
	}
	assertNoToken(t, out, errs)
	if g, p, w := s.counts(); g != 1 || p != 1 || w != 1 {
		t.Fatalf("retried a 412: get/preview/patch = %d/%d/%d", g, p, w)
	}

	s.conflict, s.conflictLeft = "preview", 1
	code, out, errs = u.run("--net", "p", "prefs", "set", "events.refresh_interval", "30m")
	if code != 2 || !strings.Contains(errs, "prefs_revision_conflict") {
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
	s.preview = prefsPreviewJSON(t, []prefs.Risk{prefsRisk("presence_shortened", "presence.default_ttl", "Shorter TTL may release held wakes.")})
	values := prefsReadValues()
	values["presence.default_ttl"] = prefs.Effective{Value: prefs.Duration(1800), Source: prefs.SourceStored}
	s.after = prefsSnapshot(t, values)
	srv := startPrefs(t, s)
	u := prefsUser(t, prefsProfile("p", srv.URL, "vn_operator_prefs"))

	code, out, errs := u.run("--net", "p", "prefs", "set", "presence.default_ttl", "30m", "--confirm-risk", "presence_shortened", "--apply-existing")
	if code != 0 || !lineHas(out, "presence.default_ttl", "30m", "stored") {
		t.Fatalf("confirm: exit %d\n%s\n%s", code, out, errs)
	}
	assertNoToken(t, out, errs)
	var patch prefs.Patch
	if err := json.Unmarshal(s.lastPatch(), &patch); err != nil {
		t.Fatal(err)
	}
	n, _ := patch.Set["presence.default_ttl"].Int64()
	if n != 1800 || !patch.ApplyExisting || len(patch.Confirm) != 1 || patch.Confirm[0] != prefsToken {
		t.Fatalf("confirmed patch = %+v", patch)
	}
	var proposed prefs.Patch
	if err := json.Unmarshal(s.lastPreview(), &proposed); err != nil {
		t.Fatal(err)
	}
	if !proposed.ApplyExisting || len(proposed.Confirm) != 0 {
		t.Fatalf("preview body = %+v", proposed)
	}
	if g, p, w := s.counts(); g != 1 || p != 1 || w != 1 {
		t.Fatalf("counts = %d/%d/%d", g, p, w)
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
	if code != 2 || !strings.Contains(errs, "archive_disabled") {
		t.Fatalf("unexpected: exit %d\n%s\n%s", code, out, errs)
	}
	assertNoToken(t, out, errs)
	if _, p, w := s.counts(); p != 1 || w != 0 {
		t.Fatalf("unexpected risk wrote: preview %d patch %d", p, w)
	}

	code, out, errs = u.run("--net", "p", "prefs", "set", "presence.default_ttl", "30m")
	if code != 2 || !strings.Contains(errs, "presence_shortened") || !strings.Contains(errs, "archive_disabled") {
		t.Fatalf("noninteractive: exit %d\n%s\n%s", code, out, errs)
	}
	assertNoToken(t, out, errs)
	if _, p, w := s.counts(); p != 2 || w != 0 {
		t.Fatalf("noninteractive wrote: preview %d patch %d", p, w)
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
