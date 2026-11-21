package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
)

const idSuffix = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
const idPeer = "peer_" + idSuffix
const idThread = "thr_" + idSuffix
const idMessage = "msg_" + idSuffix
const idDelivery = "dly_" + idSuffix

type idCountingReader struct{ reads int }

func (r *idCountingReader) Read([]byte) (int, error) {
	r.reads++
	return 0, errors.New("stdin must not be read")
}

type idGuardTransport struct{ calls int }

func (g *idGuardTransport) RoundTrip(*http.Request) (*http.Response, error) {
	g.calls++
	return nil, errors.New("synthetic unavailable transport")
}

func idEnvironment(t *testing.T, hub string) map[string]string {
	t.Helper()
	env := hookTestEnv(t)
	env["VECNET_LOCAL_MAINTENANCE"] = "0"
	if err := cliFixtureMkdirAll(filepath.Dir(env["VECNET_CONFIG"]), 0700); err != nil {
		t.Fatal(err)
	}
	if err := cliFixtureWriteFile(env["VECNET_CONFIG"], []byte(fmt.Sprintf("[profiles.p]\nhub = %q\nsecret = \"synthetic-secret\"\n", hub)), 0600); err != nil {
		t.Fatal(err)
	}
	return env
}

func idRun(env map[string]string, input io.Reader, args ...string) (int, string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var out, stderr bytes.Buffer
	code := RunContext(ctx, args, input, &out, &stderr, func(k string) string { return env[k] })
	return code, out.String(), stderr.String()
}

// Removing ID checks, moving them behind priority/IO, or entering localStartup
// before them must fail this test. Every HTTP request counts, including probes.
func TestCLIWrongIDBeforeIO(t *testing.T) {
	g := &idGuardTransport{}
	old := http.DefaultTransport
	http.DefaultTransport = g
	t.Cleanup(func() { http.DefaultTransport = old })
	bad := []string{"", "Max", "future_" + idSuffix, "e1.42", "peer_" + strings.ToLower(idSuffix), "peer_short", "peer_8" + idSuffix[1:], "peer_" + idSuffix[:25] + "I", "peer_" + strings.Repeat("Z", 2000), "SECRET\x1b[31m\n\u202e"}
	for _, prefix := range []string{"thr", "msg", "dly", "job", "apr", "src", "prn", "ntc", "blo"} {
		bad = append(bad, prefix+"_"+idSuffix)
	}
	for i, recipient := range bad {
		for _, flags := range [][]string{nil, {"--hold"}, {"--hold", "--wake"}} {
			t.Run(fmt.Sprintf("recipient%d/priority%d", i, len(flags)), func(t *testing.T) {
				env := idEnvironment(t, "http://127.0.0.1:1")
				args := append([]string{"send", idPeer, recipient, "-", "--attach", filepath.Join(t.TempDir(), "missing")}, flags...)
				var out, stderr bytes.Buffer
				input := &idCountingReader{}
				startup := 0
				before := g.calls
				code := RunContext(context.Background(), args, input, &out, &stderr, func(k string) string {
					if k == "VECNET_LOCAL_MAINTENANCE" {
						startup++
					}
					return env[k]
				})
				text := stderr.String()
				if code != exitUsage || out.Len() != 0 || input.reads != 0 || g.calls != before || startup != 0 || !strings.Contains(text, "recipient") || !strings.Contains(text, "peer_, team_ or grp_") {
					t.Fatalf("exit=%d stdout=%q stderr=%q reads=%d requests=%d startup=%d", code, out.String(), text, input.reads, g.calls-before, startup)
				}
				if len(text) > 600 || strings.ContainsAny(strings.TrimSuffix(text, "\n"), "\n\r\x1b\u202e") || strings.Contains(text, "SECRET") {
					t.Fatalf("unbounded/unsafe local error: %q", text)
				}
				if strings.HasPrefix(recipient, "thr_") && (!strings.Contains(text, "thread ID") || !strings.Contains(text, "send --thread") || !strings.Contains(text, "--hold")) {
					t.Fatalf("missing existing-thread guidance: %q", text)
				}
				if recipient == "Max" && (!strings.Contains(text, "display names are not addresses") || !strings.Contains(text, "vecnet peers")) {
					t.Fatalf("missing address discovery guidance: %q", text)
				}
			})
		}
	}
}

