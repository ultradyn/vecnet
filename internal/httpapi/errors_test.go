package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
)

func TestWriteErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
		path   any // expected details.path, nil = not checked
	}{
		{"api", hub.E(http.StatusConflict, "idempotency_conflict", "key reused"), 409, "idempotency_conflict", nil},
		{"wrapped api", fmt.Errorf("send: %w", hub.E(422, "as_required", "need as")), 422, "as_required", nil},
		{"schema", &envelope.SchemaError{Path: "/priority", Message: "missing"}, 400, "invalid_body", "/priority"},
		{"unauth", fmt.Errorf("x: %w", auth.ErrUnauthenticated), 401, "unauthenticated", nil},
		{"maxbytes", &http.MaxBytesError{Limit: 1}, 413, "payload_too_large", nil},
		{"other", errors.New("db exploded: secret-ish detail"), 500, "internal", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeError(rec, tc.err)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			var got struct {
				Error struct {
					Code    string         `json:"code"`
					Message string         `json:"message"`
					Details map[string]any `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode: %v; %s", err, rec.Body)
			}
			if got.Error.Code != tc.code {
				t.Fatalf("code = %q, want %q", got.Error.Code, tc.code)
			}
			if got.Error.Details == nil {
				t.Fatalf("details missing or null: %s", rec.Body)
			}
			if tc.path != nil && got.Error.Details["path"] != tc.path {
				t.Fatalf("details.path = %v, want %v", got.Error.Details["path"], tc.path)
			}
			if tc.code == "internal" && got.Error.Message != "internal" {
				t.Fatalf("500 message = %q, want \"internal\" (no internals leaked)", got.Error.Message)
			}
		})
	}
}
