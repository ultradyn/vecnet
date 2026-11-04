package httpapi_test

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/avatar"
	"github.com/XertroV/vecnet/internal/egress"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
)

// testPNG is a w×h PNG of one colour.
func testPNG(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// putAvatar sends PUT path with body as ctype ("" sends no Content-Type).
func putAvatar(t *testing.T, env *testhub.Env, secret, path, ctype string, body io.Reader) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, env.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

// setAvatarOK uploads img to path and returns the stored sha256.
func setAvatarOK(t *testing.T, env *testhub.Env, secret, path string, img []byte) string {
	t.Helper()
	resp, raw := putAvatar(t, env, secret, path, "image/png", bytes.NewReader(img))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT %s = %d; %s", path, resp.StatusCode, raw)
	}
	var out map[string]string
	if err := json.Unmarshal(raw, &out); err != nil || len(out["avatar"]) != 64 || len(out) != 1 {
		t.Fatalf("PUT %s body %s, want {avatar: sha256}", path, raw)
	}
	return out["avatar"]
}

// directoryAvatar returns peer's avatar field in GET /v1/peers: the sha256,
// "" for null; it fails when the field is absent.
func directoryAvatar(t *testing.T, env *testhub.Env, secret, peer string) string {
	t.Helper()
	resp, raw := env.Do(secret, http.MethodGet, "/v1/peers", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/peers = %d; %s", resp.StatusCode, raw)
	}
	var body struct {
		Peers []map[string]json.RawMessage `json:"peers"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	for _, p := range body.Peers {
		var id string
		_ = json.Unmarshal(p["peer_id"], &id)
		if id != peer {
			continue
		}
		v, ok := p["avatar"]
		if !ok {
			t.Fatalf("directory entry of %s has no avatar field: %v", peer, p)
		}
		var sha *string
		if err := json.Unmarshal(v, &sha); err != nil {
			t.Fatal(err)
		}
		if sha == nil {
			return ""
		}
		return *sha
	}
	t.Fatalf("peer %s not in the directory", peer)
	return ""
}

// getAvatar fetches GET /v1/peers/{peer}/avatar with optional headers.
func getAvatar(t *testing.T, env *testhub.Env, secret, peer string, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, env.URL+"/v1/peers/"+peer+"/avatar", nil)
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

func avatarRows(t *testing.T, env *testhub.Env) int {
	t.Helper()
	var n int
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM avatars`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAvatarSetReadClear(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("session", "A")
	b, bSec := env.NewPeer("session", "B")
	before := len(directoryEvents(t, env, b))

	if got := directoryAvatar(t, env, bSec, a); got != "" {
		t.Fatalf("fresh peer avatar = %q, want null", got)
	}
	sha := setAvatarOK(t, env, aSec, "/v1/peers/me/avatar", testPNG(t, 300, 200, color.NRGBA{200, 30, 30, 255}))
	if got := directoryAvatar(t, env, bSec, a); got != sha {
		t.Fatalf("directory avatar = %q, want %q", got, sha)
	}
	if got := directoryAvatar(t, env, bSec, b); got != "" {
		t.Fatalf("B's avatar = %q, want null", got)
	}
	evs := directoryEvents(t, env, b)
	if len(evs) != before+1 || string(evs[len(evs)-1]["change"]) != `"updated"` || string(evs[len(evs)-1]["fields"]) != `["avatar"]` ||
		string(evs[len(evs)-1]["peer_id"]) != strconv.Quote(a) {
		t.Fatalf("directory_updated events after set = %v", evs[before:])
	}

	// Any peer reads it with its bearer; it is the normalised PNG.
	resp, body := getAvatar(t, env, bSec, a, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET avatar = %d; %s", resp.StatusCode, body)
	}
	for k, want := range map[string]string{
		"Content-Type": "image/png", "ETag": `"` + sha + `"`, "Cache-Control": "private, no-cache",
		"X-Content-Type-Options": "nosniff",
	} {
		if got := resp.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	img, err := png.Decode(bytes.NewReader(body))
	if err != nil || img.Bounds().Dx() != avatar.Size || img.Bounds().Dy() != avatar.Size {
		t.Fatalf("avatar is not a %dpx PNG: %v %v", avatar.Size, err, img)
	}
	resp, _ = getAvatar(t, env, bSec, a, map[string]string{"If-None-Match": `"` + sha + `"`})
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("revalidation = %d, want 304", resp.StatusCode)
	}
	resp, _ = getAvatar(t, env, aSec, "me", nil)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("ETag") != `"`+sha+`"` {
		t.Fatalf("GET me/avatar = %d %s", resp.StatusCode, resp.Header.Get("ETag"))
	}
	resp, body = getAvatar(t, env, "", a, nil)
	mustErr(t, resp, body, http.StatusUnauthorized, "unauthenticated")
	resp, body = getAvatar(t, env, aSec, b, nil)
	mustErr(t, resp, body, http.StatusNotFound, "not_found")

	// Setting the same image again changes nothing and tells nobody.
	if again := setAvatarOK(t, env, aSec, "/v1/peers/me/avatar", testPNG(t, 300, 200, color.NRGBA{200, 30, 30, 255})); again != sha {
		t.Fatalf("same image hashed %s, want %s", again, sha)
	}
	if n := len(directoryEvents(t, env, b)); n != before+1 {
		t.Fatalf("a no-op set emitted %d events", n-before-1)
	}

	resp, body = env.Do(aSec, http.MethodDelete, "/v1/peers/me/avatar", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE = %d; %s", resp.StatusCode, body)
	}
	if got := directoryAvatar(t, env, bSec, a); got != "" {
		t.Fatalf("after clear avatar = %q", got)
	}
	resp, body = getAvatar(t, env, bSec, a, nil)
	mustErr(t, resp, body, http.StatusNotFound, "not_found")
	if n := avatarRows(t, env); n != 0 {
		t.Fatalf("%d avatar rows remain after the only one was cleared", n)
	}
	if n := len(directoryEvents(t, env, b)); n != before+2 {
		t.Fatalf("clear emitted %d events, want 1", n-before-1)
	}
}

// Two peers with the same image share one row, which outlives either alone.
func TestAvatarSharedRowGC(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	_, bSec := env.NewPeer("session", "B")
	img := testPNG(t, 64, 64, color.NRGBA{1, 2, 3, 255})
	if setAvatarOK(t, env, aSec, "/v1/peers/me/avatar", img) != setAvatarOK(t, env, bSec, "/v1/peers/me/avatar", img) {
		t.Fatal("same image, different hashes")
	}
	env.Do(aSec, http.MethodDelete, "/v1/peers/me/avatar", nil)
	if n := avatarRows(t, env); n != 1 {
		t.Fatalf("rows = %d, want 1 (still B's)", n)
	}
	setAvatarOK(t, env, bSec, "/v1/peers/me/avatar", testPNG(t, 64, 64, color.NRGBA{9, 9, 9, 255}))
	if n := avatarRows(t, env); n != 1 {
		t.Fatalf("rows = %d, want 1 (the old image collected)", n)
	}
}

// Security: an oversized body is 413 whether declared or streamed.
func TestAvatarOversizeBody(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	big := make([]byte, avatar.MaxBytes+1)
	copy(big, testPNG(t, 8, 8, color.Black))
	resp, body := putAvatar(t, env, aSec, "/v1/peers/me/avatar", "image/png", bytes.NewReader(big))
	e := mustErr(t, resp, body, http.StatusRequestEntityTooLarge, "payload_too_large")
	if e.Error.Details["limit"] != float64(avatar.MaxBytes) {
		t.Fatalf("limit = %v", e.Error.Details["limit"])
	}
	resp, body = putAvatar(t, env, aSec, "/v1/peers/me/avatar", "image/png", onlyReader{bytes.NewReader(big)})
	mustErr(t, resp, body, http.StatusRequestEntityTooLarge, "payload_too_large")
	// The 2 MiB cap is only for avatars: a 300 KiB image is accepted here.
	resp, body = putAvatar(t, env, env.OperatorSecret, "/v1/admin/peers/nobody/avatar", "image/png", bytes.NewReader(make([]byte, 300<<10)))
	if resp.StatusCode == http.StatusRequestEntityTooLarge {
		t.Fatalf("300 KiB admin avatar refused by size: %s", body)
	}
}

// pngBomb is a PNG declaring w×h in a file of a few dozen bytes.
func pngBomb(w, h uint32) []byte {
	chunk := func(typ string, data []byte) []byte {
		var b bytes.Buffer
		binary.Write(&b, binary.BigEndian, uint32(len(data)))
		b.WriteString(typ)
		b.Write(data)
		binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(append([]byte(typ), data...)))
		return b.Bytes()
	}
	var ihdr bytes.Buffer
	binary.Write(&ihdr, binary.BigEndian, w)
	binary.Write(&ihdr, binary.BigEndian, h)
	ihdr.Write([]byte{8, 6, 0, 0, 0})
	out := append([]byte("\x89PNG\r\n\x1a\n"), chunk("IHDR", ihdr.Bytes())...)
	out = append(out, chunk("IDAT", []byte{0x78, 0x9c, 0x03, 0x00, 0x00, 0x00, 0x00, 0x01})...)
	return append(out, chunk("IEND", nil)...)
}

// Security: a pixel bomb is refused from its header.
func TestAvatarPixelBomb(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("session", "A")
	bomb := pngBomb(60000, 60000)
	resp, body := putAvatar(t, env, aSec, "/v1/peers/me/avatar", "image/png", bytes.NewReader(bomb))
	e := mustErr(t, resp, body, http.StatusUnprocessableEntity, "invalid_image")
	if e.Error.Details["reason"] != "too_many_pixels" || e.Error.Details["max_pixels"] != float64(avatar.MaxPixels) {
		t.Fatalf("details = %v", e.Error.Details)
	}
	if got := directoryAvatar(t, env, aSec, a); got != "" {
		t.Fatalf("refused image was stored: %s", got)
	}
}

// Security: SVG is never accepted, by declared type or by content.
func TestAvatarRefusesSVG(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	svg := `<svg xmlns="http://www.w3.org/2000/svg" width="8" height="8"><script>alert(1)</script></svg>`
	for _, ctype := range []string{"image/svg+xml", "image/svg+xml; charset=utf-8", "image/png", "application/octet-stream", ""} {
		resp, body := putAvatar(t, env, aSec, "/v1/peers/me/avatar", ctype, strings.NewReader(svg))
		mustErr(t, resp, body, http.StatusUnsupportedMediaType, "unsupported_media_type")
	}
	// Declared SVG is refused even when the bytes are a real PNG.
	resp, body := putAvatar(t, env, aSec, "/v1/peers/me/avatar", "image/svg+xml", bytes.NewReader(testPNG(t, 4, 4, color.White)))
	mustErr(t, resp, body, http.StatusUnsupportedMediaType, "unsupported_media_type")
	if n := avatarRows(t, env); n != 0 {
		t.Fatalf("%d rows stored", n)
	}
}

// Security: garbage and polyglots are refused.
func TestAvatarRefusesGarbageAndPolyglots(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	cases := map[string]struct {
		body   []byte
		status int
		code   string
	}{
		"html":        {[]byte("<!doctype html><script>alert(1)</script>"), 415, "unsupported_media_type"},
		"empty":       {nil, 415, "unsupported_media_type"},
		"gif html":    {[]byte("GIF89a\x01\x00\x01\x00\x00\x00\x00<script>alert(1)</script>"), 422, "invalid_image"},
		"png garbage": {append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte("<html>"), 64)...), 422, "invalid_image"},
		"jpeg header": {[]byte{0xff, 0xd8, 0xff, 0xe0, 0, 0x10, 'J', 'F', 'I', 'F', 0}, 422, "invalid_image"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			resp, body := putAvatar(t, env, aSec, "/v1/peers/me/avatar", "image/png", bytes.NewReader(c.body))
			mustErr(t, resp, body, c.status, c.code)
		})
	}
	if n := avatarRows(t, env); n != 0 {
		t.Fatalf("%d rows stored", n)
	}
}