func TestCLIMessageAndThreadIDArguments(t *testing.T) {
	g := &idGuardTransport{}
	old := http.DefaultTransport
	http.DefaultTransport = g
	t.Cleanup(func() { http.DefaultTransport = old })
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"send", idPeer, "-", "--thread", idPeer}, "thr_"},
		{[]string{"send", "-", "--thread", idPeer}, "thr_"},
		{[]string{"send", idPeer, "-", "--thread="}, "thr_"},
		{[]string{"send", "-", "--thread="}, "thr_"},
		{[]string{"send", idPeer, "-", "--reply-to", idThread}, "msg_"},
		{[]string{"send", idPeer, "-", "--reply-to="}, "msg_"},
		{[]string{"reply", idThread, "-"}, "msg_"},
		{[]string{"reply", idMessage, "-", "--thread", idPeer}, "thr_"},
		{[]string{"reply", idMessage, "-", "--thread="}, "thr_"},
		{[]string{"react", idThread, "check"}, "msg_"},
		{[]string{"thread", idPeer}, "thr_"},
		{[]string{"thread", "archive", idPeer}, "thr_"},
		{[]string{"thread", "unarchive", idPeer}, "thr_"},
		{[]string{"thread", "budget", idPeer, "3"}, "thr_"},
		{[]string{"thread", idThread, "--after", idThread}, "msg_"},
		{[]string{"thread", idThread, "--after="}, "msg_"},
		{[]string{"groups", "create", idThread}, "peer_"},
		{[]string{"groups", "add", "grp_" + idSuffix, idPeer, idThread}, "peer_"},
		{[]string{"groups", "add", idPeer, idPeer}, "team_ or grp_"},
		{[]string{"groups", "unarchive", idThread}, "team_ or grp_"},
		{[]string{"receipt", idMessage, "read"}, "dly_"},
		{[]string{"dead-letters", "retry", idMessage}, "dly_"},
		{[]string{"dead-letters", "discard", idMessage}, "dly_"},
		{[]string{"ack", idMessage}, "eN.N"},
		{[]string{"ack", "e1.2\n"}, "eN.N"},
		{[]string{"ack", "e.2"}, "eN.N"},
		{[]string{"ack", "e1.-2"}, "eN.N"},
		{[]string{"ack", "e1.2.3"}, "eN.N"},
		{[]string{"send", idPeer, "-", "--as", idThread}, "peer_"},
		{[]string{"send", idPeer, "-", "--as="}, "peer_"},
		{[]string{"reply", idMessage, "-", "--as="}, "peer_"},
		{[]string{"react", idMessage, "check", "--as", idThread}, "peer_"},
		{[]string{"groups", "create", idPeer, "--as="}, "peer_"},
		{[]string{"inbox", "--as", idThread}, "peer_"},
		{[]string{"watch", "--as="}, "peer_"},
		{[]string{"threads", "--as="}, "peer_"},
		{[]string{"ack", "e1.2", "--as="}, "peer_"},
	}
	for i, tc := range tests {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			env := idEnvironment(t, "http://127.0.0.1:1")
			input := &idCountingReader{}
			before := g.calls
			code, out, stderr := idRun(env, input, tc.args...)
			if code != exitUsage || out != "" || !strings.Contains(stderr, "expected "+tc.want) || input.reads != 0 || g.calls != before {
				t.Fatalf("args=%q exit=%d stdout=%q stderr=%q reads=%d requests=%d", tc.args, code, out, stderr, input.reads, g.calls-before)
			}
		})
	}
}

func TestCLIInvalidIDBeforeMaintenance(t *testing.T) {
	a, root, home := localRegistryFixture(t)
	var out, stderr bytes.Buffer
	input := &idCountingReader{}
	startup := 0
	code := RunContext(context.Background(), []string{"send", idThread, "-"}, input, &out, &stderr, func(k string) string {
		if k == "HOME" {
			return home
		}
		if k == "VECNET_LOCAL_MAINTENANCE" {
			startup++
		}
		return ""
	})
	if code != exitUsage || startup != 0 || input.reads != 0 || !strings.Contains(stderr.String(), "thread ID") {
		t.Fatalf("exit=%d startup=%d reads=%d stderr=%q", code, startup, input.reads, stderr.String())
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("invalid ID created maintenance inventory/home: %v", err)
	}
	// Also exercise the same entry seam with real pending repair state and a
	// synthetic process port. No repair process or host scheduler may run.
	if err := a.saveLocalEnrollment(localEnrollment{IntentID: "pending", Config: filepath.Join(home, "config.toml"), Profile: "p", Directory: root, ThreadID: bindT1, CodexHome: home, State: "pending", UpdatedAt: localRegistryNow()}); err != nil {
		t.Fatal(err)
	}
	started := 0
	a.localRepairStart = func(*exec.Cmd) error { started++; return nil }
	a.stdin = input
	err := a.runCommand([]string{"send", idThread, "-"})
	projects, inventoryErr := a.localProjects()
	if err == nil || !strings.Contains(err.Error(), "thread ID") || started != 0 || input.reads != 0 || inventoryErr != nil || len(projects) != 0 {
		t.Fatalf("preflight entered repair/startup: err=%v started=%d reads=%d inventory=%v projects=%v", err, started, input.reads, inventoryErr, projects)
	}
}

