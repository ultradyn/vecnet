package mcpserver_test

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
)

func avatarPNG(t *testing.T, c color.Color) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 20, 10))
	for i := 0; i < 200; i++ {
		img.Set(i%20, i/20, c)
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// peerAvatar reads peer's avatar through vecnet_peers ("" for null).
func peerAvatar(t *testing.T, env *testhub.Env, secret, peer string) string {
	t.Helper()
	var dir struct {
		Peers []hub.DirectoryEntry `json:"peers"`
	}
	toolOK(t, env, secret, "vecnet_peers", map[string]any{}, &dir)
	for _, p := range dir.Peers {
		if p.PeerID == peer {
			if p.Avatar == nil {
				return ""
			}
			return *p.Avatar
		}
	}
	t.Fatalf("%s not in the directory", peer)
	return ""
}

func TestSetAvatarTool(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")

	var out map[string]string
	toolOK(t, env, aSec, "vecnet_set_avatar", map[string]any{"data_base64": base64.StdEncoding.EncodeToString(avatarPNG(t, color.NRGBA{200, 0, 0, 255}))}, &out)
	if len(out["avatar"]) != 64 || len(out) != 1 {
		t.Fatalf("result %v, want {avatar: sha256}", out)
	}
	if got := peerAvatar(t, env, aSec, a); got != out["avatar"] {
		t.Fatalf("directory avatar %q, want %q", got, out["avatar"])
	}
	// Unpadded and line-wrapped base64 is accepted.
	enc := base64.RawStdEncoding.EncodeToString(avatarPNG(t, color.NRGBA{0, 0, 200, 255}))
	toolOK(t, env, aSec, "vecnet_set_avatar", map[string]any{"data_base64": enc[:10] + "\n" + enc[10:]}, &out)

	toolOK(t, env, aSec, "vecnet_clear_avatar", map[string]any{}, &map[string]any{})
	if got := peerAvatar(t, env, aSec, a); got != "" {
		t.Fatalf("avatar after clear = %q", got)
	}

	// Another peer's avatar is refused.
	toolFails(t, env, aSec, "vecnet_set_avatar", map[string]any{"data_base64": enc, "as": b}, "forbidden")
	toolFails(t, env, aSec, "vecnet_clear_avatar", map[string]any{"as": b}, "forbidden")
}

func TestSetAvatarToolArguments(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "A")
	img := base64.StdEncoding.EncodeToString(avatarPNG(t, color.White))
	if p := invalidPath(t, env, aSec, "vecnet_set_avatar", map[string]any{}); p != "" {
		t.Fatalf("neither: path %q", p)
	}
	if p := invalidPath(t, env, aSec, "vecnet_set_avatar", map[string]any{"data_base64": img, "url": "https://example.com/a.png"}); p != "" {
		t.Fatalf("both: path %q", p)
	}
	if p := invalidPath(t, env, aSec, "vecnet_set_avatar", map[string]any{"data_base64": "not base64!"}); p != "/data_base64" {
		t.Fatalf("bad base64: path %q", p)
	}
	if p := invalidPath(t, env, aSec, "vecnet_set_avatar", map[string]any{"url": "http://example.com/a.png"}); p != "/url" {
		t.Fatalf("http url: path %q", p)
	}
	if p := invalidPath(t, env, aSec, "vecnet_set_avatar", map[string]any{"data_base64": img, "media_type": "image/png"}); p != "" && p != "/media_type" {
		t.Fatalf("extra property: path %q", p)
	}
}

// Security: the tool refuses what the HTTP form refuses.
func TestSetAvatarToolRefusals(t *testing.T) {
	env := testhub.StartWith(t, func(c *hub.Config) { c.AllowPrivateEgress = false }) // production egress policy
	_, aSec := env.NewPeer("session", "A")
	svg := base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`))
	toolFails(t, env, aSec, "vecnet_set_avatar", map[string]any{"data_base64": svg}, "unsupported_media_type")
	for _, u := range []string{"https://127.0.0.1/a.png", "https://169.254.169.254/latest", "https://localhost/x.png", "https://[::1]/a"} {
		toolFails(t, env, aSec, "vecnet_set_avatar", map[string]any{"url": u}, "invalid_url")
	}
	// The cap: 160 KiB decoded. One byte over still fits the 256 KiB /mcp
	// body as base64, so the hub, not the transport, answers.
	// Past the base64 maxLength (cap+3 on) it is still the tool's answer.
	for _, n := range []int{160<<10 + 1, 160<<10 + 3, 190 << 10} {
		over := make([]byte, n)
		copy(over, avatarPNG(t, color.Black))
		e := toolFails(t, env, aSec, "vecnet_set_avatar", map[string]any{"data_base64": base64.StdEncoding.EncodeToString(over)}, "payload_too_large")
		if e.Error.Details["limit"] != float64(160<<10) {
			t.Fatalf("%d bytes: limit = %v", n, e.Error.Details["limit"])
		}
	}
}
