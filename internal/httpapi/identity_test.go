package httpapi_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hubid"
	"github.com/XertroV/vecnet/internal/testhub"
)

func identityDoc(t *testing.T, e *testhub.Env, query string) envelope.HubIdentity {
	t.Helper()
	resp, body := e.Do("", http.MethodGet, "/v1/hub/identity"+query, nil)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("identity: %d cache=%q %s", resp.StatusCode, resp.Header.Get("Cache-Control"), body)
	}
	var doc envelope.HubIdentity
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func identityChallenge() string {
	return base64.RawURLEncoding.EncodeToString([]byte(rand.Text()))
}

func TestGetHubIdentityPublic(t *testing.T) {
	e := testhub.Start(t)
	doc := identityDoc(t, e, "")
	if _, err := hubid.DecodePubkey(doc.Pubkey); err != nil {
		t.Fatal(err)
	}
	fp, err := hubid.Fingerprint(doc.Pubkey)
	if err != nil || doc.Fingerprint != fp || doc.PublicURL != e.Hub.Cfg.PublicURL {
		t.Fatalf("identity = %+v, fingerprint error = %v", doc, err)
	}
	if doc.Previous != nil || doc.ChallengeSig != "" {
		t.Fatalf("unexpected proof or previous key: %+v", doc)
	}
	if _, err := envelope.ParseTime(doc.KeyCreatedAt); err != nil {
		t.Fatal(err)
	}
	_, body := e.Do("", http.MethodGet, "/v1/hub/identity", nil)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 4 {
		t.Fatalf("unexpected public identity fields: %s", body)
	}
}

func TestGetHubIdentityChallenge(t *testing.T) {
	e := testhub.Start(t)
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	c := base64.RawURLEncoding.EncodeToString(raw)
	doc := identityDoc(t, e, "?challenge="+c)
	if err := hubid.VerifyChallenge(doc.Pubkey, doc.PublicURL, c, doc.ChallengeSig); err != nil {
		t.Fatal(err)
	}
}

func TestGetHubIdentityBadChallenge(t *testing.T) {
	e := testhub.Start(t)
	for _, c := range []string{base64.RawURLEncoding.EncodeToString(make([]byte, 8)), base64.URLEncoding.EncodeToString(make([]byte, 32)), strings.Repeat("+", 43), ""} {
		t.Run(c, func(t *testing.T) {
			resp, body := e.Do("", http.MethodGet, "/v1/hub/identity?challenge="+url.QueryEscape(c), nil)
			var got struct {
				Error struct {
					Code    string         `json:"code"`
					Details map[string]any `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != 422 || got.Error.Code != "invalid_value" || got.Error.Details["field"] != "challenge" {
				t.Fatalf("bad challenge: %d %s", resp.StatusCode, body)
			}
		})
	}
}

func TestEnrolReturnsHubPubkey(t *testing.T) {
	e := testhub.Start(t)
	peer, token, err := e.Hub.AddPeer(context.Background(), "session", "identity-peer", "")
	if err != nil {
		t.Fatal(err)
	}
	resp, body := e.Do("", http.MethodPost, "/v1/enrol", map[string]string{"token": token})
	var got struct {
		PeerID    string `json:"peer_id"`
		Secret    string `json:"secret"`
		HubPubkey string `json:"hub_pubkey"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || got.PeerID != peer || got.Secret == "" || got.HubPubkey == "" {
		t.Fatalf("enrol: %d %s", resp.StatusCode, body)
	}
	if doc := identityDoc(t, e, ""); got.HubPubkey != doc.Pubkey {
		t.Fatalf("enrol key %q differs from identity %q", got.HubPubkey, doc.Pubkey)
	}
}

func TestCloneDBSameIdentity(t *testing.T) {
	e := testhub.Start(t)
	peer, secret := e.NewPeer("session", "clone-peer")
	clone := e.CloneDB(t)
	original, copied := identityDoc(t, e, ""), identityDoc(t, clone, "")
	if original.Pubkey != copied.Pubkey || original.Fingerprint != copied.Fingerprint || original.KeyCreatedAt != copied.KeyCreatedAt {
		t.Fatalf("clone identity differs: %+v / %+v", original, copied)
	}
	if e.URL == clone.URL || copied.PublicURL != clone.URL || original.PublicURL != e.URL {
		t.Fatalf("clone must have its own public URL: %+v / %+v", original, copied)
	}
	for _, env := range []*testhub.Env{e, clone} {
		resp, body := env.Do(secret, http.MethodGet, "/v1/peers/me", nil)
		if resp.StatusCode != 200 || !strings.Contains(string(body), peer) {
			t.Fatalf("copied peer credential: %d %s", resp.StatusCode, body)
		}
		resp, body = env.Do(e.OperatorSecret, http.MethodGet, "/v1/admin/peers", nil)
		if resp.StatusCode != 200 {
			t.Fatalf("copied operator credential: %d %s", resp.StatusCode, body)
		}
	}
}
