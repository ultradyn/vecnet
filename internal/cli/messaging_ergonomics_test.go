package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/cli"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestSendReceiptNamesColdCache(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "Sender")
	b, _ := env.NewPeer("session", "Mira")
	u := profileUser(t, env.URL, secret)
	cachePath := filepath.Join(u.env["XDG_CACHE_HOME"], "vecnet", "peers.json")
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Fatalf("fixture is not cold: %v", err)
	}
	out := u.mustRun("send", b, "cold receipt", "--hold")
	if !strings.Contains(out, "Mira") || !strings.Contains(out, b) || !strings.Contains(out, "deliveries 1") || len(nonEmptyLines(out)) != 1 {
		t.Errorf("receipt lacks name/full destination ID: %q", out)
	}
	var receipt struct {
		Deliveries []struct {
			Recipient string `json:"recipient"`
			Name      string `json:"recipient_name"`
		} `json:"deliveries"`
	}
	raw := u.mustRun("--json", "send", b, "json receipt", "--hold")
	if err := json.Unmarshal([]byte(raw), &receipt); err != nil {
		t.Fatal(err)
	}
	if len(receipt.Deliveries) != 1 || receipt.Deliveries[0].Recipient != b || receipt.Deliveries[0].Name != "Mira" {
		t.Fatalf("JSON receipt lacks authoritative name/full ID: %s", raw)
	}
}

func TestReplyReceiptNamesActualParticipants(t *testing.T) {
	env := testhub.Start(t)
	a, aSecret := env.NewPeer("session", "Alpha")
	b, bSecret := env.NewPeer("session", "Bravo")
	c, _ := env.NewPeer("session", "Charlie")
	u, v := profileUser(t, env.URL, aSecret), profileUser(t, env.URL, bSecret)
	first := u.send(b, "first", "--hold")
	u.send(c, "join Charlie", "--thread", first.ThreadID, "--hold")
	v.mustRun("inbox") // Cache the original message/thread, not a destination list.
	out := v.mustRun("reply", first.MessageID, "answer all current participants", "--hold")
	for _, want := range []string{"Alpha", a, "Charlie", c, "deliveries 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("reply receipt %q lacks %q", out, want)
		}
	}
	if strings.Contains(out, b) {
		t.Fatal("reply receipt included the replying sender")
	}
}

func TestSendReceiptGroupNamesAndFullIDs(t *testing.T) {
	env := testhub.Start(t)
	a, secret := env.NewPeer("session", "Sender")
	b, _ := env.NewPeer("session", "Shared")
	c, _ := env.NewPeer("session", "Shared")
	group, err := env.Hub.CreateTeam(context.Background(), auth.Caller{Kind: "operator"}, "Receipt Team", []string{a, b, c})
	if err != nil {
		t.Fatal(err)
	}
	out := profileUser(t, env.URL, secret).mustRun("send", b, group, "group receipt", "--hold")
	if strings.Count(out, "Shared") != 2 || strings.Count(out, b) != 1 || strings.Count(out, c) != 1 || !strings.Contains(out, group) || !strings.Contains(out, "deliveries 2") || strings.Contains(out, a) {
		t.Fatalf("group receipt does not describe distinct actual destinations/via: %q", out)
	}
}

func TestSendReceiptMetadataNeedsNoDirectoryRequest(t *testing.T) {
	const peer = "peer_00000000000000000000000002"
	var sends, directory atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/messages":
			sends.Add(1)
			if r.Method != http.MethodPost {
				t.Error("receipt path changed send method")
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprintf(w, `{"message_id":"msg_00000000000000000000000001","thread_id":"thr_00000000000000000000000001","deliveries":[{"delivery_id":"dly_00000000000000000000000001","recipient":%q,"recipient_name":"Mira","via":null,"state":"queued"}],"spilled":false}`, peer)
		case "/v1/peers":
			directory.Add(1)
			http.Error(w, "directory unavailable", http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r) // Existing optional identity pin discovery is allowed.
		}
	}))
	defer srv.Close()
	out := profileUser(t, srv.URL, "synthetic-secret").mustRun("send", peer, "one send", "--hold")
	if sends.Load() != 1 || directory.Load() != 0 || !strings.Contains(out, "Mira") || !strings.Contains(out, peer) {
		t.Fatalf("receipt coupled to extra send/directory work: sends=%d directory=%d out=%q", sends.Load(), directory.Load(), out)
	}
}

