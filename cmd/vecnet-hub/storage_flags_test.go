package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/XertroV/vecnet/internal/store"
)

// TestStorageSwitchesIndependent boots the binary with every combination of
// --delivery-references and --storage-backfill (and with both omitted). The
// backfill status follows only --storage-backfill, and the format a new send
// persists follows only --delivery-references.
func TestStorageSwitchesIndependent(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		flags                []string
		references, backfill bool
	}{
		{name: "omitted"},
		{name: "refs_off_backfill_off", flags: []string{"--delivery-references=false", "--storage-backfill=false"}},
		{name: "refs_off_backfill_on", flags: []string{"--delivery-references=false", "--storage-backfill=true"}, backfill: true},
		{name: "refs_on_backfill_off", flags: []string{"--delivery-references=true", "--storage-backfill=false"}, references: true},
		{name: "refs_on_backfill_on", flags: []string{"--delivery-references=true", "--storage-backfill=true"}, references: true, backfill: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dataDir, opSecret := initHub(t)
			args := append([]string{"--data-dir", dataDir, "--listen", "127.0.0.1:0", "--storage-backfill-rows=2", "--storage-backfill-bytes=2048", "--storage-backfill-target=5ms"}, tc.flags...)
			hb := bootHub(t, args, noEnv)
			admin := func(method, path string, body any) []byte {
				t.Helper()
				resp, out := hubDo(t, method, hb.base+path, opSecret, body)
				if resp.StatusCode/100 != 2 {
					t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, out)
				}
				return out
			}
			var status struct {
				Enabled bool `json:"enabled"`
			}
			if err := json.Unmarshal(admin(http.MethodGet, "/v1/admin/storage/backfill", nil), &status); err != nil || status.Enabled != tc.backfill {
				t.Fatalf("backfill enabled = %v (%v), want %v", status.Enabled, err, tc.backfill)
			}
			var sender, recipient struct {
				PeerID     string `json:"peer_id"`
				EnrolToken string `json:"enrol_token"`
			}
			if err := json.Unmarshal(admin(http.MethodPost, "/v1/admin/peers", map[string]string{"kind": "session", "name": "sender"}), &sender); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(admin(http.MethodPost, "/v1/admin/peers", map[string]string{"kind": "session", "name": "recipient"}), &recipient); err != nil {
				t.Fatal(err)
			}
			resp, body := hubDo(t, http.MethodPost, hb.base+"/v1/enrol", "", map[string]string{"token": sender.EnrolToken})
			var enrolled struct {
				Secret string `json:"secret"`
			}
			if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &enrolled) != nil {
				t.Fatalf("enrol: %d", resp.StatusCode)
			}
			resp, body = hubDo(t, http.MethodPost, hb.base+"/v1/messages", enrolled.Secret, map[string]any{
				"idempotency_key": "storage-flags", "priority": "hold", "recipients": []string{recipient.PeerID},
				"content": map[string]any{"text": "synthetic storage flag probe"},
			})
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("send: %d %s", resp.StatusCode, body)
			}
			if err := hb.stop(); err != nil && err != http.ErrServerClosed {
				t.Fatalf("hub stop: %v", err)
			}
			db, err := store.Open(filepath.Join(dataDir, "hub.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var formats []string
			rows, err := db.Query(`SELECT payload_format FROM events WHERE type='delivery' AND owner=? ORDER BY seq`, recipient.PeerID)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			for rows.Next() {
				var f string
				if err := rows.Scan(&f); err != nil {
					t.Fatal(err)
				}
				formats = append(formats, f)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			want := "inline"
			if tc.references {
				want = "delivery_ref_v1"
			}
			if fmt.Sprint(formats) != "["+want+"]" {
				t.Fatalf("persisted delivery formats = %v, want [%s]", formats, want)
			}
		})
	}
}
