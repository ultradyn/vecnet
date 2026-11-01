package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/mcpserver"
	"github.com/XertroV/vecnet/internal/testhub"
	"github.com/XertroV/vecnet/internal/webhook"
)

const routineURL = "https://api2.cursor.sh/automations/webhook/123e4567-e89b-12d3-a456-426614174000"

var linkCodeRe = regexp.MustCompile(`^[ABCDEFGHJKMNPQRSTUVWXYZ23456789]{8}$`)

// field returns the value of the "key value" line of a command's human output.
func field(t *testing.T, out, key string) string {
	t.Helper()
	for _, l := range nonEmptyLines(out) {
		if v, ok := strings.CutPrefix(l, key+" "); ok {
			return v
		}
	}
	t.Fatalf("no %q line in %q", key, out)
	return ""
}

func TestPeerLinkPrintsCode(t *testing.T) {
	env := testhub.Start(t)
	op := profileUser(t, env.URL, env.OperatorSecret)
	ctx := context.Background()
	dot, _ := env.NewPeer("dot", "d")

	code := field(t, op.mustRun("peer", "link", dot), "code")
	if !linkCodeRe.MatchString(code) {
		t.Fatalf("code = %q, want 8 characters of the link alphabet", code)
	}
	if got, err := env.Hub.ConsumeDotLink(ctx, code); err != nil || got != dot {
		t.Fatalf("ConsumeDotLink(%q) = %q, %v; want %s", code, got, err, dot)
	}

	before := env.Now.Now()
	out := op.mustRun("--json", "peer", "link", dot)
	var body struct {
		Code      string `json:"code"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		t.Fatalf("peer link --json output %q: %v", out, err)
	}
	if !linkCodeRe.MatchString(body.Code) {
		t.Fatalf("--json code = %q", body.Code)
	}
	exp, err := envelope.ParseTime(body.ExpiresAt)
	if err != nil {
		t.Fatalf("expires_at %q: %v", body.ExpiresAt, err)
	}
	if want := before.Add(10 * time.Minute); !exp.Equal(want) {
		t.Fatalf("expires_at = %v, want %v", exp, want)
	}

	// The human form shows the expiry too.
	if got := field(t, op.mustRun("peer", "link", dot), "expires_at"); got != body.ExpiresAt {
		t.Fatalf("human expires_at = %q, want %q", got, body.ExpiresAt)
	}

	session, _ := env.NewPeer("session", "s")
	if code, _, errs := op.run("peer", "link", session); code != 2 || !strings.HasPrefix(errs, "invalid_value: ") {
		t.Fatalf("peer link of a session = %d %q, want 2 invalid_value", code, errs)
	}
	if code, _, errs := op.run("peer", "link"); code != 1 || !strings.Contains(errs, "usage") {
		t.Fatalf("peer link with no peer = %d %q, want 1 usage", code, errs)
	}
}

func TestSourceAddPrintsURLAndSecret(t *testing.T) {
	env := testhub.Start(t)
	op := profileUser(t, env.URL, env.OperatorSecret)
	ctx := context.Background()
	target, _ := env.NewPeer("session", "t")
	secretRe := regexp.MustCompile(`^[0-9a-f]{64}$`)

	out := op.mustRun("source", "add", "--kind", "github", "--name", "gh", "--route", target)
	id := field(t, out, "source_id")
	ingress, secret := field(t, out, "ingress_url"), field(t, out, "secret")
	if !strings.HasPrefix(id, "src_") || ingress != env.URL+"/v1/ingress/"+id {
		t.Fatalf("ingress_url = %q for %q, want %s/v1/ingress/src_…", ingress, id, env.URL)
	}
	if !secretRe.MatchString(secret) {
		t.Fatalf("secret = %q, want 64 hex", secret)
	}
	if _, stored, err := env.Hub.SourceSecret(ctx, id); err != nil || stored != secret {
		t.Fatalf("SourceSecret: %q, %v; want the printed secret", stored, err)
	}

	out = op.mustRun("--json", "source", "add", "--kind", "github", "--name", "gh2", "--route", target,
		"--events", "push,issues", "--priority", "hold")
	var added struct {
		SourceID   string `json:"source_id"`
		IngressURL string `json:"ingress_url"`
		Secret     string `json:"secret"`
	}
	if err := json.Unmarshal([]byte(out), &added); err != nil {
		t.Fatalf("source add --json output %q: %v", out, err)
	}
	if added.IngressURL != env.URL+"/v1/ingress/"+added.SourceID || !secretRe.MatchString(added.Secret) {
		t.Fatalf("source add --json = %+v", added)
	}

	// Listings carry neither secret, in either form.
	human := op.mustRun("source", "list")
	raw := op.mustRun("--json", "source", "list")
	for _, s := range []string{human, raw} {
		if strings.Contains(s, secret) || strings.Contains(s, added.Secret) || strings.Contains(s, "secret") {
			t.Fatalf("source list leaks a secret: %q", s)
		}
	}
	lines := nonEmptyLines(human)
	if len(lines) != 2 || !strings.Contains(human, id) || !strings.Contains(human, added.SourceID) {
		t.Fatalf("source list = %q, want a line per source", human)
	}
	for _, want := range []string{"target=" + target, "events=push,issues", "priority=hold"} {
		if !strings.Contains(human, want) {
			t.Fatalf("source list = %q, want %s", human, want)
		}
	}
	// A name is the operator's text, and a row stays one line whatever it holds.
	if _, err := env.Hub.DB.Exec(`UPDATE sources SET name = 'two' || char(10) || 'lines' WHERE src_id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if rows := nonEmptyLines(op.mustRun("source", "list")); len(rows) != 2 {
		t.Fatalf("a newline in a name split the row: %q", rows)
	}
	var list []struct {
		SourceID   string   `json:"source_id"`
		Target     string   `json:"target"`
		EventAllow []string `json:"event_allow"`
		Priority   string   `json:"priority"`
	}
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatalf("source list --json output %q: %v", raw, err)
	}
	if len(list) != 2 {
		t.Fatalf("source list --json = %+v, want 2", list)
	}
	for _, s := range list {
		wantPriority, wantEvents := "wake_idle", 0
		if s.SourceID == added.SourceID {
			wantPriority, wantEvents = "hold", 2
		}
		if s.Target != target || s.Priority != wantPriority || len(s.EventAllow) != wantEvents {
			t.Fatalf("source %+v, want target %s priority %s and %d events", s, target, wantPriority, wantEvents)
		}
	}

	// --events with nothing in it must not read as "allow everything".
	if code, _, errs := op.run("source", "add", "--kind", "github", "--name", "gh3", "--route", target, "--events", " , "); code != 1 || errs == "" {
		t.Fatalf("source add --events blank = %d %q, want 1", code, errs)
	}
	if code, _, _ := op.run("source", "add", "--kind", "github", "--name", "gh4"); code != 1 {
		t.Fatalf("source add without --route = %d, want 1", code)
	}
	if code, _, errs := op.run("source", "add", "--kind", "github", "--name", "gh5", "--route", "peer_NOPE"); code != 2 || errs == "" {
		t.Fatalf("source add to an unknown target = %d %q, want 2", code, errs)
	}
}

