package cli_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSendAdvisoryIsVisibleWithoutExtraRequests(t *testing.T) {
	const peer = "peer_00000000000000000000000002"
	const group = "grp_00000000000000000000000001"
	warnings := `[null,17,{"code":"rapid_thread_creation","message":42},{"code":"rapid_thread_creation","message":"Rapid thread creation; reply to an existing thread for related work. Separate topics are fine.","group_id":"` + group + `","window_seconds":60,"threshold":2},{"code":"recipient_not_seen","message":"Send succeeded. Recipient was last seen 75 minutes ago.","recipient":"` + peer + `","last_seen":"2026-11-15T00:00:00.000Z","age_seconds":4500,"threshold_seconds":3600}]`
	var sends, directory atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			if r.URL.Path == "/v1/peers" || strings.HasPrefix(r.URL.Path, "/v1/groups") {
				directory.Add(1)
			}
			http.NotFound(w, r)
			return
		}
		sends.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, `{"message_id":"msg_warning","thread_id":"thr_warning","deliveries":[],"spilled":false,"warnings":%s}`, warnings)
	}))
	defer srv.Close()
	u := profileUser(t, srv.URL, "synthetic-secret")
	code, out, stderr := u.run("send", peer, "successful send", "--hold")
	if code != 0 || !strings.Contains(out, "sent msg_warning thread thr_warning") || !strings.Contains(stderr, "warning:") || !strings.Contains(stderr, group) || !strings.Contains(stderr, "reply to an existing thread") || !strings.Contains(stderr, peer) || !strings.Contains(stderr, "75 minutes ago") || sends.Load() != 1 || directory.Load() != 0 {
		t.Fatalf("advisory missing or changed successful send: code=%d out=%q stderr=%q sends=%d directory=%d", code, out, stderr, sends.Load(), directory.Load())
	}
	code, out, stderr = u.run("--json", "send", peer, "json send", "--hold")
	var raw map[string]json.RawMessage
	if code != 0 || stderr != "" || json.Unmarshal([]byte(out), &raw) != nil || !reflect.DeepEqual(ergCLIJSONValue(t, raw["warnings"]), ergCLIJSONValue(t, []byte(warnings))) {
		t.Fatalf("JSON receipt lost raw warning metadata: code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestSendAdvisoryMalformedMetadataNeverFailsSuccessfulSend(t *testing.T) {
	for _, warning := range []string{`null`, `"unexpected"`, `{}`, `[false,{"code":23,"message":"bad"},{"code":"future_warning","message":{}}]`, `[{"code":"rapid_thread_creation","message":"Reply\u001b]2;owned\u0007\nexisting\r\tthread\u202e","group_id":"grp_test\nspoof"}]`} {
		t.Run(warning, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/messages" {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				fmt.Fprintf(w, `{"message_id":"msg_ok","thread_id":"thr_ok","deliveries":[],"spilled":false,"warnings":%s}`, warning)
			}))
			defer srv.Close()
			code, out, stderr := profileUser(t, srv.URL, "synthetic-secret").run("send", "peer_00000000000000000000000002", "sent", "--hold")
			if code != 0 || !strings.Contains(out, "sent msg_ok") || strings.ContainsAny(strings.TrimSuffix(stderr, "\n"), "\x1b\a\r\t\u202e\n") {
				t.Fatalf("optional metadata failed send or injected terminal output: code=%d out=%q stderr=%q", code, out, stderr)
			}
			if strings.Contains(warning, "owned") && (!strings.Contains(stderr, "Reply") || !strings.Contains(stderr, "thread")) {
				t.Fatalf("sanitization erased valid advisory: %q", stderr)
			}
		})
	}
}