// Security: EXIF and other metadata do not survive into the stored image.
func TestAvatarStripsMetadata(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("session", "A")
	jpg, err := os.ReadFile("../avatar/testdata/exif.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(jpg, []byte("VECNET-EXIF-MARKER")) {
		t.Fatal("fixture has no EXIF marker")
	}
	resp, body := putAvatar(t, env, aSec, "/v1/peers/me/avatar", "image/jpeg", bytes.NewReader(jpg))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT jpeg = %d; %s", resp.StatusCode, body)
	}
	resp, body = getAvatar(t, env, aSec, a, nil)
	if resp.StatusCode != http.StatusOK || !bytes.HasPrefix(body, []byte("\x89PNG")) {
		t.Fatalf("GET = %d", resp.StatusCode)
	}
	for _, bad := range []string{"VECNET-EXIF-MARKER", "Exif", "eXIf", "tEXt", "iTXt"} {
		if bytes.Contains(body, []byte(bad)) {
			t.Fatalf("stored avatar still carries %q", bad)
		}
	}
}

// Security: a peer cannot set another peer's avatar; a principal can set
// only its own peers'.
func TestAvatarOnlyOwnPeer(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("session", "A")
	b, bSec := env.NewPeer("session", "B")
	img := testPNG(t, 16, 16, color.NRGBA{0, 200, 0, 255})

	resp, body := putAvatar(t, env, aSec, "/v1/peers/me/avatar?as="+b, "image/png", bytes.NewReader(img))
	mustErr(t, resp, body, http.StatusForbidden, "forbidden")
	resp, body = env.Do(aSec, http.MethodDelete, "/v1/peers/me/avatar?as="+b, nil)
	mustErr(t, resp, body, http.StatusForbidden, "forbidden")
	resp, body = putAvatar(t, env, aSec, "/v1/admin/peers/"+b+"/avatar", "image/png", bytes.NewReader(img))
	mustErr(t, resp, body, http.StatusForbidden, "forbidden")
	resp, body = env.Do(aSec, http.MethodDelete, "/v1/admin/peers/"+b+"/avatar", nil)
	mustErr(t, resp, body, http.StatusForbidden, "forbidden")
	resp, body = putAvatar(t, env, aSec, "/v1/peers/"+b+"/avatar", "image/png", bytes.NewReader(img))
	if resp.StatusCode < 400 {
		t.Fatalf("PUT /v1/peers/{other}/avatar = %d; %s", resp.StatusCode, body)
	}
	if got := directoryAvatar(t, env, bSec, b); got != "" {
		t.Fatalf("B's avatar was set by A: %s", got)
	}

	prn, prnSec := env.NewPrincipal("host")
	own, _, err := env.Hub.AddPeer(context.Background(), "pagchat-bot", "own", prn)
	if err != nil {
		t.Fatal(err)
	}
	resp, body = putAvatar(t, env, prnSec, "/v1/peers/me/avatar", "image/png", bytes.NewReader(img))
	mustErr(t, resp, body, http.StatusUnprocessableEntity, "as_required")
	sha := setAvatarOK(t, env, prnSec, "/v1/peers/me/avatar?as="+own, img)
	if got := directoryAvatar(t, env, aSec, own); got != sha {
		t.Fatalf("principal's peer avatar = %q", got)
	}
	resp, body = putAvatar(t, env, prnSec, "/v1/peers/me/avatar?as="+a, "image/png", bytes.NewReader(img))
	mustErr(t, resp, body, http.StatusForbidden, "forbidden")
}

