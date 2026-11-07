package oauth_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/blob"
	"github.com/XertroV/vecnet/internal/blobwire"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/oauth"
	"github.com/XertroV/vecnet/internal/testhub"
)

// All provider responses are synthesized; this transport never opens a socket.
// Uploads seed metadata in memory instead of sending file bytes anywhere.
type sessionOAuthR2 struct {
	mu   sync.Mutex
	head http.Header
}

func (f *sessionOAuthR2) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodHead || r.URL.Host != "00000000000000000000000000000000.r2.cloudflarestorage.com" {
		return nil, fmt.Errorf("unexpected fake provider request")
	}
	f.mu.Lock()
	head := f.head.Clone()
	f.mu.Unlock()
	if head == nil {
		return nil, fmt.Errorf("missing fake object metadata")
	}
	return &http.Response{StatusCode: http.StatusOK, Header: head, Body: http.NoBody, Request: r}, nil
}

func legacyOAuthTransferCall(t *testing.T, env *testhub.Env, token, sid, name string, args any) (json.RawMessage, bool) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, env.URL+"/mcp", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Mcp-Session-Id", sid)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("legacy transfer HTTP status %d", resp.StatusCode)
	}
	var reply struct {
		Error  json.RawMessage `json:"error"`
		Result struct {
			IsError           bool            `json:"isError"`
			StructuredContent json.RawMessage `json:"structuredContent"`
			Content           []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		t.Fatal("invalid legacy tool response")
	}
	if len(reply.Error) != 0 {
		t.Fatal("legacy transfer returned RPC error")
	}
	if len(reply.Result.StructuredContent) != 0 {
		return reply.Result.StructuredContent, reply.Result.IsError
	}
	if len(reply.Result.Content) != 1 {
		t.Fatal("legacy tool response missing content")
	}
	return json.RawMessage(reply.Result.Content[0].Text), reply.Result.IsError
}

func TestOAuthRefreshedSessionUsesCurrentTransferCredential(t *testing.T) {
	for _, change := range []string{"initializer_revoked", "initializer_expired", "current_revoked_during_signing"} {
		t.Run(change, func(t *testing.T) {
			provider := &sessionOAuthR2{}
			env := testhub.StartWith(t, func(cfg *hub.Config) {
				cfg.BlobBackend = "r2"
				cfg.R2 = hub.R2Config{AccountID: "00000000000000000000000000000000", Bucket: "test-bucket", ChecksumProfile: "sha256", StorageQuotaBytes: 100, AccessKeyID: "FAKEACCESS", SecretAccessKey: "fake-signing-secret", Client: &http.Client{Transport: provider}}
			})
			b, _, initial := login(t, env)
			resp, _ := mcpInitialize(t, env, initial.AccessToken)
			sid := resp.Header.Get("Mcp-Session-Id")
			if sid == "" {
				t.Fatal("missing legacy session")
			}
			current := b.refresh(initial.RefreshToken)
			if current.status != http.StatusOK {
				t.Fatal("OAuth refresh failed")
			}
			if change == "initializer_revoked" {
				if _, err := env.Hub.DB.Exec(`UPDATE oauth_tokens SET revoked_at=? WHERE token_hash=?`, envelope.FormatTime(env.Now.Now()), auth.HashSecret(initial.AccessToken)); err != nil {
					t.Fatal(err)
				}
			}
			if change == "initializer_expired" {
				if _, err := env.Hub.DB.Exec(`UPDATE oauth_tokens SET expires_at=? WHERE token_hash=?`, envelope.FormatTime(env.Now.Now()), auth.HashSecret(initial.AccessToken)); err != nil {
					t.Fatal(err)
				}
			}
			sum := sha256.Sum256([]byte("abc"))
			digest := hex.EncodeToString(sum[:])
			raw, failed := legacyOAuthTransferCall(t, env, current.AccessToken, sid, "vecnet_blob_put", map[string]any{"phase": "grant", "size": 3, "sha256": digest, "media_type": "text/plain", "idempotency_key": "refreshed-session"})
			if failed {
				t.Fatal("refreshed session used initialization credential for upload")
			}
			var grant blobwire.UploadGrant
			if err := json.Unmarshal(raw, &grant); err != nil || grant.BlobID == "" || grant.Upload.URL == "" {
				t.Fatal("missing upload grant")
			}
			head := http.Header{}
			for k, v := range grant.Upload.Headers {
				head.Set(k, v)
			}
			head.Set("ETag", `"fake-etag"`)
			provider.mu.Lock()
			provider.head = head
			provider.mu.Unlock()
			_, failed = legacyOAuthTransferCall(t, env, current.AccessToken, sid, "vecnet_blob_put", map[string]any{"phase": "complete", "blob_id": grant.BlobID})
			if failed {
				t.Fatal("refreshed session used initialization credential for completion")
			}
			if change == "current_revoked_during_signing" {
				env.Hub.BlobSignDownload = func(ctx context.Context, _ blob.R2Object) (blobwire.Request, error) {
					_, err := env.Hub.DB.ExecContext(ctx, `UPDATE oauth_tokens SET revoked_at=? WHERE token_hash=?`, envelope.FormatTime(env.Now.Now()), auth.HashSecret(current.AccessToken))
					return blobwire.Request{Method: "GET", URL: "https://fake.invalid/capability", ExpiresAt: env.Now.Now().Add(time.Minute).Format(time.RFC3339Nano)}, err
				}
			}
			raw, failed = legacyOAuthTransferCall(t, env, current.AccessToken, sid, "vecnet_blob_get", map[string]any{"sha256": digest, "blob_id": grant.BlobID})
			if change != "current_revoked_during_signing" {
				var download blobwire.DownloadGrant
				if failed || json.Unmarshal(raw, &download) != nil || download.URL == "" {
					t.Fatal("active refreshed token could not download through its original session")
				}
			} else {
				if !failed || bytes.Contains(raw, []byte("https://")) {
					t.Fatal("revoked current credential escaped transfer authorization")
				}
				var refusal struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				if json.Unmarshal(raw, &refusal) != nil || refusal.Error.Code != "forbidden" {
					t.Fatal("current-token revocation must fail with forbidden")
				}
				var issued int
				if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM blob_usage_events WHERE kind='download_grant_issued'`).Scan(&issued); err != nil || issued != 0 {
					t.Fatal("denied current-token mint recorded as issued")
				}
				// The initialization token remains live: using it accidentally
				// would have authorized and exposed the denied capability.
				req, err := http.NewRequest(http.MethodPost, env.URL+"/mcp", nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer "+initial.AccessToken)
				if _, err := (&oauth.TokenAuth{S: env.OAuth}).Authenticate(context.Background(), req); err != nil {
					t.Fatal("initialization token should remain active in this case")
				}
			}
		})
	}
}