// routeCount is how many routes the hub holds.
func routeCount(t *testing.T, env *testhub.Env) int {
	t.Helper()
	resp, out := env.Do(env.OperatorSecret, "GET", "/v1/admin/routes", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list routes: %d %s", resp.StatusCode, out)
	}
	var body struct {
		Routes []json.RawMessage `json:"routes"`
	}
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	return len(body.Routes)
}

func TestRouteAddGrokbotKeyFile(t *testing.T) {
	env := testhub.Start(t)
	op := profileUser(t, env.URL, env.OperatorSecret)
	owner, _ := env.NewPeer("grokbot", "g")
	dir := t.TempDir()
	const key = "crsr_0123456789abcdef"
	keyFile := func(name, body string, mode os.FileMode) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil { // beat the umask
			t.Fatal(err)
		}
		return p
	}
	grokbot := []string{"route", "add", "--owner", owner, "--kind", "grokbot", "--url", routineURL}

	// A 0600 file, with the trailing newline an editor leaves: quiet success.
	code, out, errs := op.run(append(grokbot, "--key-file", keyFile("ok", key+"\n", 0o600))...)
	if code != 0 || errs != "" {
		t.Fatalf("route add = %d, stdout %q, stderr %q; want 0 and a quiet stderr", code, out, errs)
	}
	id := field(t, out, "route_id")
	if !strings.HasPrefix(id, "route_") {
		t.Fatalf("route_id = %q, want route_…", id)
	}
	if stored, err := env.Hub.GrokbotKey(context.Background(), id); err != nil || stored != key {
		t.Fatalf("stored key = %q, %v; want the file's, without its newline", stored, err)
	}
	if strings.Contains(out, key) || strings.Contains(out, "whsec") {
		t.Fatalf("route add output %q carries a key", out)
	}
	if n := routeCount(t, env); n != 1 {
		t.Fatalf("routes = %d, want 1", n)
	}
	if _, listed := env.Do(env.OperatorSecret, "GET", "/v1/admin/routes", nil); bytes.Contains(listed, []byte(key)) {
		t.Fatalf("route listing leaks the key: %s", listed)
	}

	// No argv form exists, and the refusal does not echo the value.
	for _, args := range [][]string{{"--key", key}, {"--key=" + key}} {
		code, out, errs := op.run(append(grokbot, args...)...)
		if code != 1 || !strings.Contains(errs, "unknown flag --key") {
			t.Fatalf("route add %v = %d %q, want 1 unknown flag", args, code, errs)
		}
		if strings.Contains(out+errs, key) {
			t.Fatalf("usage error echoes the key: %q %q", out, errs)
		}
	}
	if n := routeCount(t, env); n != 1 {
		t.Fatalf("routes after refused argv keys = %d, want 1", n)
	}

	// A readable file warns on stderr and is still accepted.
	code, out, errs = op.run(append(grokbot, "--key-file", keyFile("open", key, 0o644))...)
	if code != 0 {
		t.Fatalf("route add with a 0644 file = %d %q %q, want 0", code, out, errs)
	}
	if !strings.Contains(errs, "warning") || !strings.Contains(errs, "0644") {
		t.Fatalf("stderr = %q, want a warning naming the mode", errs)
	}
	if strings.Contains(out+errs, key) {
		t.Fatalf("output carries the key: %q %q", out, errs)
	}
	if n := routeCount(t, env); n != 2 {
		t.Fatalf("routes = %d, want 2", n)
	}

	// --json prints the id alone.
	out = op.mustRun(append([]string{"--json"}, append(grokbot, "--key-file", keyFile("j", key, 0o600))...)...)
	var added map[string]any
	if err := json.Unmarshal([]byte(out), &added); err != nil {
		t.Fatalf("route add --json output %q: %v", out, err)
	}
	if len(added) != 1 || added["route_id"] == nil {
		t.Fatalf("route add --json = %v, want route_id alone", added)
	}

	// Bad invocations stop locally: nothing leaves, and file contents stay out
	// of the error.
	const notKey = "-----BEGIN PRIVATE KEY-----"
	bad := map[string][]string{
		"no key file":       grokbot,
		"missing file":      append(grokbot[:len(grokbot):len(grokbot)], "--key-file", filepath.Join(dir, "nope")),
		"not a crsr_ key":   append(grokbot[:len(grokbot):len(grokbot)], "--key-file", keyFile("pem", notKey+"\n", 0o600)),
		"empty file":        append(grokbot[:len(grokbot):len(grokbot)], "--key-file", keyFile("empty", "\n", 0o600)),
		"oversize file":     append(grokbot[:len(grokbot):len(grokbot)], "--key-file", keyFile("big", key+strings.Repeat("a", 8192), 0o600)),
		"key file for http": {"route", "add", "--owner", owner, "--url", "http://127.0.0.1:9/hook", "--key-file", keyFile("h", key, 0o600)},
	}
	for name, args := range bad {
		code, _, errs := op.run(args...)
		if code != 1 || errs == "" {
			t.Fatalf("%s: exit %d %q, want 1", name, code, errs)
		}
		if strings.Contains(errs, notKey) || strings.Contains(errs, key) {
			t.Fatalf("%s: error echoes file contents: %q", name, errs)
		}
	}
	if n := routeCount(t, env); n != 3 {
		t.Fatalf("routes after refused invocations = %d, want 3", n)
	}

	// The hub's own check still answers.
	if code, _, errs := op.run("route", "add", "--owner", owner, "--kind", "grokbot", "--url", "https://example.com/x",
		"--key-file", keyFile("k2", key, 0o600)); code != 2 || !strings.HasPrefix(errs, "invalid_url: ") {
		t.Fatalf("route add with a foreign URL = %d %q, want 2 invalid_url", code, errs)
	}

	// The http form is unchanged: it still issues a whsec_.
	out = op.mustRun("route", "add", "--owner", owner, "--url", "http://127.0.0.1:9/hook")
	if !strings.HasPrefix(field(t, out, "whsec"), "whsec_") {
		t.Fatalf("http route add = %q, want a whsec_", out)
	}
}