func TestSendReceiptMissingNameAndHostileLabel(t *testing.T) {
	const peer = "peer_00000000000000000000000002"
	for _, scenario := range []struct {
		name, label string
		present     bool
	}{
		{name: "missing"},
		{name: "empty", present: true},
		{name: "hostile", label: "Mira\x1b]2;owned\a\nFAKE\r\t\u202e\"(peer_spoof)", present: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			label := scenario.label
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/messages" {
					http.NotFound(w, r)
					return
				}
				delivery := map[string]any{"delivery_id": "dly_old", "recipient": peer, "via": nil, "state": "queued"}
				if scenario.present {
					delivery["recipient_name"] = label
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				json.NewEncoder(w).Encode(map[string]any{"message_id": "msg_old", "thread_id": "thr_old", "deliveries": []any{delivery}, "spilled": true, "job_id": "job_old"})
			}))
			defer srv.Close()
			u := profileUser(t, srv.URL, "synthetic-secret")
			// A stale cache must not fabricate a current/send-time receipt name.
			cachePath := filepath.Join(u.env["XDG_CACHE_HOME"], "vecnet", "peers.json")
			if err := cliFixtureMkdirAll(filepath.Dir(cachePath), 0700); err != nil {
				t.Fatal(err)
			}
			cached, _ := json.Marshal(map[string]string{peer: "StaleCacheName"})
			if err := cliFixtureWriteFile(cachePath, cached, 0600); err != nil {
				t.Fatal(err)
			}
			out := u.mustRun("send", peer, "legacy or hostile label", "--hold")
			if !strings.Contains(out, peer) || !strings.Contains(out, "(spilled)") || !strings.Contains(out, "job job_old") || strings.Contains(out, "StaleCacheName") || strings.Count(out, "\n") != 1 || strings.ContainsAny(strings.TrimSuffix(out, "\n"), "\x1b\a\r\t\u202e") {
				t.Fatalf("unsafe/incomplete receipt output: %q", out)
			}
			if label == "" && !strings.Contains(strings.ToLower(out), "name unavailable") {
				t.Fatalf("legacy receipt needs explicit name-unavailable fallback: %q", out)
			}
			if label != "" && !strings.Contains(out, "Mira") {
				t.Fatalf("sanitization lost the available name: %q", out)
			}
			if label != "" {
				start := strings.Index(out, `"Mira`)
				if start < 0 {
					t.Fatalf("untrusted name is not quoted: %q", out)
				}
				quoted, err := strconv.QuotedPrefix(out[start:])
				if err != nil || !strings.HasPrefix(out[start+len(quoted):], "("+peer+")") {
					t.Fatalf("name quoting does not delimit canonical destination: %q", out)
				}
				var wire struct {
					Deliveries []map[string]json.RawMessage `json:"deliveries"`
				}
				raw := u.mustRun("--json", "send", peer, "JSON label", "--hold")
				if err := json.Unmarshal([]byte(raw), &wire); err != nil {
					t.Fatal(err)
				}
				var got string
				if len(wire.Deliveries) != 1 || json.Unmarshal(wire.Deliveries[0]["recipient_name"], &got) != nil || got != label {
					t.Fatal("JSON label was terminal-formatted instead of encoded as data")
				}
			}
		})
	}
}

