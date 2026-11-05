package hub_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/XertroV/vecnet/internal/blob"
	"github.com/XertroV/vecnet/internal/blobwire"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
)

func shaHex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// putBlob uploads content as secret and returns its sha256.
func putBlob(t *testing.T, env *testhub.Env, secret, content string) string {
	t.Helper()
	resp, raw := env.Do(secret, "PUT", "/v1/blobs", content)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT /v1/blobs = %d; %s", resp.StatusCode, raw)
	}
	var pr struct {
		SHA256 string `json:"sha256"`
	}
	if err := json.Unmarshal(raw, &pr); err != nil {
		t.Fatal(err)
	}
	return pr.SHA256
}

// onlyDelivery returns the single delivery Message in peer's log.
func onlyDelivery(t *testing.T, env *testhub.Env, peer string) envelope.Message {
	t.Helper()
	evs := deliveryEvents(t, env, peer)
	if len(evs) != 1 {
		t.Fatalf("%s has %d delivery events, want 1", peer, len(evs))
	}
	_, _, m := decodeDelivery(t, evs[0])
	return m
}

// blobToken splits a blob_url into its sha256 path segment and ?t= token.
func blobToken(t *testing.T, env *testhub.Env, blobURL string) (sha, tok string) {
	t.Helper()
	if !strings.HasPrefix(blobURL, env.URL+"/v1/blobs/") {
		t.Fatalf("blob_url %q does not start with %s/v1/blobs/", blobURL, env.URL)
	}
	u, err := url.Parse(blobURL)
	if err != nil {
		t.Fatalf("parse blob_url: %v", err)
	}
	return strings.TrimPrefix(u.Path, "/v1/blobs/"), u.Query().Get("t")
}

