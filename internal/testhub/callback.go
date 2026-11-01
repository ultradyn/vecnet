package testhub

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/XertroV/vecnet/internal/webhook"
)

// CallbackPost is one event POST an MCP Events Callback received.
type CallbackPost struct {
	ID, Timestamp, Signature, Subscription string
	Body                                   []byte
}

// Callback is an MCP Events webhook receiver on 127.0.0.1 (PROTOCOL §4.4). It
// echoes verification challenges and answers event POSTs 200 unless told
// otherwise; it records both.
type Callback struct {
	URL string

	mu          sync.Mutex
	posts       []CallbackPost
	challenges  []CallbackPost
	onPost      func(n int, w http.ResponseWriter)
	onChallenge func(challenge string, w http.ResponseWriter, r *http.Request)
}

// NewCallback starts a Callback; it stops at test end.
func NewCallback(t *testing.T) *Callback {
	t.Helper()
	c := &Callback{}
	srv := httptest.NewServer(http.HandlerFunc(c.serve))
	t.Cleanup(srv.Close)
	c.URL = srv.URL + "/hook/path-with-token"
	return c
}

// OnPost makes the n-th event POST (1-based, counting every POST received)
// answer through f; nil restores the plain 200.
func (c *Callback) OnPost(f func(n int, w http.ResponseWriter)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onPost = f
}

// OnChallenge replaces the echo of a verification challenge with f.
func (c *Callback) OnChallenge(f func(challenge string, w http.ResponseWriter, r *http.Request)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onChallenge = f
}

// Posts returns the event POSTs received so far.
func (c *Callback) Posts() []CallbackPost {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]CallbackPost(nil), c.posts...)
}

// Challenges returns the verification POSTs received so far.
func (c *Callback) Challenges() []CallbackPost {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]CallbackPost(nil), c.challenges...)
}

// Requests counts every request received, of either kind.
func (c *Callback) Requests() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.posts) + len(c.challenges)
}

func (c *Callback) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	post := CallbackPost{
		ID:           r.Header.Get(webhook.HeaderID),
		Timestamp:    r.Header.Get(webhook.HeaderTimestamp),
		Signature:    r.Header.Get(webhook.HeaderSignature),
		Subscription: r.Header.Get("X-MCP-Subscription-Id"),
		Body:         body,
	}
	var head struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
	}
	_ = json.Unmarshal(body, &head)
	if head.Type == "verification" {
		c.mu.Lock()
		c.challenges = append(c.challenges, post)
		f := c.onChallenge
		c.mu.Unlock()
		if f != nil {
			f(head.Challenge, w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"challenge": head.Challenge})
		return
	}
	c.mu.Lock()
	c.posts = append(c.posts, post)
	n := len(c.posts)
	f := c.onPost
	c.mu.Unlock()
	if f != nil {
		f(n, w)
	}
}