func TestInboxJSONSenderChainDefaultAndOptIn(t *testing.T) {
	for _, references := range []bool{false, true} {
		t.Run(fmt.Sprintf("references=%t", references), func(t *testing.T) {
			env := testhub.StartWith(t, func(cfg *hub.Config) { cfg.DeliveryReferences = references })
			a, secret := env.NewPeer("session", "Alpha")
			b, bSecret := env.NewPeer("session", "Bravo")
			sender, reader := profileUser(t, env.URL, secret), profileUser(t, env.URL, bSecret)
			first := sender.send(b, "first", "--hold")
			middle := reader.send2("reply", first.MessageID, "middle", "--thread", first.ThreadID, "--hold")
			resp, raw := env.Do(secret, http.MethodPost, "/v1/messages", map[string]any{
				"idempotency_key": "compact-final", "thread_id": first.ThreadID, "in_reply_to": middle.MessageID,
				"priority": "hold", "content": map[string]any{"text": "compact inbox"},
				"metadata": json.RawMessage(`{"sender_chain":["keep"],"large":9007199254740993,"precise":1.2300}`),
			})
			var sent sendResp
			if resp.StatusCode != http.StatusAccepted || json.Unmarshal(raw, &sent) != nil || sent.MessageID == "" {
				t.Fatalf("final fixture send: %d %s", resp.StatusCode, raw)
			}
			sender.mustRun("receipt", middle.Deliveries[0].DeliveryID, "read")
			canonicalBefore := ergCanonicalPage(t, env, bSecret)
			stored := func() [4]string {
				var fields [4]string
				err := env.Hub.DB.QueryRow(`SELECT m.body,d.body_sha256,e.data,e.payload_format FROM messages m JOIN deliveries d ON d.msg_id=m.msg_id JOIN events e ON e.seq=d.event_seq WHERE m.msg_id=?`, sent.MessageID).Scan(&fields[0], &fields[1], &fields[2], &fields[3])
				if err != nil {
					t.Fatal(err)
				}
				return fields
			}
			storedBefore := stored()
			compact := []byte(reader.mustRun("--json", "inbox"))
			if _, present := ergCLILoop(t, compact, sent.MessageID)["sender_chain"]; present {
				t.Error("default inbox JSON still contains delivery message.loop.sender_chain")
			}
			expected := ergCLIJSONValue(t, canonicalBefore).(map[string]any)
			hasReceipt := false
			for _, item := range expected["events"].([]any) {
				event := item.(map[string]any)
				if event["type"] == "delivery" {
					delete(event["data"].(map[string]any)["message"].(map[string]any)["loop"].(map[string]any), "sender_chain")
				}
				hasReceipt = hasReceipt || event["type"] == "receipt"
			}
			if !hasReceipt || !reflect.DeepEqual(expected, ergCLIJSONValue(t, compact)) {
				t.Errorf("compact must change only the delivery ancestry key, preserving metadata, other loop fields, events and cursor: %s", compact)
			}
			cached := ergCLICache(t, reader)
			if cached[sent.MessageID] != first.ThreadID || cached["next_cursor"] != "" {
				t.Fatalf("compact view poisoned message/thread cache or cached an inbox cursor: %#v", cached)
			}
			code, full, stderr := reader.run("--json", "inbox", "--include-sender-chain")
			if code != 0 {
				t.Fatalf("full-view flag/admission failure, not a projection assertion: code=%d stderr=%s", code, stderr)
			}
			var chain []string
			if err := json.Unmarshal(ergCLILoop(t, []byte(full), sent.MessageID)["sender_chain"], &chain); err != nil || !reflect.DeepEqual(chain, []string{a, b, a}) {
				t.Fatalf("opt-in lost ordered ancestry: %s %v", full, err)
			}
			if !reflect.DeepEqual(ergCLIJSONValue(t, canonicalBefore), ergCLIJSONValue(t, []byte(full))) {
				t.Fatal("full view differs from canonical HTTP event values or cursor")
			}
			if storedBefore != stored() || !bytes.Equal(canonicalBefore, ergCanonicalPage(t, env, bSecret)) {
				t.Fatal("inbox reads changed stored Message, delivery hash, event data/format or canonical HTTP bytes")
			}
		})
	}
}

func TestInboxTextSenderChainOptIn(t *testing.T) {
	env := testhub.Start(t)
	a, secret := env.NewPeer("session", "Alpha")
	b, bSecret := env.NewPeer("session", "Bravo")
	sender, reader := profileUser(t, env.URL, secret), profileUser(t, env.URL, bSecret)
	first := sender.send(b, "text inbox", "--hold")
	reader.send2("reply", first.MessageID, "ordered answer", "--thread", first.ThreadID, "--hold")
	reader.mustRun("receipt", first.Deliveries[0].DeliveryID, "read")
	plain := sender.mustRun("inbox")
	code, full, stderr := sender.run("inbox", "--include-sender-chain")
	if code != 0 {
		t.Fatalf("text opt-in flag/admission failure: code=%d stderr=%s", code, stderr)
	}
	chain, err := json.Marshal([]string{a, b})
	if err != nil {
		t.Fatal(err)
	}
	suffix := " sender_chain=" + string(chain)
	if !strings.Contains(plain, "receipt") || strings.Contains(plain, "sender_chain=") || strings.Count(full, suffix) != 1 || strings.Replace(full, suffix, "", 1) != plain {
		t.Fatalf("opt-in must append only the exact ordered JSON chain to the delivery row: plain=%q full=%q", plain, full)
	}
}