func storedBody(t *testing.T, env *testhub.Env, msgID string) string {
	t.Helper()
	var body string
	if err := env.Hub.DB.QueryRow(`SELECT body FROM messages WHERE msg_id = ?`, msgID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestSpillPreviewRuneBoundary(t *testing.T) {
	text := strings.Repeat("é", 2050) // 4100 bytes
	if len(text) != 4100 {
		t.Fatalf("setup: %d bytes", len(text))
	}
	p := hub.SpillPreview(text, 4096)
	if len(p) > 4096 {
		t.Fatalf("preview is %d bytes, want ≤ 4096", len(p))
	}
	if !utf8.ValidString(p) {
		t.Fatalf("preview is not valid UTF-8")
	}
	if len(p)%2 != 0 {
		t.Fatalf("preview is %d bytes, want an even length (whole 2-byte runes)", len(p))
	}
	if !strings.HasPrefix(text, p) || len(p) < 4096-256 {
		t.Fatalf("preview of %d bytes is not a long prefix of the text", len(p))
	}

	// An odd cap lands mid-rune: the cut steps back to the boundary.
	if p := hub.SpillPreview(text, 4095); len(p) != 4094 || !utf8.ValidString(p) {
		t.Fatalf("cap 4095: preview %d bytes valid=%v, want 4094 valid", len(p), utf8.ValidString(p))
	}
	// 3- and 4-byte runes across the boundary.
	for _, r := range []string{"€", "😀"} {
		s := strings.Repeat(r, 2000)
		p := hub.SpillPreview(s, 4096)
		if len(p) > 4096 || !utf8.ValidString(p) || len(p)%len(r) != 0 || !strings.HasPrefix(s, p) {
			t.Fatalf("%q: preview %d bytes valid=%v", r, len(p), utf8.ValidString(p))
		}
	}
	// A short text is returned unchanged.
	if p := hub.SpillPreview("short", 4096); p != "short" {
		t.Fatalf("short preview = %q", p)
	}
	// A separator in the final 256 bytes is preferred to a mid-word cut.
	words := strings.Repeat("word ", 1000)
	p = hub.SpillPreview(words, 4096)
	if len(p) > 4096 || !strings.HasPrefix(words, p) || strings.HasSuffix(p, "wo") || len(p) < 4096-256 {
		t.Fatalf("word preview ends %q (%d bytes), want a cut at a space", p[len(p)-8:], len(p))
	}
	lines := strings.Repeat("line of text\n", 400)
	p = hub.SpillPreview(lines, 4096)
	if len(p) > 4096 || !strings.HasPrefix(lines, p) || !strings.HasSuffix(p, "line of text") {
		t.Fatalf("line preview ends %q (%d bytes), want a cut at a newline", p[len(p)-8:], len(p))
	}
}

func TestSendSpills(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")

	line := "spill line é€ of the long message\n"
	full := strings.Repeat(line, 12*1024/len(line)+1)
	if len(full) <= 12*1024 {
		t.Fatalf("setup: %d bytes", len(full))
	}
	body := req("k1", b)
	body["content"] = map[string]any{"text": full}
	sr := mustSend(t, env, aSec, body)
	if !sr.Spilled {
		t.Fatalf("response.spilled = false, want true")
	}

	m := onlyDelivery(t, env, b)
	if !m.Content.Truncated {
		t.Fatalf("content.truncated = false, want true")
	}
	if len(m.Content.Text) > 4096 || !utf8.ValidString(m.Content.Text) || !strings.HasPrefix(full, m.Content.Text) {
		t.Fatalf("content.text is %d bytes (valid=%v), want a ≤ 4096-byte prefix", len(m.Content.Text), utf8.ValidString(m.Content.Text))
	}
	if len(m.Attachments) != 1 {
		t.Fatalf("attachments = %+v, want 1", m.Attachments)
	}
	att := m.Attachments[0]
	if att.MediaType != "text/markdown" || att.SHA256 != shaHex(full) || att.Size != int64(len(full)) || att.Name != "message.md" {
		t.Fatalf("spill attachment = %+v", att)
	}
	if !strings.Contains(att.BlobURL, "?t=rt_") {
		t.Fatalf("blob_url = %q, want it to carry ?t=rt_", att.BlobURL)
	}
	if v, ok := m.Metadata["vecnet.spilled"]; !ok || v != true {
		t.Fatalf(`metadata["vecnet.spilled"] = %v (%v), want true`, v, ok)
	}

	resp, got := env.Do("", "GET", strings.TrimPrefix(att.BlobURL, env.URL), nil)
	if resp.StatusCode != http.StatusOK || string(got) != full {
		t.Fatalf("GET blob_url = %d, %d bytes; want 200 with the full text (%d bytes)", resp.StatusCode, len(got), len(full))
	}

	// The stored Message has the attachment but never a blob_url, and the
	// spill blob is pinned to it.
	stored := storedBody(t, env, sr.MessageID)
	if strings.Contains(stored, "blob_url") || !strings.Contains(stored, att.SHA256) {
		t.Fatalf("stored message: blob_url present or attachment missing: %s", stored)
	}
	if n := count(t, env, `SELECT count(*) FROM blob_refs WHERE sha256 = ? AND msg_id = ?`, att.SHA256, sr.MessageID); n != 1 {
		t.Fatalf("blob_refs for the spill = %d, want 1", n)
	}

	// A text at exactly the cap does not spill.
	body = req("k2", b)
	body["content"] = map[string]any{"text": strings.Repeat("x", 4096)}
	if sr := mustSend(t, env, aSec, body); sr.Spilled {
		t.Fatalf("4096-byte text spilled")
	}
}

func TestUnknownBlobRejected(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	missing := shaHex("never uploaded")

	body := req("k1", b)
	body["attachments"] = []any{map[string]any{"sha256": missing, "size": 14, "media_type": "text/plain"}}
	mustReject(t, env, aSec, body, http.StatusUnprocessableEntity, "unknown_blob")
	if n := count(t, env, `SELECT count(*) FROM messages`); n != 0 {
		t.Fatalf("messages = %d after a rejected send, want 0", n)
	}

	body = req("k2", b)
	body["attachments"] = []any{map[string]any{"sha256": missing, "size": 14, "media_type": "text/plain",
		"origin_url": "https://example.com/report.txt"}}
	sr := mustSend(t, env, aSec, body)
	m := onlyDelivery(t, env, b)
	if len(m.Attachments) != 1 || m.Attachments[0].BlobURL != "" || m.Attachments[0].OriginURL != "https://example.com/report.txt" {
		t.Fatalf("reference-only attachment = %+v, want origin_url and no blob_url", m.Attachments)
	}
	if n := count(t, env, `SELECT count(*) FROM blob_refs WHERE msg_id = ?`, sr.MessageID); n != 0 {
		t.Fatalf("blob_refs for a reference-only attachment = %d, want 0", n)
	}

	// A hosted attachment whose declared size disagrees with the stored bytes.
	sha := putBlob(t, env, aSec, "hosted bytes")
	body = req("k3", b)
	body["attachments"] = []any{map[string]any{"sha256": sha, "size": 999, "media_type": "text/plain"}}
	mustReject(t, env, aSec, body, http.StatusUnprocessableEntity, "attachment_size_mismatch")

	// Hosted bytes that also name an origin_url are still hosted: pinned
	// and linked.
	body = req("k4", b)
	body["attachments"] = []any{map[string]any{"sha256": sha, "size": len("hosted bytes"), "media_type": "text/plain",
		"origin_url": "https://example.com/hosted.txt"}}
	sr = mustSend(t, env, aSec, body)
	evs := deliveryEvents(t, env, b)
	_, _, m = decodeDelivery(t, evs[len(evs)-1])
	if len(m.Attachments) != 1 || !strings.Contains(m.Attachments[0].BlobURL, "?t=rt_") {
		t.Fatalf("hosted attachment with origin_url = %+v, want a blob_url", m.Attachments)
	}
	if n := count(t, env, `SELECT count(*) FROM blob_refs WHERE sha256 = ? AND msg_id = ?`, sha, sr.MessageID); n != 1 {
		t.Fatalf("blob_refs for the hosted attachment = %d, want 1", n)
	}
}

func TestBlobURLScopedPerRecipient(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	c, _ := env.NewPeer("session", "C")
	content := "shared attachment bytes"
	sha := putBlob(t, env, aSec, content)

	body := req("k1", b, c)
	body["attachments"] = []any{map[string]any{"sha256": sha, "size": len(content), "media_type": "text/plain", "name": "a.txt"}}
	sr := mustSend(t, env, aSec, body)

	mb, mc := onlyDelivery(t, env, b), onlyDelivery(t, env, c)
	if len(mb.Attachments) != 1 || len(mc.Attachments) != 1 {
		t.Fatalf("attachments: B %+v, C %+v", mb.Attachments, mc.Attachments)
	}
	shaB, tokB := blobToken(t, env, mb.Attachments[0].BlobURL)
	shaC, tokC := blobToken(t, env, mc.Attachments[0].BlobURL)
	if shaB != sha || shaC != sha {
		t.Fatalf("blob_url hashes B %q C %q, want %q", shaB, shaC, sha)
	}
	if tokB == "" || tokC == "" || tokB == tokC {
		t.Fatalf("tokens B %q C %q: want two different tokens", tokB, tokC)
	}
	now := env.Now.Now()
	if who, ok := env.Hub.Blobs.VerifyToken(tokB, sha, now); !ok || who != b || who == c {
		t.Fatalf("VerifyToken(B's token) = %q, %v; want B %q, not C", who, ok, b)
	}
	if who, ok := env.Hub.Blobs.VerifyToken(tokC, sha, now); !ok || who != c {
		t.Fatalf("VerifyToken(C's token) = %q, %v; want C %q", who, ok, c)
	}
	// Valid for 7 days, not longer.
	if _, ok := env.Hub.Blobs.VerifyToken(tokB, sha, now.Add(7*24*60*60e9-60e9)); !ok {
		t.Fatalf("B's token expired before 7 days")
	}
	if _, ok := env.Hub.Blobs.VerifyToken(tokB, sha, now.Add(7*24*60*60e9+60e9)); ok {
		t.Fatalf("B's token still valid after 7 days")
	}
	for _, u := range []string{mb.Attachments[0].BlobURL, mc.Attachments[0].BlobURL} {
		resp, got := env.Do("", "GET", strings.TrimPrefix(u, env.URL), nil)
		if resp.StatusCode != http.StatusOK || string(got) != content {
			t.Fatalf("GET %s = %d %q", u, resp.StatusCode, got)
		}
	}
	if strings.Contains(storedBody(t, env, sr.MessageID), "blob_url") {
		t.Fatalf("stored message carries a blob_url")
	}
}

func TestPayloadCaps(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")

	body := req("k1", b)
	body["content"] = map[string]any{"text": strings.Repeat("x", 70*1024)}
	_, d := mustReject(t, env, aSec, body, http.StatusRequestEntityTooLarge, "payload_too_large")
	if d["field"] != "content.text" {
		t.Fatalf("details.field = %v, want content.text", d["field"])
	}
	// 65536 bytes of 2-byte runes is only 32768 characters: the byte cap,
	// not the schema's character cap, decides.
	body = req("k2", b)
	body["content"] = map[string]any{"text": strings.Repeat("é", 32769)}
	_, d = mustReject(t, env, aSec, body, http.StatusRequestEntityTooLarge, "payload_too_large")
	if d["field"] != "content.text" {
		t.Fatalf("details.field = %v, want content.text", d["field"])
	}

	body = req("k3", b)
	body["metadata"] = map[string]any{"blob": strings.Repeat("m", 9*1024)}
	_, d = mustReject(t, env, aSec, body, http.StatusRequestEntityTooLarge, "payload_too_large")
	if d["field"] != "metadata" {
		t.Fatalf("details.field = %v, want metadata", d["field"])
	}

	body = req("k4", b)
	body["job_request"] = map[string]any{"version": 1, "job_key": "j1", "operation": "op", "instructions": "do it",
		"input": map[string]any{"data": strings.Repeat("i", 17*1024)}}
	_, d = mustReject(t, env, aSec, body, http.StatusRequestEntityTooLarge, "payload_too_large")
	if d["field"] != "job_request.input" {
		t.Fatalf("details.field = %v, want job_request.input", d["field"])
	}

	body = req("k5", b)
	body["kind"] = "job_status"
	body["job_status"] = map[string]any{"job_id": "job_01HZZZZZZZZZZZZZZZZZZZZZZZ", "sequence": 1, "state": "running",
		"output": strings.Repeat("o", 9*1024)}
	_, d = mustReject(t, env, aSec, body, http.StatusRequestEntityTooLarge, "payload_too_large")
	if d["field"] != "job_status.output" {
		t.Fatalf("details.field = %v, want job_status.output", d["field"])
	}

	if n := count(t, env, `SELECT count(*) FROM messages`); n != 0 {
		t.Fatalf("messages = %d after rejected sends, want 0", n)
	}
	// Just under every cap is accepted.
	body = req("k6", b)
	body["content"] = map[string]any{"text": strings.Repeat("x", 65536)}
	body["metadata"] = map[string]any{"blob": strings.Repeat("m", 8000)}
	if sr := mustSend(t, env, aSec, body); !sr.Spilled {
		t.Fatalf("65536-byte text did not spill")
	}
}

// A spill adds one attachment and one metadata key; a Message with no room
// for them is refused rather than stored schema-invalid. A replayed spilled
// send returns the original response and stores nothing new.
func TestSpillNeedsRoomAndReplays(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	long := strings.Repeat("y", 5000)

	sha := putBlob(t, env, aSec, "one")
	var atts []any
	for range 16 {
		atts = append(atts, map[string]any{"sha256": sha, "size": 3, "media_type": "text/plain"})
	}
	body := req("k1", b)
	body["content"] = map[string]any{"text": long}
	body["attachments"] = atts
	_, d := mustReject(t, env, aSec, body, http.StatusRequestEntityTooLarge, "payload_too_large")
	if d["field"] != "attachments" {
		t.Fatalf("details.field = %v, want attachments", d["field"])
	}

	md := map[string]any{}
	for i := range 32 {
		md[strings.Repeat("k", i+1)] = i
	}
	body = req("k2", b)
	body["content"] = map[string]any{"text": long}
	body["metadata"] = md
	_, d = mustReject(t, env, aSec, body, http.StatusRequestEntityTooLarge, "payload_too_large")
	if d["field"] != "metadata" {
		t.Fatalf("details.field = %v, want metadata", d["field"])
	}

	body = req("k3", b)
	body["content"] = map[string]any{"text": long}
	first := mustSend(t, env, aSec, body)
	status, again, raw := sendHTTP(t, env, aSec, body)
	if status != http.StatusOK || again.MessageID != first.MessageID || !again.Spilled {
		t.Fatalf("replay = %d %+v; %s", status, again, raw)
	}
	if n := count(t, env, `SELECT count(*) FROM messages`); n != 1 {
		t.Fatalf("messages = %d, want 1", n)
	}
}

// TestAttachingAHashNeedsTheBytesOrSight: a stored hash is not a read
// capability (PROTOCOL §11, §16.3). Attaching it needs the sender (or its
// principal) to have uploaded the bytes or to see a message that references
// them; otherwise it is 422 unknown_blob, and no error reveals the size.
func TestAttachingAHashNeedsTheBytesOrSight(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	b, bSec := env.NewPeer("session", "B")
	_, cSec := env.NewPeer("session", "C")
	d, _ := env.NewPeer("session", "D")
	const secret = "A's private report"
	sha := putBlob(t, env, aSec, secret)
	att := func(size int) []any {
		return []any{map[string]any{"sha256": sha, "size": size, "media_type": "text/plain"}}
	}
	a1 := req("a1", b)
	a1["attachments"] = att(len(secret))
	mustSend(t, env, aSec, a1)

	// C has neither the bytes nor sight of them.
	c1 := req("c1", d)
	c1["attachments"] = att(len(secret))
	mustReject(t, env, cSec, c1, http.StatusUnprocessableEntity, "unknown_blob")
	c2 := req("c2", d)
	c2["attachments"] = att(1)
	_, details := mustReject(t, env, cSec, c2, http.StatusUnprocessableEntity, "unknown_blob")
	if _, ok := details["size"]; ok {
		t.Fatalf("rejection details %v reveal the stored size", details)
	}

	// B sees it through A's message, so B may forward it.
	b1 := req("b1", d)
	b1["attachments"] = att(len(secret))
	mustSend(t, env, bSec, b1)
	b2 := req("b2", d)
	b2["attachments"] = att(1)
	_, details = mustReject(t, env, bSec, b2, http.StatusUnprocessableEntity, "attachment_size_mismatch")
	if _, ok := details["size"]; ok {
		t.Fatalf("size mismatch details %v include the stored size", details)
	}

	// C uploading the same bytes proves possession: then it may attach.
	putBlob(t, env, cSec, secret)
	c3 := req("c3", d)
	c3["attachments"] = att(len(secret))
	mustSend(t, env, cSec, c3)
}

// TestBlobTokenDiesWithItsRecipient: a blob_url token is honoured only
// while the peer it was issued to exists and is enabled (PROTOCOL §14 inv.
// 6): disabling the peer revokes the links it holds.
func TestBlobTokenDiesWithItsRecipient(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	content := "bytes for B"
	sha := putBlob(t, env, aSec, content)
	body := req("k1", b)
	body["attachments"] = []any{map[string]any{"sha256": sha, "size": len(content), "media_type": "text/plain"}}
	mustSend(t, env, aSec, body)
	u := strings.TrimPrefix(onlyDelivery(t, env, b).Attachments[0].BlobURL, env.URL)
	if resp, _ := env.Do("", "GET", u, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET blob_url = %d, want 200", resp.StatusCode)
	}
	if err := env.Hub.SetPeerDisabled(context.Background(), b, true); err != nil {
		t.Fatal(err)
	}
	if resp, raw := env.Do("", "GET", u, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("GET blob_url after disabling its recipient = %d %s, want 403", resp.StatusCode, raw)
	}
}

func TestOperatorAttachmentOnlySend(t *testing.T) {
	env := testhub.Start(t)
	peer, secret := env.NewPeer("session", "recipient")
	// Establish a thread, then address the attachment-only send to that thread.
	first := mustSend(t, env, secret, req("start-thread", env.OperatorPeer))
	sha := putBlob(t, env, env.OperatorSecret, "abc")
	body := req("attachment-only")
	body["thread_id"] = first.ThreadID
	delete(body, "recipients")
	body["content"] = map[string]any{"text": ""}
	body["attachments"] = []envelope.Attachment{{SHA256: sha, Size: 3, MediaType: "text/plain", Name: "note.txt"}}
	sent := mustSend(t, env, env.OperatorSecret, body)
	m := onlyDelivery(t, env, peer)
	if m.MessageID != sent.MessageID || m.ThreadID != first.ThreadID || m.Content.Text != "" || m.Content.Truncated {
		t.Fatalf("attachment-only message = %+v", m)
	}
	if len(m.Attachments) != 1 {
		t.Fatalf("attachments = %+v, want one", m.Attachments)
	}
	att := m.Attachments[0]
	if att.SHA256 != sha || att.Size != 3 || att.MediaType != "text/plain" || att.Name != "note.txt" {
		t.Fatalf("attachment = %+v", att)
	}
	gotSHA, token := blobToken(t, env, att.BlobURL)
	if gotSHA != sha || token == "" {
		t.Fatalf("blob_url = %q, want uploaded hash and read token", att.BlobURL)
	}
	resp, raw := env.Do("", "GET", strings.TrimPrefix(att.BlobURL, env.URL), nil)
	if resp.StatusCode != http.StatusOK || string(raw) != "abc" {
		t.Fatalf("GET blob_url = %d %q, want 200 abc", resp.StatusCode, raw)
	}
}

func TestFifteenAttachmentsPlusSpill(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "sender")
	peer, _ := env.NewPeer("session", "recipient")
	sha := putBlob(t, env, secret, "abc")
	atts := make([]envelope.Attachment, 15)
	for i := range atts {
		atts[i] = envelope.Attachment{SHA256: sha, Size: 3, MediaType: "text/plain", Name: "note.txt"}
	}
	long := strings.Repeat("x", 5000)
	body := req("fifteen-plus-spill", peer)
	body["content"] = map[string]any{"text": long}
	body["attachments"] = atts
	if sent := mustSend(t, env, secret, body); !sent.Spilled {
		t.Fatal("5000-byte text did not spill")
	}
	m := onlyDelivery(t, env, peer)
	if len(m.Attachments) != 16 || !m.Content.Truncated {
		t.Fatalf("got %d attachments, truncated=%v; want 16, true", len(m.Attachments), m.Content.Truncated)
	}
	for i, att := range m.Attachments[:15] {
		if att.SHA256 != sha || att.Size != 3 || att.MediaType != "text/plain" || att.Name != "note.txt" || att.BlobURL == "" {
			t.Fatalf("attachment %d = %+v", i, att)
		}
	}
	spill := m.Attachments[15]
	if spill.SHA256 != shaHex(long) || spill.Size != 5000 || spill.MediaType != "text/markdown" || spill.Name != "message.md" || spill.BlobURL == "" {
		t.Fatalf("last attachment = %+v, want the spill", spill)
	}
}

// enableR2Durable installs the task's injected transfer seam in a test hub.
// It deliberately installs no preference adapters or public upload/download
// routes. All provider traffic uses a fake TLS httptest endpoint.
func enableR2Durable(t *testing.T, env *testhub.Env) *blob.Transfers {
	t.Helper()
	var transfers *blob.Transfers
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			w.WriteHeader(204)
			return
		}
		if r.Method != "HEAD" {
			w.WriteHeader(405)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/test-bucket/")
		o, err := transfers.Lookup(r.Context(), key)
		if err != nil {
			w.WriteHeader(404)
			return
		}
		digest, _ := hex.DecodeString(o.SHA256)
		checksum := base64.StdEncoding.EncodeToString(digest)
		w.Header().Set("Content-Length", strconv.FormatInt(o.Size, 10))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", "attachment")
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Amz-Meta-Vecnet-Checksum-Profile", "sha256")
		w.Header().Set("X-Amz-Meta-Vecnet-Checksum", checksum)
		w.Header().Set("X-Amz-Checksum-Sha256", checksum)
		w.Header().Set("ETag", `"fake-etag"`)
	}))
	t.Cleanup(server.Close)
	objects, err := blob.NewR2Objects(blob.R2ObjectsConfig{Endpoint: server.URL, Bucket: "test-bucket", Namespace: "0123456789abcdef0123456789abcdef", AccessID: "fake-access", Secret: "fake-secret", Client: server.Client(), Now: env.Now.Now, Lookup: func(ctx context.Context, key string) (blob.R2Object, error) { return transfers.Lookup(ctx, key) }})
	if err != nil {
		t.Fatal("fake R2 initialization failed")
	}
	transfers, err = blob.NewTransfers(blob.TransfersConfig{DB: env.Hub.DB, Now: env.Now.Now, Namespace: "0123456789abcdef0123456789abcdef", Preferences: env.Hub.Preferences, Objects: objects, StorageQuota: 1024, SignPUT: func(ctx context.Context, o blob.R2Object) (blobwire.Request, error) {
		digest, _ := hex.DecodeString(o.SHA256)
		checksum := base64.StdEncoding.EncodeToString(digest)
		h := http.Header{"Content-Length": {strconv.FormatInt(o.Size, 10)}, "Content-Type": {"application/octet-stream"}, "Content-Disposition": {"attachment"}, "Cache-Control": {"private, no-store"}, "If-None-Match": {"*"}, "X-Amz-Checksum-Sha256": {checksum}, "X-Amz-Meta-Vecnet-Checksum-Profile": {"sha256"}, "X-Amz-Meta-Vecnet-Checksum": {checksum}}
		u, err := blob.PresignPUT(server.URL, "test-bucket", o.ObjectKey, "fake-access", "fake-secret", o.IssuedAt, blob.R2UploadTTL, h)
		return blobwire.Request{Method: "PUT", URL: u, ExpiresAt: o.UploadDeadline.Format(time.RFC3339Nano)}, err
	}})
	if err != nil {
		t.Fatal(err)
	}
	env.Hub.Blobs.Transfers = transfers
	return transfers
}