// Literal expected fragments are independent of the formatter's allowlist.
func TestInvalidBodyDetailsPresentation(t *testing.T) {
	tests := []struct {
		name string
		path any
		want string
	}{
		{"recipient", "/recipients/0", "field /recipients/0; expected peer_, team_ or grp_ ID"},
		{"large-index", "/recipients/1234567890", "field /recipients/1234567890; expected peer_, team_ or grp_ ID"},
		{"thread", "/thread_id", "field /thread_id; expected thr_ ID"},
		{"reply", "/in_reply_to", "field /in_reply_to; expected msg_ ID"},
		{"as", "/as", "field /as; expected peer_ ID"},
		{"message", "/message_id", "field /message_id; expected msg_ ID"},
		{"delivery", "/delivery_id", "field /delivery_id; expected dly_ ID"},
		{"cursor", "/cursor", "field /cursor; expected eN.N cursor"},
		{"priority", "/priority", "field /priority; expected wake, wake_idle or hold"},
		{"text", "/content/text", "field /content/text; expected text"},
		{"attachment-hash", "/attachments/0/sha256", "field /attachments/0/sha256; expected 64 lowercase hexadecimal characters"},
		{"attachment-size", "/attachments/15/size", "field /attachments/15/size; expected integer"},
		{"attachment-object", "/attachments/0", "field /attachments/0; expected attachment reference"},
		{"attachment-type", "/attachments/0/media_type", "field /attachments/0/media_type; expected media type string"},
		{"attachment-name", "/attachments/0/name", "field /attachments/0/name; expected name string"},
		{"attachment-origin", "/attachments/0/origin_url", "field /attachments/0/origin_url; expected URI"},
		{"attachment-url", "/attachments/0/blob_url", "field /attachments/0/blob_url; expected URI"},
		{"attachment-id", "/attachments/0/blob_id", "field /attachments/0/blob_id; expected blo_ ID"},
		{"attachment-retention", "/attachments/0/retention_class", "field /attachments/0/retention_class; expected 1h, 6h"},
		{"attachment-expiry", "/attachments/0/expires_at", "field /attachments/0/expires_at; expected UTC timestamp"},
		{"masked-root", "/<unknown>", "field /<unknown>; request must match"},
		{"masked-content", "/content/<unknown>", "field /content/<unknown>; request must match"},
		{"masked-attachment", "/attachments/0/<unknown>", "field /attachments/0/<unknown>; request must match"},
		{"root", "", "field /; request must match"},
		{"absent", nil, "field unavailable; request must match"},
		{"non-string", 17, "field unavailable; request must match"},
		{"slash", "/", "field unavailable; request must match"},
		{"unknown", "/SECRET", "field unavailable; request must match"},
		{"secret-map-key", "/metadata/SECRET", "field unavailable; request must match"},
		{"signed-url", "/https://fixture.invalid?sig=SECRET", "field unavailable; request must match"},
		{"escape", "/content/te~1xt", "field unavailable; request must match"},
		{"tilde-escape", "/content/te~0xt", "field unavailable; request must match"},
		{"bad-escape", "/content/text~2", "field unavailable; request must match"},
		{"trailing-escape", "/content/text~", "field unavailable; request must match"},
		{"leading-zero", "/recipients/00", "field unavailable; request must match"},
		{"negative-index", "/recipients/-1", "field unavailable; request must match"},
		{"long-index", "/recipients/12345678901", "field unavailable; request must match"},
		{"untrusted-suffix", "/recipients/0/SECRET", "field unavailable; request must match"},
		{"masked-suffix", "/content/<unknown>/SECRET", "field unavailable; request must match"},
		{"too-long", "/content/text" + strings.Repeat("/SECRET", 50), "field unavailable; request must match"},
		{"too-deep", strings.Repeat("/content", 17), "field unavailable; request must match"},
		{"controls", "/content/text\x1b[31m\nSECRET", "field unavailable; request must match"},
		{"bidi", "/content/\u202eSECRET", "field unavailable; request must match"},
	}
	var body any
	env, _ := hookFixtureRaw(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(body)
	})
	env["VECNET_LOCAL_MAINTENANCE"] = "0"
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			details := map[string]any{"expected": "SECRET", "value": "SECRET", "SECRET": "https://fixture.invalid?sig=SECRET", "nested": map[string]any{"path": "SECRET"}}
			if tc.path != nil {
				details["path"] = tc.path
			}
			body = map[string]any{"error": map[string]any{"code": "invalid_body", "message": "value does not satisfy the schema or hub constraint", "details": details}}
			he := &hubError{Code: "invalid_body", Message: "value does not satisfy the schema or hub constraint", Details: details}
			formatted := he.Error()
			code, out, stderr := idRun(env, strings.NewReader(""), "--json", "send", idPeer, "text", "--hold")
			if code != exitHub || out != "" || !strings.Contains(stderr, tc.want) || !strings.Contains(formatted, tc.want) {
				t.Fatalf("exit=%d stdout=%q stderr=%q Error()=%q want=%q", code, out, stderr, formatted, tc.want)
			}
			for _, text := range []string{formatted, strings.TrimSuffix(stderr, "\n")} {
				if len(text) > 650 || strings.ContainsAny(text, "\n\r\x1b\u202e") || strings.Contains(text, "SECRET") || strings.Contains(text, "https://") {
					t.Fatalf("unsafe diagnostic: %q", text)
				}
				if strings.Contains(tc.want, "request must match") && !strings.Contains(text, "--help") {
					t.Fatalf("fallback lacks schema help: %q", text)
				}
			}
		})
	}
	var stderr bytes.Buffer
	a := &app{stderr: &stderr}
	he := &hubError{Code: "unknown_recipient", Message: "recipient is not enrolled", Details: map[string]any{"path": "/SECRET"}, Hint: "safe hint"}
	if code := a.exit(he); code != exitHub || stderr.String() != "unknown_recipient: recipient is not enrolled\nsafe hint\n" || he.Error() != "unknown_recipient: recipient is not enrolled" {
		t.Fatalf("non-schema error/hint changed: code=%d stderr=%q Error()=%q", code, stderr.String(), he.Error())
	}
}

