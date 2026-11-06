package blobclient

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestHubMovingNoticeIsNarrowAndNeverRendered(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
		want   bool
	}{
		{410, "hub_moved", true}, {503, "hub_moved", false}, {410, "blob_purged", false},
	} {
		t.Run(tc.code+http.StatusText(tc.status), func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"error": map[string]any{"code": tc.code, "message": "private-provider-text", "details": map[string]any{"notice": map[string]any{"notice_id": "notice", "kind": "moving", "sig": "fake-signature", "new_url": "https://new.invalid", "extra": "private-provider-text"}, "authorization": "private-provider-text"}}})
			err := responseError(&http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))})
			var e *Error
			if !errors.As(err, &e) || (e.MovingNotice != nil) != tc.want {
				t.Fatal("lost or over-broad relocation metadata")
			}
			if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "fake-signature") || strings.Contains(err.Error(), "https:") {
				t.Fatal("error rendered sensitive metadata")
			}
			if tc.want {
				raw, _ := json.Marshal(e.MovingNotice)
				if strings.Contains(string(raw), "extra") || strings.Contains(string(raw), "private") {
					t.Fatal("arbitrary hub details propagated")
				}
			}
		})
	}
}
