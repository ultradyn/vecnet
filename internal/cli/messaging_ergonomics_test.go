package cli_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/XertroV/vecnet/internal/auth"
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
			if err := os.MkdirAll(filepath.Dir(cachePath), 0700); err != nil {
				t.Fatal(err)
			}
			cached, _ := json.Marshal(map[string]string{peer: "StaleCacheName"})
			if err := os.WriteFile(cachePath, cached, 0600); err != nil {
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