func idFixtureHub(t *testing.T, handler http.HandlerFunc) map[string]string {
	t.Helper()
	key := testIdentityKey(t, "1")
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/hub/identity" {
			sig, _ := key.SignChallenge(srv.URL, r.URL.Query().Get("challenge"))
			_ = json.NewEncoder(w).Encode(envelope.HubIdentity{Pubkey: key.Pubkey(), PublicURL: srv.URL, ChallengeSig: sig})
			return
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return idEnvironment(t, srv.URL)
}

func TestCLISendThreadOnly(t *testing.T) {
	sends := 0
	env := idFixtureHub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected command request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
			return
		}
		sends++
		raw, _ := io.ReadAll(r.Body)
		if err := envelope.Validate("sendRequest", raw); err != nil {
			t.Errorf("invalid thread-only body: %v", err)
		}
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		if _, present := body["recipients"]; present || body["thread_id"] != idThread || body["priority"] != "hold" {
			t.Errorf("thread-only body: %s", raw)
		}
		fmt.Fprintf(w, `{"message_id":%q,"thread_id":%q,"deliveries":[]}`, idMessage, idThread)
	})
	code, out, stderr := idRun(env, strings.NewReader(""), "--json", "send", "--thread", idThread, "--hold", "thread-only text")
	if code != exitOK || stderr != "" || sends != 1 || !strings.Contains(out, idMessage) {
		t.Fatalf("thread-only send: exit=%d stdout=%q stderr=%q sends=%d", code, out, stderr, sends)
	}
	code, _, stderr = idRun(env, strings.NewReader(""), "send", "text", "--hold")
	if code != exitUsage || !strings.Contains(stderr, "send needs at least one recipient or --thread") || sends != 1 {
		t.Fatalf("missing recipient/thread changed: exit=%d stderr=%q sends=%d", code, stderr, sends)
	}
	_, help, _ := idRun(env, strings.NewReader(""), "send", "--help")
	if !strings.Contains(help, "send --thread") || !strings.Contains(help, "no default") {
		t.Fatalf("help omits recipient-free send or required priority: %q", help)
	}
}