type ergFailedOutput struct {
	calls int
	short bool
}

func (writer *ergFailedOutput) Write(data []byte) (int, error) {
	writer.calls++
	if writer.short && len(data) > 0 {
		return len(data) - 1, nil
	}
	return 0, io.ErrClosedPipe
}

func TestInboxChainOptInFailedOutputNeverAcknowledges(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		for _, include := range []bool{false, true} {
			for _, short := range []bool{false, true} {
				t.Run(fmt.Sprintf("json=%t/include=%t/short=%t", asJSON, include, short), func(t *testing.T) {
					env := testhub.Start(t)
					_, secret := env.NewPeer("session", "Alpha")
					b, bSecret := env.NewPeer("session", "Bravo")
					sent := profileUser(t, env.URL, secret).send(b, "must remain unread", "--hold")
					reader := profileUser(t, env.URL, bSecret)
					canonicalBefore := ergCanonicalPage(t, env, bSecret)
					var beforePage ergCLIPage
					if err := json.Unmarshal(canonicalBefore, &beforePage); err != nil {
						t.Fatal(err)
					}
					args := []string{"inbox", "--ack"}
					if asJSON {
						args = append([]string{"--json"}, args...)
					}
					if include {
						args = append(args, "--include-sender-chain")
					}
					writer := &ergFailedOutput{short: short}
					var stderr bytes.Buffer
					code := cli.Run(args, strings.NewReader(""), writer, &stderr, reader.getenv)
					if include && strings.Contains(stderr.String(), "unknown flag --include-sender-chain") {
						t.Fatalf("flag prerequisite rejected before stdout; not writer-path RED: %s", stderr.String())
					}
					if code == 0 || writer.calls != 1 {
						t.Fatalf("failed/short complete-page write must fail once before ack: code=%d calls=%d stderr=%s", code, writer.calls, stderr.String())
					}
					var state string
					if err := env.Hub.DB.QueryRow("SELECT state FROM deliveries WHERE dly_id=?", sent.Deliveries[0].DeliveryID).Scan(&state); err != nil {
						t.Fatal(err)
					}
					cached := ergCLICache(t, reader)
					if state != "queued" || cached[sent.MessageID] != sent.ThreadID || cached["next_cursor"] != "" || !bytes.Equal(canonicalBefore, ergCanonicalPage(t, env, bSecret)) {
						t.Fatal("failed output acknowledged, poisoned the cache/cursor or changed canonical delivery data")
					}
					recoveredRaw := []byte(reader.mustRun("--json", "inbox"))
					var recovered ergCLIPage
					if err := json.Unmarshal(recoveredRaw, &recovered); err != nil || len(recovered.Events) == 0 || recovered.NextCursor != beforePage.NextCursor {
						t.Fatalf("unacknowledged page could not be read again: %+v %v", recovered, err)
					}
					ergCLILoop(t, recoveredRaw, sent.MessageID)
				})
			}
		}
	}
}