// subscribe opens an MCP Events subscription for bearer against callback.
func subscribe(t *testing.T, env *testhub.Env, bearer, callback string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "events/subscribe", "params": map[string]any{
		"name":      events.Name,
		"arguments": map[string]any{},
		"delivery":  map[string]any{"mode": "webhook", "url": callback, "secret": webhook.NewSecret()},
	}})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, env.URL+"/mcp", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("MCP-Protocol-Version", mcpserver.ProtoCurrent)
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var r struct {
		Result struct {
			ID string `json:"id"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode events/subscribe: %v", err)
	}
	if r.Result.ID == "" {
		t.Fatalf("events/subscribe failed: %d %s", resp.StatusCode, r.Error)
	}
	return r.Result.ID
}

func TestSubscriptionsList(t *testing.T) {
	env := testhub.Start(t)
	op := profileUser(t, env.URL, env.OperatorSecret)

	if out := op.mustRun("subscriptions", "list"); out != "" {
		t.Fatalf("empty subscriptions list = %q", out)
	}
	if out := op.mustRun("--json", "subscriptions", "list"); strings.TrimSpace(out) != "[]" {
		t.Fatalf("empty subscriptions list --json = %q, want []", out)
	}

	dot, dotSecret := env.NewPeer("session", "d")
	cb := testhub.NewCallback(t)
	sub := subscribe(t, env, dotSecret, cb.URL)
	u, err := url.Parse(cb.URL)
	if err != nil {
		t.Fatal(err)
	}

	human := op.mustRun("subscriptions", "list")
	raw := op.mustRun("--json", "subscriptions", "list")
	for _, s := range []string{human, raw} {
		for _, leak := range []string{u.Path, "path-with-token", u.Host, "http://", "whsec_", "secret"} {
			if strings.Contains(s, leak) {
				t.Fatalf("subscriptions list shows %q: %q", leak, s)
			}
		}
	}
	lines := nonEmptyLines(human)
	if len(lines) != 1 {
		t.Fatalf("subscriptions list = %q, want one row", human)
	}
	var rows []struct {
		SubID        string  `json:"sub_id"`
		PeerID       string  `json:"peer_id"`
		URL          string  `json:"url"`
		Cursor       string  `json:"cursor"`
		ExpiresAt    *string `json:"expires_at"`
		PausedReason *string `json:"paused_reason"`
		LastStatus   *int    `json:"last_status"`
	}
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("subscriptions list --json output %q: %v", raw, err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want one", rows)
	}
	r := rows[0]
	if r.SubID != sub || r.PeerID != dot || r.URL != u.Hostname() || r.ExpiresAt == nil {
		t.Fatalf("row = %+v", r)
	}
	for _, want := range []string{sub, dot, " " + u.Hostname() + " ", "cursor=" + r.Cursor, "expires=" + *r.ExpiresAt, "paused=-", "last_status=-"} {
		if !strings.Contains(lines[0], want) {
			t.Fatalf("row %q lacks %q", lines[0], want)
		}
	}

	// A paused subscription shows why and its last status; resume clears it.
	if _, err := env.Hub.DB.Exec(`UPDATE event_subscriptions SET paused_reason = 'delivery_failed', last_status = 503`); err != nil {
		t.Fatal(err)
	}
	row := op.mustRun("subscriptions", "list")
	if !strings.Contains(row, "paused=delivery_failed") || !strings.Contains(row, "last_status=503") {
		t.Fatalf("paused row = %q", row)
	}
	if out := op.mustRun("subscriptions", "resume", sub); !strings.Contains(out, sub) {
		t.Fatalf("subscriptions resume = %q", out)
	}
	if row := op.mustRun("subscriptions", "list"); !strings.Contains(row, "paused=-") {
		t.Fatalf("resumed row = %q", row)
	}
	if out := op.mustRun("--json", "subscriptions", "resume", sub); strings.TrimSpace(out) != `{"ok":true}` {
		t.Fatalf("subscriptions resume --json = %q", out)
	}

	if code, _, errs := op.run("subscriptions", "resume", "sub_NOPE"); code != 2 || !strings.HasPrefix(errs, "not_found: ") {
		t.Fatalf("resume unknown = %d %q, want 2 not_found", code, errs)
	}
	if code, _, _ := op.run("subscriptions"); code != 1 {
		t.Fatalf("subscriptions alone = %d, want 1", code)
	}
	if code, _, _ := op.run("subscriptions", "resume"); code != 1 {
		t.Fatalf("subscriptions resume with no id = %d, want 1", code)
	}

	_, sec := env.NewPeer("session", "s")
	if code, _, errs := profileUser(t, env.URL, sec).run("subscriptions", "list"); code != 2 || !strings.HasPrefix(errs, "forbidden: ") {
		t.Fatalf("subscriptions list as a peer = %d %q, want 2 forbidden", code, errs)
	}
}