func TestCLIValidIDPathsPreserved(t *testing.T) {
	var sent envelope.SendRequest
	var historyAfter string
	var acked string
	env := idFixtureHub(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/messages":
			raw, _ := io.ReadAll(r.Body)
			if err := envelope.Validate("sendRequest", raw); err != nil {
				t.Errorf("canonical send invalid: %v", err)
			}
			sent = envelope.SendRequest{}
			_ = json.Unmarshal(raw, &sent)
			fmt.Fprintf(w, `{"message_id":%q,"thread_id":%q,"deliveries":[]}`, idMessage, idThread)
		case "/v1/threads/" + idThread + "/messages":
			historyAfter = r.URL.Query().Get("after")
			fmt.Fprint(w, `{"messages":[]}`)
		case "/v1/ack":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			acked = body["cursor"]
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected command request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	})
	for _, tc := range []struct{ recipient, flag, priority string }{
		{idPeer, "--wake", "wake"}, {"team_" + idSuffix, "--wake-idle", "wake_idle"}, {"grp_" + idSuffix, "--hold", "hold"},
	} {
		code, _, stderr := idRun(env, strings.NewReader(""), "send", tc.recipient, tc.flag, idMessage)
		if code != exitOK || stderr != "" || len(sent.Recipients) != 1 || sent.Recipients[0] != tc.recipient || sent.Priority != tc.priority || sent.Content.Text != idMessage {
			t.Fatalf("valid recipient/priority/ID-like text: exit=%d stderr=%q sent=%+v", code, stderr, sent)
		}
	}
	code, _, stderr := idRun(env, strings.NewReader(""), "send", idPeer, "--thread", "bad", "--hold", "--thread="+idThread, "--as=bad", "--as", idPeer, "--reply-to=bad", "--reply-to", idMessage, "--idempotency-key", "arbitrary-key", "--", "--as=literal")
	if code != exitOK || stderr != "" || sent.ThreadID == nil || *sent.ThreadID != idThread || sent.InReplyTo == nil || *sent.InReplyTo != idMessage || sent.As != idPeer || sent.Content.Text != "--as=literal" || sent.IdempotencyKey != "arbitrary-key" {
		t.Fatalf("flag grammar/last value: exit=%d stderr=%q sent=%+v", code, stderr, sent)
	}
	for _, args := range [][]string{
		{"reply", idMessage, "reply", "--hold", "--thread", idThread},
		{"reply", idMessage, "cached reply", "--hold"},
	} {
		code, _, stderr = idRun(env, strings.NewReader(""), args...)
		if code != exitOK || stderr != "" || sent.ThreadID == nil || *sent.ThreadID != idThread || sent.InReplyTo == nil || *sent.InReplyTo != idMessage || len(sent.Recipients) != 0 {
			t.Fatalf("explicit/cached reply: args=%q exit=%d stderr=%q sent=%+v", args, code, stderr, sent)
		}
	}
	code, _, stderr = idRun(env, strings.NewReader(""), "thread", idThread, "--after=bad", "--after", idMessage, "--limit", "2", "--json")
	if code != exitOK || stderr != "" || historyAfter != idMessage {
		t.Fatalf("history pagination: exit=%d stderr=%q after=%q", code, stderr, historyAfter)
	}
	// Syntax only: leading zeros and huge epoch/sequence are passed byte-for-byte.
	const cursor = "e00000000000000000000000000000000001.99999999999999999999999999999999"
	code, _, stderr = idRun(env, strings.NewReader(""), "ack", cursor)
	if code != exitOK || stderr != "" || acked != cursor {
		t.Fatalf("cursor was interpreted/normalized: exit=%d stderr=%q cursor=%q", code, stderr, acked)
	}
}

func TestCLIReplyCacheShape(t *testing.T) {
	env := idEnvironment(t, "http://127.0.0.1:1")
	code, _, stderr := idRun(env, strings.NewReader(""), "reply", idMessage, "text", "--hold")
	if code != exitUsage || !strings.Contains(stderr, "unknown message id; pass --thread") {
		t.Fatalf("canonical uncached message: exit=%d stderr=%q", code, stderr)
	}
	dir, err := cacheDir(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if err := (cache{dir: dir}).rememberMessages(map[string]string{idMessage: "SECRET\n\x1b[31m"}); err != nil {
		t.Fatal(err)
	}
	input := &idCountingReader{}
	code, _, stderr = idRun(env, input, "reply", idMessage, "-", "--hold")
	if code != exitUsage || !strings.Contains(stderr, "expected thr_ ID") || strings.Contains(stderr, "SECRET") || input.reads != 0 {
		t.Fatalf("untrusted cached thread reached IO: exit=%d stderr=%q reads=%d", code, stderr, input.reads)
	}
}

func TestCLIArgumentSyntaxPreserved(t *testing.T) {
	env := idEnvironment(t, "http://127.0.0.1:1")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"send", idThread, "text", "--unknown"}, "unknown flag --unknown"},
		{[]string{"reply", idThread}, "usage: vecnet reply"},
		{[]string{"thread", "archive", idPeer, "extra"}, "usage: vecnet thread archive"},
		{[]string{"thread", "budget", idPeer, "not-a-number"}, "usage: vecnet thread budget"},
		{[]string{"groups", "add", idPeer}, "usage: vecnet groups"},
		{[]string{"receipt", idMessage}, "usage: vecnet receipt"},
		{[]string{"ack", "bad", "extra"}, "usage: vecnet ack"},
		{[]string{"send", idPeer, "text", "--thread"}, "--thread needs a value"},
	} {
		code, out, stderr := idRun(env, strings.NewReader(""), tc.args...)
		if code != exitUsage || out != "" || !strings.Contains(stderr, tc.want) || strings.Contains(stderr, "expected ") {
			t.Fatalf("syntax masked: args=%q exit=%d out=%q stderr=%q", tc.args, code, out, stderr)
		}
	}
}