func TestR2SpillSurvivesAttachmentExpiry(t *testing.T) {
	env := testhub.Start(t)
	transfers := enableR2Durable(t, env)
	ctx := context.Background()
	_, aSec := env.NewPeer("session", "A")
	b, bSec := env.NewPeer("session", "B")
	_, outsider := env.NewPeer("session", "outsider")

	avatarSHA, err := env.Hub.AdminSetAvatar(ctx, b, r2AvatarPNG(t))
	if err != nil {
		t.Fatal(err)
	}
	var avatarBefore []byte
	if err := env.Hub.DB.QueryRow(`SELECT png FROM avatars WHERE sha256=?`, avatarSHA).Scan(&avatarBefore); err != nil {
		t.Fatal(err)
	}
	full := strings.Repeat("x", 8<<10)
	body := req("r2-spill", b)
	body["content"] = map[string]any{"text": full}
	sr := mustSend(t, env, aSec, body)
	m := onlyDelivery(t, env, b)
	before := storedBody(t, env, sr.MessageID)
	if !sr.Spilled || !m.Content.Truncated || len(m.Attachments) != 1 || m.Attachments[0].SHA256 != shaHex(full) {
		t.Fatal("spill representation changed")
	}
	// Public R2 attachment sends are Task 4. Exercise the produced reference
	// transaction seam on this retained Message, without weakening its send guard.
	grant, err := transfers.Reserve(ctx, env.OperatorPeer, "file", blobwire.UploadRequest{Size: 3, SHA256: shaHex("abc"), MediaType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = transfers.Complete(ctx, env.OperatorPeer, grant.BlobID); err != nil {
		t.Fatal(err)
	}
	if err = env.Hub.DB.Tx(ctx, func(tx *sql.Tx) error {
		return transfers.AddReference(ctx, tx, grant.BlobID, grant.SHA256, sr.MessageID)
	}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = env.Hub.DB.QueryRow(`SELECT count(*) FROM blobs WHERE sha256=?`, shaHex(full)).Scan(&n); err != nil || n != 0 {
		t.Fatal("R2 spill touched disk metadata")
	}
	if err = env.Hub.DB.QueryRow(`SELECT count(*) FROM durable_blob_refs WHERE msg_id=? AND purpose='spill'`, sr.MessageID).Scan(&n); err != nil || n != 1 {
		t.Fatal("spill not purpose-pinned")
	}
	env.Now.Advance(25 * time.Hour)
	if err = transfers.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	state, err := transfers.State(ctx, grant.BlobID)
	if err != nil || state.State != "expired" {
		t.Fatal("file survived logical deadline")
	}

	var avatarAfter []byte
	if err := env.Hub.DB.QueryRow(`SELECT png FROM avatars WHERE sha256=?`, avatarSHA).Scan(&avatarAfter); err != nil || !bytes.Equal(avatarBefore, avatarAfter) {
		t.Fatal("attachment expiry changed avatar PNG")
	}
	if storedBody(t, env, sr.MessageID) != before {
		t.Fatal("expiry rewrote original preview")
	}
	resp, data := env.Do(bSec, "GET", "/v1/blobs/"+shaHex(full), nil)
	if resp.StatusCode != 200 || string(data) != full {
		t.Fatal("authorized DB spill read lost full text")
	}
	resp, _ = env.Do(outsider, "GET", "/v1/blobs/"+shaHex(full), nil)
	if resp.StatusCode != 403 {
		t.Fatal("durable hash became a capability")
	}
	sha, tok := blobToken(t, env, m.Attachments[0].BlobURL)
	resp, data = env.Do("", "GET", "/v1/blobs/"+sha+"?t="+tok, nil)
	if resp.StatusCode != 200 || string(data) != full {
		t.Fatal("legacy spill token no longer resolves DB bytes")
	}
	hosted, err := env.Hub.HostedAttachments(ctx, []envelope.Message{m})
	if err != nil || len(hosted[sr.MessageID]) != 1 {
		t.Fatal("durable history lost hosted projection")
	}
}

func TestR2SpillQuotaRollbackAndPurge(t *testing.T) {
	env := testhub.Start(t)
	transfers := enableR2Durable(t, env)
	ctx := context.Background()
	_, aSec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	full := strings.Repeat("x", 8<<10)
	env.Hub.Blobs.DailyQuota = int64(len(full) - 1)
	body := req("quota", b)
	body["content"] = map[string]any{"text": full}
	resp, _ := env.Do(aSec, "POST", "/v1/messages", body)
	if resp.StatusCode != 429 {
		t.Fatalf("text quota status=%d", resp.StatusCode)
	}
	var n int
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM durable_blob_payloads`).Scan(&n); err != nil || n != 0 {
		t.Fatal("failed spill left durable bytes")
	}
	env.Hub.Blobs.DailyQuota = 500 << 20
	mustSend(t, env, aSec, body)
	if err := transfers.PurgeHash(ctx, shaHex(full)); err != nil {
		t.Fatal(err)
	}
	body["idempotency_key"] = "purged"
	resp, _ = env.Do(aSec, "POST", "/v1/messages", body)
	if resp.StatusCode != 410 {
		t.Fatal("purged spill restored")
	}
}

func TestR2DurablePurgePreservesReadAuthorization(t *testing.T) {
	for _, purpose := range []string{"spill", "source_raw"} {
		for _, entry := range []string{"store", "transfers"} {
			t.Run(purpose+"/"+entry, func(t *testing.T) {
				env := testhub.Start(t)
				transfers := enableR2Durable(t, env)
				ctx := context.Background()
				prn, principalSecret := env.NewPrincipal("recipient")
				b, enrollment, err := env.Hub.AddPeer(ctx, "session", "recipient", prn)
				if err != nil {
					t.Fatal(err)
				}
				_, bearer, err := env.Hub.Auth.Enrol(ctx, enrollment)
				if err != nil {
					t.Fatal("synthetic enrollment failed")
				}
				_, sender := env.NewPeer("session", "sender")
				_, outsider := env.NewPeer("session", "outsider")
				_, outsiderPrincipal := env.NewPrincipal("outsider")
				full := strings.Repeat("x", 8<<10)
				var msgID string
				if purpose == "spill" {
					body := req("purge", b)
					body["content"] = map[string]any{"text": full}
					msgID = mustSend(t, env, sender, body).MessageID
				} else {
					full = `{"event":"synthetic"}`
					src, _, _, err := env.Hub.AddSource(ctx, "github", "source", b, nil, "")
					if err != nil {
						t.Fatal(err)
					}
					msgID, _, err = env.Hub.IngestSource(ctx, hub.SourceMessage{SrcID: src, ProviderDeliveryID: "one", OriginThreadID: "issue/1", OriginEventID: "one", EventName: "issues.opened", Summary: "source text", Raw: []byte(full), MediaType: "application/json"})
					if err != nil {
						t.Fatal(err)
					}
				}
				sha := shaHex(full)
				before := storedBody(t, env, msgID)
				m := onlyDelivery(t, env, b)
				_, token := blobToken(t, env, m.Attachments[0].BlobURL)
				late, lateBearer := env.NewPeer("session", "late participant")
				join := req("join-after-durable", late)
				join["thread_id"] = m.ThreadID
				mustSend(t, env, bearer, join)
				for _, secret := range []string{bearer, principalSecret} {
					resp, data := env.Do(secret, "GET", "/v1/blobs/"+sha, nil)
					if resp.StatusCode != 200 || string(data) != full {
						t.Fatal("authorized durable read failed before purge")
					}
				}
				purge := env.Hub.Blobs.Purge
				if entry == "transfers" {
					purge = transfers.PurgeHash
				}
				for round := range 2 {
					if err := purge(ctx, sha); err != nil {
						t.Errorf("purge round %d: %v", round, err)
					}
					if err := env.Hub.Blobs.Purge(ctx, sha); err != nil {
						t.Errorf("repeated Store.Purge round %d: %v", round, err)
					}
					for _, secret := range []string{bearer, principalSecret} {
						resp, raw := env.Do(secret, "GET", "/v1/blobs/"+sha, nil)
						if resp.StatusCode != 410 || !bytes.Contains(raw, []byte(`"blob_purged"`)) {
							t.Errorf("authorized read after purge: status=%d, want 410 blob_purged", resp.StatusCode)
						}
					}
					resp, _ := env.Do("", "GET", "/v1/blobs/"+sha+"?t="+token, nil)
					if resp.StatusCode != 410 {
						t.Errorf("delivery token after purge: status=%d", resp.StatusCode)
					}
					for _, secret := range []string{outsider, outsiderPrincipal, lateBearer} {
						resp, _ := env.Do(secret, "GET", "/v1/blobs/"+sha, nil)
						if resp.StatusCode != 403 {
							t.Errorf("outsider learned purge state: status=%d", resp.StatusCode)
						}
					}
					var refs, payloads, size int
					if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM durable_blob_refs WHERE msg_id=? AND sha256=? AND purpose=?`, msgID, sha, purpose).Scan(&refs); err != nil {
						t.Fatal(err)
					}
					if err := env.Hub.DB.QueryRow(`SELECT count(*),coalesce(sum(length(bytes)),0) FROM durable_blob_payloads WHERE sha256=?`, sha).Scan(&payloads, &size); err != nil {
						t.Fatal(err)
					}
					if refs != 1 || payloads != 1 || size != 0 {
						t.Errorf("purge must retain authorization with empty stub: refs=%d payloads=%d bytes=%d", refs, payloads, size)
					}
					if storedBody(t, env, msgID) != before {
						t.Fatal("purge rewrote retained message")
					}
					if err := transfers.Tick(ctx); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := env.Hub.DB.Exec(`DELETE FROM messages WHERE msg_id=?`, msgID); err != nil {
					t.Fatal(err)
				}
				if err := transfers.Tick(ctx); err != nil {
					t.Fatal(err)
				}
				if err := env.Hub.Blobs.Purge(ctx, sha); err != nil {
					t.Errorf("permanent tombstone must keep purge idempotent after last-ref GC: %v", err)
				}
			})
		}
	}
}
