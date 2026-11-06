package httpapi_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// PROTOCOL §15: a body that is not JSON or violates the shape (unknown field,
// wrong type, missing required field) is 400 invalid_body with details.path at
// the first violation. Semantic value errors stay 422 invalid_value.
func TestR2HTTPBodyShapeErrors(t *testing.T) {
	f := newR2HTTP(t)
	_, secret := f.env.NewPeer("session", "shape")
	g := f.upload(t, secret, "shape")
	sha := g.SHA256
	n := 0
	post := func(t *testing.T, path, body string) (int, []byte) {
		t.Helper()
		n++
		req, _ := http.NewRequest("POST", f.env.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "shape-"+strconv.Itoa(n))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal("hub request failed")
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal("hub response failed")
		}
		return resp.StatusCode, raw
	}
	cases := []struct {
		name, endpoint, body, path string
	}{
		{"missing size", "/v1/blobs/uploads", `{"sha256":"` + sha + `","media_type":"text/plain"}`, "/size"},
		{"missing sha256", "/v1/blobs/uploads", `{"size":3,"media_type":"text/plain"}`, "/sha256"},
		{"missing media_type", "/v1/blobs/uploads", `{"size":3,"sha256":"` + sha + `"}`, "/media_type"},
		{"null size", "/v1/blobs/uploads", `{"size":null,"sha256":"` + sha + `","media_type":"text/plain"}`, "/size"},
		{"string size", "/v1/blobs/uploads", `{"size":"3","sha256":"` + sha + `","media_type":"text/plain"}`, "/size"},
		{"fractional size", "/v1/blobs/uploads", `{"size":3.5,"sha256":"` + sha + `","media_type":"text/plain"}`, "/size"},
		{"null sha256", "/v1/blobs/uploads", `{"size":3,"sha256":null,"media_type":"text/plain"}`, "/sha256"},
		{"numeric media_type", "/v1/blobs/uploads", `{"size":3,"sha256":"` + sha + `","media_type":7}`, "/media_type"},
		{"case variant", "/v1/blobs/uploads", `{"Size":3,"sha256":"` + sha + `","media_type":"text/plain"}`, "/Size"},
		{"case variant sha", "/v1/blobs/uploads", `{"size":3,"SHA256":"` + sha + `","media_type":"text/plain"}`, "/SHA256"},
		{"parked md5", "/v1/blobs/uploads", `{"size":3,"sha256":"` + sha + `","media_type":"text/plain","content_md5":"x"}`, "/content_md5"},
		{"array", "/v1/blobs/uploads", `[]`, ""},
		{"null grant", "/v1/blobs/uploads", `null`, ""},
		{"trailing", "/v1/blobs/uploads", `{"size":3,"sha256":"` + sha + `","media_type":"text/plain"} {}`, ""},
		{"not json", "/v1/blobs/uploads", `{`, ""},
		{"completion field", g.CompleteURL, `{"url":"https://foreign.invalid"}`, "/url"},
		{"completion null", g.CompleteURL, `null`, ""},
		{"completion array", g.CompleteURL, `[]`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, raw := post(t, c.endpoint, c.body)
			if status != 400 {
				t.Fatalf("status %d, want 400", status)
			}
			var e struct {
				Error struct {
					Code    string          `json:"code"`
					Details json.RawMessage `json:"details"`
				} `json:"error"`
			}
			if json.Unmarshal(raw, &e) != nil || e.Error.Code != "invalid_body" {
				t.Fatal("expected invalid_body")
			}
			var details map[string]any
			if json.Unmarshal(e.Error.Details, &details) != nil {
				t.Fatal("invalid_body lacks details")
			}
			if path, ok := details["path"].(string); !ok || path != c.path {
				t.Fatalf("details.path = %v, want %q", details["path"], c.path)
			}
		})
	}
	// A well-formed body with an invalid value is semantic, not a shape error.
	status, raw := post(t, "/v1/blobs/uploads", `{"size":0,"sha256":"`+sha+`","media_type":"text/plain"}`)
	if status != 422 {
		t.Fatalf("zero size status %d, want 422", status)
	}
	requireCode(t, raw, "invalid_value")
}