func TestInvalidBodyMessagePrivacy(t *testing.T) {
	// invalid_body has fixed public prose on our hub. A received replacement
	// must not turn validator values or arbitrary server prose into terminal text.
	he := &hubError{Code: "invalid_body", Message: "SECRET\x1b[31m\nhttps://fixture.invalid?sig=SECRET", Details: map[string]any{"path": "/thread_id"}}
	var stderr bytes.Buffer
	a := &app{stderr: &stderr}
	if a.exit(he) != exitHub {
		t.Fatal("schema error changed exit classification")
	}
	for _, text := range []string{he.Error(), strings.TrimSuffix(stderr.String(), "\n")} {
		if !strings.Contains(text, "field /thread_id; expected thr_ ID") || strings.Contains(text, "SECRET") || strings.ContainsAny(text, "\x1b\n\r") {
			t.Fatalf("untrusted schema prose: %q", text)
		}
	}
}

func TestCLIValidTypedIDsReachHub(t *testing.T) {
	var paths []string
	env := idFixtureHub(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"code":"forbidden","message":"synthetic authorization refusal","details":{}}}`)
	})
	for _, tc := range []struct {
		args []string
		path string
	}{
		{[]string{"react", idMessage, "check", "--as", idPeer}, "POST /v1/reactions"},
		{[]string{"thread", idThread, "--after", idMessage}, "GET /v1/threads/" + idThread + "/messages"},
		{[]string{"thread", "archive", idThread}, "POST /v1/admin/threads/" + idThread + "/archive"},
		{[]string{"thread", "unarchive", idThread}, "POST /v1/admin/threads/" + idThread + "/unarchive"},
		{[]string{"thread", "budget", idThread, "3"}, "PATCH /v1/admin/threads/" + idThread},
		{[]string{"groups", "create", idPeer, "--as", idPeer}, "POST /v1/groups"},
		{[]string{"groups", "add", "team_" + idSuffix, idPeer}, "POST /v1/groups/team_" + idSuffix + "/members"},
		{[]string{"groups", "add", "grp_" + idSuffix, idPeer}, "POST /v1/groups/grp_" + idSuffix + "/members"},
		{[]string{"groups", "unarchive", "grp_" + idSuffix}, "POST /v1/admin/groups/grp_" + idSuffix + "/unarchive"},
		{[]string{"receipt", idDelivery, "read"}, "POST /v1/receipts"},
		{[]string{"dead-letters", "retry", idDelivery}, "POST /v1/admin/dead-letters/" + idDelivery + "/retry"},
		{[]string{"dead-letters", "discard", idDelivery}, "POST /v1/admin/dead-letters/" + idDelivery + "/discard"},
		{[]string{"threads", "--as", idPeer}, "GET /v1/threads"},
		{[]string{"inbox", "--as", idPeer}, "GET /v1/events"},
	} {
		before := len(paths)
		code, out, stderr := idRun(env, strings.NewReader(""), tc.args...)
		if code != exitHub || out != "" || !strings.HasPrefix(stderr, "forbidden:") || len(paths) != before+1 || paths[before] != tc.path {
			t.Fatalf("canonical ID refused locally or wrong request: args=%q exit=%d out=%q stderr=%q paths=%q", tc.args, code, out, stderr, paths[before:])
		}
	}
}

func TestCLIRecipientCanonicalBoundaries(t *testing.T) {
	requests := 0
	env := idFixtureHub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected request: %s", r.URL)
			return
		}
		requests++
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"code":"unknown_recipient","message":"synthetic recipient is not enrolled","details":{}}}`)
	})
	for _, tc := range []struct {
		id                  string
		canonical, schemaOK bool
	}{
		{"peer_00000000000000000000000000", true, true},
		{"peer_7ZZZZZZZZZZZZZZZZZZZZZZZZZ", true, true},
		// Schema's suffix regex is broader; the existing strict parser rejects
		// overflow rather than changing any wire/schema/parser contract here.
		{"peer_8ZZZZZZZZZZZZZZZZZZZZZZZZZ", false, true},
		{"peer_01arz3ndektsv4rrffq69g5fav", false, false},
		{"peer_01ARZ3NDEKTSV4RRFFQ69G5FAO", false, false},
		{"peer_01ARZ3NDEKTSV4RRFFQ69G5FA", false, false},
	} {
		raw := []byte(fmt.Sprintf(`{"idempotency_key":"test","recipients":[%q],"priority":"hold","content":{"text":"test"}}`, tc.id))
		if schemaOK := envelope.Validate("sendRequest", raw) == nil; schemaOK != tc.schemaOK {
			t.Fatalf("schema boundary drift for %q: valid=%v", tc.id, schemaOK)
		}
		before := requests
		code, out, stderr := idRun(env, strings.NewReader(""), "send", tc.id, "text", "--hold")
		if tc.canonical {
			if code != exitHub || out != "" || !strings.HasPrefix(stderr, "unknown_recipient:") || requests != before+1 {
				t.Fatalf("canonical boundary did not reach hub: id=%q exit=%d out=%q stderr=%q calls=%d", tc.id, code, out, stderr, requests-before)
			}
		} else if code != exitUsage || out != "" || !strings.Contains(stderr, "expected peer_, team_ or grp_ ID") || requests != before {
			t.Fatalf("malformed/overflow boundary escaped preflight: id=%q exit=%d out=%q stderr=%q calls=%d", tc.id, code, out, stderr, requests-before)
		}
	}
}