// The operator sets and clears any peer's avatar, unlimited and audited.
func TestAvatarOperatorOverride(t *testing.T) {
	env := testhub.StartWith(t, func(c *hub.Config) { c.AvatarChangesPerHour = 1 })
	a, aSec := env.NewPeer("session", "A")
	imgs := [][]byte{
		testPNG(t, 16, 16, color.NRGBA{255, 0, 0, 255}), testPNG(t, 16, 16, color.NRGBA{0, 255, 0, 255}),
		testPNG(t, 16, 16, color.NRGBA{0, 0, 255, 255}),
	}
	var sha string
	for _, img := range imgs { // over A's own limit of 1: the operator is not limited
		sha = setAvatarOK(t, env, env.OperatorSecret, "/v1/admin/peers/"+a+"/avatar", img)
	}
	if got := directoryAvatar(t, env, aSec, a); got != sha {
		t.Fatalf("avatar = %q, want %q", got, sha)
	}
	resp, body := env.Do(env.OperatorSecret, http.MethodDelete, "/v1/admin/peers/"+a+"/avatar", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("admin DELETE = %d; %s", resp.StatusCode, body)
	}
	if got := directoryAvatar(t, env, aSec, a); got != "" {
		t.Fatalf("avatar after admin clear = %q", got)
	}
	resp, body = putAvatar(t, env, env.OperatorSecret, "/v1/admin/peers/peer_nosuch/avatar", "image/png", bytes.NewReader(imgs[0]))
	mustErr(t, resp, body, http.StatusNotFound, "not_found")
	var n int
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM audit WHERE actor = 'operator' AND action IN ('avatar_set', 'avatar_clear')`).Scan(&n); err != nil || n != 4 {
		t.Fatalf("operator avatar audit rows = %d (%v), want 4", n, err)
	}
	// The operator's own peer, through /me.
	setAvatarOK(t, env, env.OperatorSecret, "/v1/peers/me/avatar", imgs[0])

	// A disabled peer's avatar is hidden from peers, not from the operator.
	setAvatarOK(t, env, env.OperatorSecret, "/v1/admin/peers/"+a+"/avatar", imgs[1])
	_, bSec := env.NewPeer("session", "B")
	if err := env.Hub.SetPeerDisabled(context.Background(), a, true); err != nil {
		t.Fatal(err)
	}
	resp, body = getAvatar(t, env, bSec, a, nil)
	mustErr(t, resp, body, http.StatusNotFound, "not_found")
	if resp, _ := getAvatar(t, env, env.OperatorSecret, a, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("operator read of a disabled peer's avatar = %d", resp.StatusCode)
	}
}

// Security: the URL form never reaches a private, loopback or link-local
// address, and needs https.
func TestAvatarURLRefusesPrivate(t *testing.T) {
	// testhub allows private egress for its local receivers; production
	// does not, and that is the policy under test.
	env := testhub.StartWith(t, func(c *hub.Config) { c.AllowPrivateEgress = false })
	_, aSec := env.NewPeer("session", "A")
	var hits atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write(testPNG(t, 4, 4, color.White))
	}))
	defer local.Close()
	for _, u := range []string{
		"https://127.0.0.1/a.png",
		"https://127.0.0.1:" + local.URL[strings.LastIndex(local.URL, ":")+1:] + "/a.png",
		local.URL + "/a.png", // http to loopback
		"https://localhost/a.png",
		"https://[::1]/a.png",
		"https://169.254.169.254/latest/meta-data",
		"https://10.0.0.7/a.png",
		"https://192.168.1.1/a.png",
		"https://[fd00::1]/a.png",
		"https://printer.local/a.png",
		"https://0x7f000001/a.png",
		"https://user:pw@example.com/a.png",
		"http://example.com/a.png",
		"file:///etc/passwd",
	} {
		resp, body := env.Do(aSec, http.MethodPut, "/v1/peers/me/avatar", map[string]string{"url": u})
		mustErr(t, resp, body, http.StatusUnprocessableEntity, "invalid_url")
		if strings.Contains(string(body), u) {
			t.Fatalf("error quotes the URL: %s", body)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("the hub connected to a refused address %d times", hits.Load())
	}
}

// The URL form fetches through the egress client: a good image is stored;
// redirects, non-200 answers, oversize bodies and non-images are refused.
func TestAvatarURLFetch(t *testing.T) {
	env := testhub.StartWith(t, func(c *hub.Config) { c.AllowPrivateEgress = true })
	a, aSec := env.NewPeer("session", "A")
	good := testPNG(t, 40, 30, color.NRGBA{10, 20, 200, 255})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/good.png":
			w.Header().Set("Content-Type", "text/plain") // the declared type is ignored
			w.Write(good)
		case "/redirect":
			http.Redirect(w, r, "/good.png", http.StatusFound)
		case "/missing":
			http.NotFound(w, r)
		case "/huge":
			w.Write(make([]byte, avatar.MaxBytes+10))
		case "/svg":
			w.Header().Set("Content-Type", "image/svg+xml")
			w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`))
		case "/slow":
			time.Sleep(2 * time.Second)
		}
	}))
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	env.Hub.AvatarClient = egress.Policy{AllowPrivate: true, RootCAs: pool}.Client(time.Second)

	resp, body := env.Do(aSec, http.MethodPut, "/v1/peers/me/avatar", map[string]string{"url": srv.URL + "/good.png"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT url = %d; %s", resp.StatusCode, body)
	}
	var out map[string]string
	_ = json.Unmarshal(body, &out)
	if got := directoryAvatar(t, env, aSec, a); got != out["avatar"] || got == "" {
		t.Fatalf("directory avatar %q, response %s", got, body)
	}
	for path, want := range map[string]struct {
		status int
		code   string
	}{
		"/redirect": {422, "fetch_failed"},
		"/missing":  {422, "fetch_failed"},
		"/huge":     {413, "payload_too_large"},
		"/svg":      {415, "unsupported_media_type"},
		"/slow":     {422, "fetch_failed"},
	} {
		resp, body := env.Do(aSec, http.MethodPut, "/v1/peers/me/avatar", map[string]string{"url": srv.URL + path})
		mustErr(t, resp, body, want.status, want.code)
	}
	// The admin form takes a URL too.
	resp, body = env.Do(env.OperatorSecret, http.MethodPut, "/v1/admin/peers/"+a+"/avatar", map[string]string{"url": srv.URL + "/good.png"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin PUT url = %d; %s", resp.StatusCode, body)
	}
	resp, body = env.Do(aSec, http.MethodPut, "/v1/peers/me/avatar", map[string]any{"url": srv.URL + "/good.png", "extra": 1})
	mustErr(t, resp, body, http.StatusBadRequest, "invalid_body")
}

// Security: self-service changes are rate limited per peer; a no-op is free
// and the window slides.
func TestAvatarRateLimit(t *testing.T) {
	env := testhub.StartWith(t, func(c *hub.Config) { c.AvatarChangesPerHour = 3 })
	a, aSec := env.NewPeer("session", "A")
	_, bSec := env.NewPeer("session", "B")
	red, green := testPNG(t, 8, 8, color.NRGBA{255, 0, 0, 255}), testPNG(t, 8, 8, color.NRGBA{0, 255, 0, 255})

	setAvatarOK(t, env, aSec, "/v1/peers/me/avatar", red)
	setAvatarOK(t, env, aSec, "/v1/peers/me/avatar", red) // no change: not counted
	env.Now.Advance(time.Minute)
	setAvatarOK(t, env, aSec, "/v1/peers/me/avatar", green)
	if resp, body := env.Do(aSec, http.MethodDelete, "/v1/peers/me/avatar", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("clear = %d; %s", resp.StatusCode, body)
	}
	resp, body := putAvatar(t, env, aSec, "/v1/peers/me/avatar", "image/png", bytes.NewReader(red))
	e := mustErr(t, resp, body, http.StatusTooManyRequests, "rate_limited")
	if e.Error.Details["limit"] != "avatar_changes_per_hour" || e.Error.Details["max"] != float64(3) {
		t.Fatalf("details = %v", e.Error.Details)
	}
	if ra := resp.Header.Get("Retry-After"); ra != "3540" {
		t.Fatalf("Retry-After = %q, want 3540 (the first change, a minute old, ages out first)", ra)
	}
	// Clearing is a change too, and other peers are unaffected.
	resp, body = env.Do(aSec, http.MethodDelete, "/v1/peers/me/avatar", nil)
	if resp.StatusCode != http.StatusNoContent { // no avatar: a no-op, never limited
		t.Fatalf("no-op clear = %d; %s", resp.StatusCode, body)
	}
	setAvatarOK(t, env, bSec, "/v1/peers/me/avatar", red)
	// The operator still can.
	setAvatarOK(t, env, env.OperatorSecret, "/v1/admin/peers/"+a+"/avatar", red)

	env.Now.Advance(time.Hour)
	setAvatarOK(t, env, aSec, "/v1/peers/me/avatar", green)
}