func TestInboxProjectionMalformedPageNeverAcknowledges(t *testing.T) {
	for _, mode := range []struct {
		name string
		args []string
	}{
		{"compact-json", []string{"--json", "inbox", "--ack"}},
		{"compact-text", []string{"inbox", "--ack"}},
		{"full-text", []string{"inbox", "--include-sender-chain", "--ack"}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			var acks, polls atomic.Int32
			var repaired, explicitAfter atomic.Bool
			page := `{"events":[{"cursor":"e1.1","type":"delivery","data":{"delivery_id":"dly_one","message":{"message_id":"msg_one","thread_id":"thr_one","loop":{"sender_chain":["peer_test"]},"content":{"text":"first"}}}},{"cursor":"e1.2","type":"delivery","data":{"delivery_id":"dly_bad","message":{"message_id":"msg_bad","thread_id":"thr_one","loop":[]}}}],"next_cursor":"e1.2"}`
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/v1/peers/me":
					fmt.Fprint(w, `{"kind":"peer","peer_id":"peer_test"}`)
				case "/v1/events":
					polls.Add(1)
					if r.URL.Query().Get("after") != "" {
						explicitAfter.Store(true)
					}
					body := page
					if repaired.Load() {
						body = strings.Replace(body, `"loop":[]`, `"loop":{"sender_chain":[]}`, 1)
					}
					fmt.Fprint(w, body)
				case "/v1/ack":
					acks.Add(1)
					fmt.Fprint(w, "{}")
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			u := profileUser(t, srv.URL, "synthetic-secret")
			cachePath := filepath.Join(u.env["XDG_CACHE_HOME"], "vecnet", "messages.json")
			if err := cliFixtureMkdirAll(filepath.Dir(cachePath), 0700); err != nil {
				t.Fatal(err)
			}
			if err := cliFixtureWriteFile(cachePath, []byte(`{"msg_cached":"thr_cached"}`), 0600); err != nil {
				t.Fatal(err)
			}
			code, out, stderr := u.run(mode.args...)
			if strings.Contains(stderr, "unknown flag --include-sender-chain") {
				t.Fatalf("flag prerequisite rejected before malformed-page rendering: %s", stderr)
			}
			if code == 0 || out != "" || acks.Load() != 0 || polls.Load() != 1 {
				t.Fatalf("late malformed event must produce no page/ack: code=%d output=%q acks=%d polls=%d stderr=%s", code, out, acks.Load(), polls.Load(), stderr)
			}
			cached := ergCLICache(t, u)
			if cached["msg_cached"] != "thr_cached" || (cached["msg_one"] != "" && cached["msg_one"] != "thr_one") || cached["next_cursor"] != "" {
				t.Fatalf("failed projection poisoned existing cache or persisted a cursor: %#v", cached)
			}
			repaired.Store(true)
			raw := []byte(u.mustRun("--json", "inbox"))
			var recovered ergCLIPage
			if err := json.Unmarshal(raw, &recovered); err != nil || len(recovered.Events) != 2 || recovered.NextCursor != "e1.2" || acks.Load() != 0 || explicitAfter.Load() {
				t.Fatalf("failed page advanced/replaced the cursor or lost replayable events: %+v %v", recovered, err)
			}
			ergCLILoop(t, raw, "msg_one")
			ergCLILoop(t, raw, "msg_bad")
		})
	}
}

type ergCLIPage struct {
	Events     []envelope.Event `json:"events"`
	NextCursor string           `json:"next_cursor"`
}

