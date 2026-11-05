package httpapi_test

import (
	"context"
	"encoding/json"
	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/httpapi"
	"github.com/XertroV/vecnet/internal/testhub"
	"io"
	"net/http/httptest"
	"testing"
)

type unreadR2Body struct{ t *testing.T }

func (b unreadR2Body) Read([]byte) (int, error) {
	b.t.Error("R2 legacy body was read")
	return 0, io.EOF
}
func (b unreadR2Body) Close() error { return nil }
func TestR2RejectsLegacyBodyBeforeRead(t *testing.T) {
	env := testhub.Start(t)
	env.Hub.Cfg.BlobBackend = "r2"
	req := httptest.NewRequest("PUT", "/v1/blobs", nil)
	req.Body = unreadR2Body{t}
	req.Header.Set("Authorization", "Bearer "+env.OperatorSecret)
	req.Header.Set("Expect", "100-continue")
	rec := httptest.NewRecorder()
	httpapi.New(httpapi.Deps{Hub: env.Hub}).ServeHTTP(rec, req)
	if rec.Code != 409 {
		t.Fatalf("status %d, expected direct upload refusal", rec.Code)
	}
	requireCode(t, rec.Body.Bytes(), "direct_upload_required")
	c, err := env.Hub.Auth.Resolve(context.Background(), env.OperatorSecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = env.Hub.PutBlob(context.Background(), auth.Caller(c), unreadR2Body{t}, "text/plain", 0); err == nil {
		t.Fatal("direct hub PUT accepted R2 bytes")
	}
}

func requireCode(t *testing.T, raw []byte, code string) {
	t.Helper()
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) != nil || e.Error.Code != code {
		t.Fatalf("expected error code %s", code)
	}
}