func TestCLIOpaqueStreamCursorsPreserved(t *testing.T) {
	const opaque = "opaque-stream-position"
	var cursors []string
	env := idFixtureHub(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/peers":
			fmt.Fprint(w, `{"peers":[]}`)
			return
		case "/v1/hub/notices":
			fmt.Fprint(w, `{"notices":[]}`)
			return
		}
		if r.URL.Path != "/v1/events" {
			t.Errorf("unexpected stream request: %s", r.URL)
		}
		cursors = append(cursors, r.URL.Query().Get("after"))
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"code":"forbidden","message":"synthetic stream refusal","details":{}}}`)
	})
	for _, command := range []string{"inbox", "watch"} {
		before := len(cursors)
		code, out, stderr := idRun(env, strings.NewReader(""), command, "--since", opaque, "--as", idPeer)
		wantOut := ""
		if command == "watch" {
			wantOut = "CURSOR TYPE THREAD FROM MSG DELIVERY TEXT\n"
		}
		if code != exitHub || out != wantOut || !strings.HasPrefix(stderr, "forbidden:") || len(cursors) != before+1 || cursors[before] != opaque {
			t.Fatalf("stream cursor was validated/changed: cmd=%s exit=%d out=%q stderr=%q cursors=%q", command, code, out, stderr, cursors[before:])
		}
	}
}

// Missing scoped preflight (or checking only nonempty values) must fail before
// maintenance, file inspection, stdin or any identity/command HTTP request.
func TestCLIScopedPeerIDBeforeIO(t *testing.T) {
	g := &idGuardTransport{}
	old := http.DefaultTransport
	http.DefaultTransport = g
	t.Cleanup(func() { http.DefaultTransport = old })
	commands := [][]string{
		{"presence", "set", "idle", "--note", "note"},
		{"avatar", "set", filepath.Join(t.TempDir(), "FILESECRET-missing")},
		{"avatar", "clear"},
	}
	for ci, command := range commands {
		for bi, bad := range []string{idThread, "peer_short", "", "peer_" + strings.ToLower(idSuffix), "SECRET\x1b[31m\n\u202e", "https://fixture.invalid?sig=SECRET"} {
			for _, equals := range []bool{false, true} {
				t.Run(fmt.Sprintf("command%d/bad%d/equals%v", ci, bi, equals), func(t *testing.T) {
					env := idEnvironment(t, "http://127.0.0.1:1")
					// The final value wins, including an explicit empty overwrite.
					args := append([]string{"--as", idPeer}, command...)
					if equals {
						args = append(args, "--as="+bad)
					} else {
						args = append(args, "--as", bad)
					}
					var out, stderr bytes.Buffer
					input := &idCountingReader{}
					startup := 0
					before := g.calls
					code := RunContext(t.Context(), args, input, &out, &stderr, func(k string) string {
						if k == "VECNET_LOCAL_MAINTENANCE" {
							startup++
						}
						return env[k]
					})
					text := stderr.String()
					if code != exitUsage || out.Len() != 0 || startup != 0 || input.reads != 0 || g.calls != before || !strings.Contains(text, "--as") || !strings.Contains(text, "expected peer_ ID") {
						t.Fatalf("exit=%d out=%q stderr=%q startup=%d reads=%d requests=%d", code, out.String(), text, startup, input.reads, g.calls-before)
					}
					if len(text) > 400 || strings.Contains(text, "SECRET") || strings.ContainsAny(strings.TrimSuffix(text, "\n"), "\n\r\x1b\u202e") {
						t.Fatalf("unsafe scoped ID diagnostic: %q", text)
					}
				})
			}
		}
	}
}

func TestCLIScopedPeerIDGrammarAndValidPaths(t *testing.T) {
	var method, path, as, body string
	calls := 0
	env := idFixtureHub(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		method, path, as = r.Method, r.URL.Path, r.URL.Query().Get("as")
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		switch {
		case r.URL.Path == "/v1/peers/me/avatar" && r.Method == http.MethodPut:
			fmt.Fprint(w, `{"avatar":null}`)
		case r.URL.Path == "/v1/presence" && r.Method == http.MethodPost,
			r.URL.Path == "/v1/peers/me" && r.Method == http.MethodPatch,
			r.URL.Path == "/v1/peers/me/avatar" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected scoped request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	})
	file := filepath.Join(t.TempDir(), idMessage)
	if err := cliFixtureWriteFile(file, []byte("avatar-file-bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct {
		args             []string
		method, path, as string
		field, value     string
	}{
		{[]string{"presence", "--note", idMessage, "set", "idle", "--as=bad", "--as", idPeer}, "POST", "/v1/presence", idPeer, "note", idMessage},
		{[]string{"--as=bad", "avatar", "set", "https://fixture.invalid/picture", "--as=" + idPeer}, "PUT", "/v1/peers/me/avatar", idPeer, "url", "https://fixture.invalid/picture"},
		{[]string{"--as", idPeer, "avatar", "set", "--", file}, "PUT", "/v1/peers/me/avatar", idPeer, "", "avatar-file-bytes"},
		{[]string{"avatar", "--as", idPeer, "clear"}, "DELETE", "/v1/peers/me/avatar", idPeer, "", ""},
		{[]string{"presence", "set", "working"}, "POST", "/v1/presence", "", "state", "working"},
		{[]string{"avatar", "clear"}, "DELETE", "/v1/peers/me/avatar", "", "", ""},
		// subscribe is credential-wide and does not consume --as. Its existing
		// ignored global value must not become a new peer-selection contract.
		{[]string{"presence", "subscribe", "all", "--as=not-a-peer"}, "PATCH", "/v1/peers/me", "", "subscribe_presence", "all"},
		{[]string{"presence", "subscribe", "none", "--as="}, "PATCH", "/v1/peers/me", "", "subscribe_presence", "none"},
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			before := calls
			code, _, stderr := idRun(env, strings.NewReader(""), append([]string{"--json"}, tc.args...)...)
			if code != exitOK || stderr != "" || calls != before+1 || method != tc.method || path != tc.path || as != tc.as {
				t.Fatalf("valid scoped request changed: exit=%d stderr=%q calls=%d method=%s path=%s as=%q", code, stderr, calls-before, method, path, as)
			}
			if tc.field != "" {
				var fields map[string]string
				if json.Unmarshal([]byte(body), &fields) != nil || fields[tc.field] != tc.value {
					t.Fatalf("scoped payload changed: %s", body)
				}
			} else if body != tc.value {
				t.Fatalf("scoped bytes changed: %q want %q", body, tc.value)
			}
		})
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"avatar", "set"}, "usage: vecnet avatar"},
		{[]string{"avatar", "clear", "extra"}, "usage: vecnet avatar"},
		{[]string{"avatar", "clear", "--unknown"}, "unknown flag --unknown"},
		{[]string{"presence", "set"}, "usage: vecnet presence"},
		{[]string{"presence", "subscribe", "all", "--note", "text"}, "usage: vecnet presence"},
		{[]string{"presence", "set", "idle", "--note"}, "--note needs a value"},
		{[]string{"presence", "set", "idle", "--unknown"}, "unknown flag --unknown"},
	} {
		before := calls
		code, out, stderr := idRun(env, strings.NewReader(""), append([]string{"--as", idThread}, tc.args...)...)
		if code != exitUsage || out != "" || !strings.Contains(stderr, tc.want) || strings.Contains(stderr, "expected peer_") || calls != before {
			t.Fatalf("scoped syntax masked: args=%q exit=%d out=%q stderr=%q calls=%d", tc.args, code, out, stderr, calls-before)
		}
	}
	for _, command := range []string{"presence", "avatar"} {
		before := calls
		code, out, stderr := idRun(env, strings.NewReader(""), command, "--as", idThread, "--help")
		if code != exitOK || stderr != "" || !strings.Contains(out, command+" set") || calls != before {
			t.Fatalf("help masked by --as: command=%s exit=%d out=%q stderr=%q calls=%d", command, code, out, stderr, calls-before)
		}
	}
}
