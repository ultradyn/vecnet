package httpapi_test

import (
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/httpapi"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/hubid"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestRelocatedAnswers410(t *testing.T) {
	e := testhub.Start(t)
	_, peer := e.NewPeer("session", "reader")
	n := e.Relocate(t, "https://next.example")
	for _, tc := range []struct{ method, path, accept string }{
		{"POST", "/v1/messages", ""}, {"GET", "/v1/events", "text/event-stream"},
		{"GET", "/v1/events?wait=0", "application/json"}, {"POST", "/v1/ack", ""},
		{"GET", "/v1/threads", ""}, {"POST", "/mcp", ""}, {"GET", "/oauth/authorize", ""},
		{"POST", "/oauth/token", ""}, {"GET", "/.well-known/oauth-authorization-server", ""},
		{"GET", "/v1/peers/me", ""}, {"POST", "/v1/admin/notices", ""},
		{"POST", "/v1/hub/identity", ""}, {"POST", "/healthz", ""}, {"GET", "/unknown", ""},
	} {
		for _, credential := range []struct{ name, secret string }{
			{"anonymous", ""}, {"valid", peer}, {"invalid", "invalid"},
		} {
			t.Run(tc.method+tc.path+tc.accept+"/"+credential.name, func(t *testing.T) {
				req, err := http.NewRequest(tc.method, e.URL+tc.path, strings.NewReader("not JSON"))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Accept", tc.accept)
				if credential.secret != "" {
					req.Header.Set("Authorization", "Bearer "+credential.secret)
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				var got struct {
					Error struct {
						Code, Message string
						Details       struct {
							NewURL string `json:"new_url"`
							Notice envelope.HubNotice
						}
					}
				}
				if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
					t.Fatal(err)
				}
				if resp.StatusCode != 410 || got.Error.Code != "hub_moved" || got.Error.Message != "this hub moved to "+n.NewURL || got.Error.Details.NewURL != n.NewURL || got.Error.Details.Notice != n {
					t.Fatalf("%d %+v", resp.StatusCode, got)
				}
				if err := hubid.VerifyNotice(e.Hub.IdentityKey().Pubkey(), got.Error.Details.Notice); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestRelocatedServesIdentityAndNotices(t *testing.T) {
	e := testhub.Start(t)
	_, peer := e.NewPeer("session", "reader")
	n := e.Relocate(t, "https://next.example")
	resp, raw := e.Do("", "GET", "/healthz", nil)
	if resp.StatusCode != 200 || string(raw) != "{\"ok\":true}\n" {
		t.Fatalf("health: %d %s", resp.StatusCode, raw)
	}
	challenge := identityChallenge()
	doc := identityDoc(t, e, "?challenge="+challenge)
	if err := hubid.VerifyChallenge(doc.Pubkey, doc.PublicURL, challenge, doc.ChallengeSig); err != nil {
		t.Fatal(err)
	}
	resp, raw = e.Do(peer, "GET", "/v1/hub/notices", nil)
	var page struct{ Notices []envelope.HubNotice }
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || len(page.Notices) != 1 || page.Notices[0] != n {
		t.Fatalf("notices: %d %s", resp.StatusCode, raw)
	}
	resp, raw = e.Do("", "GET", "/v1/hub/notices", nil)
	mustErr(t, resp, raw, 401, "unauthenticated")
}

func TestRelocatedUIPage(t *testing.T) {
	e := testhub.Start(t)
	// Exercise escaping even for a directly supplied hostile notice. Production
	// notices have normalized URLs, but the handler must always escape its input.
	for _, newURL := range []string{"https://next.example", `https://next.example/\"><script>alert(1)</script>&`} {
		n := envelope.HubNotice{NewURL: newURL}
		handler := httpapi.Relocated(httpapi.Deps{Hub: e.Hub}, n)
		for _, path := range []string{"/ui", "/ui/", "/ui/assets/app.js"} {
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
			body := rr.Body.String()
			if rr.Code != 410 || !strings.HasPrefix(rr.Header().Get("Content-Type"), "text/html") || !strings.Contains(body, `<a href="`+html.EscapeString(newURL)+`">`) || strings.Contains(strings.ToLower(body), "<script") || !strings.Contains(strings.ToLower(body), "moved") {
				t.Fatalf("UI %s: %d %s", path, rr.Code, body)
			}
			if rr.Header().Get("Content-Security-Policy") != "default-src 'none'; frame-ancestors 'none'" || rr.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("UI %s headers: %v", path, rr.Header())
			}
		}
	}
}

func TestRelocatedReusesActiveNotice(t *testing.T) {
	e := testhub.Start(t)
	at := envelope.FormatTime(e.Now.Now().Add(time.Hour))
	want := httpNotice(t, e, map[string]string{"kind": "moving", "message": "planned move", "new_url": "https://next.example", "at": at})
	got := e.Relocate(t, "HTTPS://NEXT.EXAMPLE:443/")
	if got != want {
		t.Fatalf("replaced active notice: got %+v want %+v", got, want)
	}
	resp, raw := e.Do(e.OperatorSecret, "GET", "/v1/hub/notices", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(raw), want.NoticeID) || !strings.Contains(string(raw), at) {
		t.Fatalf("active notice: %d %s", resp.StatusCode, raw)
	}
}

func TestRelocatedMiddleware(t *testing.T) {
	e := testhub.StartWith(t, func(c *hub.Config) { c.PublicURL = "https://old.example" })
	_, peer := e.NewPeer("session", "reader")
	n := e.Relocate(t, "https://next.example")
	lim := httpapi.NewLimiter(1, time.Hour)
	handler := httpapi.Relocated(httpapi.Deps{Hub: e.Hub, Limiter: lim}, n)
	for i, want := range []int{200, 429} {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest("GET", "/v1/hub/identity?challenge="+identityChallenge(), nil))
		if rr.Code != want || rr.Header().Get("Strict-Transport-Security") != "max-age=31536000" {
			t.Fatalf("request %d: %d %v", i, rr.Code, rr.Header())
		}
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/hub/notices", nil)
	req.Header.Set("Authorization", "Bearer "+peer)
	handler.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("valid bearer was throttled: %d %s", rr.Code, rr.Body.String())
	}
}