func ergCLILoop(t *testing.T, raw []byte, messageID string) map[string]json.RawMessage {
	t.Helper()
	var page ergCLIPage
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	for _, event := range page.Events {
		if event.Type != "delivery" {
			continue
		}
		var data struct {
			Message struct {
				ID   string                     `json:"message_id"`
				Loop map[string]json.RawMessage `json:"loop"`
			} `json:"message"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			t.Fatal(err)
		}
		if data.Message.ID == messageID {
			return data.Message.Loop
		}
	}
	t.Fatalf("page has no delivery for %s: %s", messageID, raw)
	return nil
}

func ergCanonicalPage(t *testing.T, env *testhub.Env, secret string) []byte {
	t.Helper()
	resp, raw := env.Do(secret, http.MethodGet, "/v1/events?after=e1.0&wait=0", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("canonical inbox: %d %s", resp.StatusCode, raw)
	}
	return raw
}

func ergCLIJSONValue(t *testing.T, raw []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func ergCLICache(t *testing.T, reader *user) map[string]string {
	t.Helper()
	path := filepath.Join(reader.env["XDG_CACHE_HOME"], "vecnet", "messages.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cached map[string]string
	if err := json.Unmarshal(raw, &cached); err != nil {
		t.Fatal(err)
	}
	return cached
}

func TestMCPRelayInboxSenderChainParity(t *testing.T) {
	env := testhub.Start(t)
	_, aSecret := env.NewPeer("session", "Alpha")
	b, bSecret := env.NewPeer("session", "Bravo")
	sent := profileUser(t, env.URL, aSecret).send(b, "relay parity", "--hold")
	direct := func(t *testing.T, method string, params any) json.RawMessage {
		t.Helper()
		body := relayLine(9, method, params)
		req, err := http.NewRequest(http.MethodPost, env.URL+"/mcp", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+bSecret)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2026-07-28")
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var reply relayReply
		if err := json.NewDecoder(response.Body).Decode(&reply); err != nil || response.StatusCode != http.StatusOK || reply.Error != nil {
			t.Fatalf("direct MCP failed: status=%d reply=%+v err=%v", response.StatusCode, reply, err)
		}
		return reply.Result
	}
	defaultParams := map[string]any{"name": "vecnet_inbox", "arguments": map[string]any{}}
	fullParams := map[string]any{"name": "vecnet_inbox", "arguments": map[string]any{"include_sender_chain": true}}
	resourceParams := map[string]any{"uri": "vecnet://peer/" + b + "/inbox"}
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			var inboxCalls, resourceCalls, unintended atomic.Int32
			target, err := url.Parse(env.URL)
			if err != nil {
				t.Fatal(err)
			}
			proxy := httputil.NewSingleHostReverseProxy(target)
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/events" || r.URL.Path == "/v1/ack" || (r.URL.Path == "/mcp" && r.Method == http.MethodGet) {
					unintended.Add(1)
				}
				if r.URL.Path == "/mcp" && r.Method == http.MethodPost {
					raw, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						http.Error(w, "read failed", http.StatusBadRequest)
						return
					}
					r.Body = io.NopCloser(bytes.NewReader(raw))
					var request struct {
						Method string `json:"method"`
						Params struct {
							Name string `json:"name"`
						} `json:"params"`
					}
					if err := json.Unmarshal(raw, &request); err != nil {
						t.Error(err)
					}
					if request.Method == "resources/read" {
						resourceCalls.Add(1)
					}
					switch request.Method {
					case "events/subscribe", "subscriptions/listen", "resources/subscribe":
						unintended.Add(1)
					}
					if request.Method == "tools/call" && request.Params.Name == "vecnet_inbox" {
						inboxCalls.Add(1)
					}
					if request.Method == "tools/call" && (request.Params.Name == "vecnet_ack" || request.Params.Name == "vecnet_receipt") {
						unintended.Add(1)
					}
				}
				proxy.ServeHTTP(w, r)
			}))
			defer gateway.Close()
			// initialize intentionally negotiates legacy even when offered current.
			// Exercise the stateless era with discovery instead.
			initLine := relayInit(version)
			if version == "2026-07-28" {
				initLine = relayLine(1, "server/discover", map[string]any{"_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": version}})
			}
			replies := relayOK(t, profileUser(t, gateway.URL, bSecret), []string{
				initLine,
				relayLine(2, "tools/call", defaultParams),
				relayLine(3, "tools/call", fullParams),
				relayLine(4, "resources/read", resourceParams),
			})
			relayIDs(t, replies, 1, 2, 3, 4)
			for i, expected := range []json.RawMessage{direct(t, "tools/call", defaultParams), direct(t, "tools/call", fullParams), direct(t, "resources/read", resourceParams)} {
				if replies[i+1].Error != nil || !reflect.DeepEqual(ergCLIMCPPayload(t, replies[i+1].Result), ergCLIMCPPayload(t, expected)) {
					t.Errorf("stdio/direct MCP result %d diverged: %+v", i, replies[i+1])
				}
			}
			var compact struct {
				Structured json.RawMessage `json:"structuredContent"`
			}
			if err := json.Unmarshal(replies[1].Result, &compact); err != nil {
				t.Fatal(err)
			}
			if _, present := ergCLILoop(t, compact.Structured, sent.MessageID)["sender_chain"]; present {
				t.Error("stdio default inbox still includes sender_chain")
			}
			if inboxCalls.Load() != 2 || resourceCalls.Load() != 1 || unintended.Load() != 0 {
				t.Fatalf("relay introduced consumer/ack work: inbox=%d resources=%d unintended=%d", inboxCalls.Load(), resourceCalls.Load(), unintended.Load())
			}
		})
	}
}

// Current stateless replies add two protocol envelope fields. Compare all
// application fields across eras, validating the known transport fields first.
func ergCLIMCPPayload(t *testing.T, raw []byte) any {
	t.Helper()
	value, ok := ergCLIJSONValue(t, raw).(map[string]any)
	if !ok {
		t.Fatal("MCP result is not an object")
	}
	if meta, ok := value["_meta"].(map[string]any); ok {
		if info, present := meta["io.modelcontextprotocol/serverInfo"]; present {
			if !reflect.DeepEqual(info, map[string]any{"name": "vecnet", "version": "dev"}) {
				t.Fatalf("unexpected serverInfo: %v", info)
			}
			delete(meta, "io.modelcontextprotocol/serverInfo")
		}
		if len(meta) == 0 {
			delete(value, "_meta")
		}
	}
	if kind, present := value["resultType"]; present {
		if kind != "complete" {
			t.Fatalf("unexpected resultType: %v", kind)
		}
		delete(value, "resultType")
	}
	return value
}
