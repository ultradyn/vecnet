package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"path/filepath"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
)

func writeAvatarFile(t *testing.T, name string, c color.Color) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 30, 20))
	for i := range 600 {
		img.Set(i%30, i/30, c)
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), name)
	if err := cliFixtureWriteFile(p, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// avatarOf reads peer's directory avatar ("" when null).
func avatarOf(t *testing.T, env *testhub.Env, peer string) string {
	t.Helper()
	dir, err := env.Hub.Directory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range dir {
		if e.PeerID == peer {
			if e.Avatar == nil {
				return ""
			}
			return *e.Avatar
		}
	}
	t.Fatalf("%s not in the directory", peer)
	return ""
}

func TestAvatarSetClear(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("session", "A")
	u := profileUser(t, env.URL, aSec)

	out := u.mustRun("--json", "avatar", "set", writeAvatarFile(t, "me.png", color.NRGBA{200, 0, 0, 255}))
	var got map[string]string
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got["avatar"]) != 64 {
		t.Fatalf("avatar set --json = %q (%v)", out, err)
	}
	if avatarOf(t, env, a) != got["avatar"] {
		t.Fatalf("directory avatar != %s", got["avatar"])
	}
	out = u.mustRun("avatar", "set", writeAvatarFile(t, "me2.png", color.NRGBA{0, 0, 200, 255}))
	if !strings.HasPrefix(out, "avatar ") {
		t.Fatalf("human output = %q", out)
	}
	u.mustRun("avatar", "clear")
	if avatarOf(t, env, a) != "" {
		t.Fatal("avatar not cleared")
	}

	// An SVG is refused by the hub (exit 2); a missing file is a usage error.
	svg := filepath.Join(t.TempDir(), "x.svg")
	cliFixtureWriteFile(svg, []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), 0o600)
	if code, _, errs := u.run("avatar", "set", svg); code != 2 || !strings.HasPrefix(errs, "unsupported_media_type: ") {
		t.Fatalf("svg: exit %d stderr %q", code, errs)
	}
	if code, _, _ := u.run("avatar", "set", filepath.Join(t.TempDir(), "nope.png")); code != 1 {
		t.Fatalf("missing file: exit %d, want 1", code)
	}
	big := filepath.Join(t.TempDir(), "big.png")
	cliFixtureWriteFile(big, make([]byte, 2<<20+1), 0o600)
	if code, _, errs := u.run("avatar", "set", big); code != 1 || !strings.Contains(errs, "2 MiB") {
		t.Fatalf("oversize file: exit %d stderr %q", code, errs)
	}
	if code, _, _ := u.run("avatar"); code != 1 {
		t.Fatalf("bare avatar: exit %d, want 1", code)
	}
}

// A URL argument is sent as {"url"} for the hub to fetch; the hub's egress
// guard refuses a private address.
func TestAvatarSetURLRefusedPrivate(t *testing.T) {
	env := testhub.StartWith(t, func(c *hub.Config) { c.AllowPrivateEgress = false })
	_, aSec := env.NewPeer("session", "A")
	u := profileUser(t, env.URL, aSec)
	if code, _, errs := u.run("avatar", "set", "https://127.0.0.1/me.png"); code != 2 || !strings.HasPrefix(errs, "invalid_url: ") {
		t.Fatalf("exit %d stderr %q", code, errs)
	}
}

func TestPeerAvatarAdmin(t *testing.T) {
	env := testhub.Start(t)
	a, aSec := env.NewPeer("session", "A")
	op := profileUser(t, env.URL, env.OperatorSecret)
	op.mustRun("peer", "avatar", a, writeAvatarFile(t, "a.png", color.NRGBA{0, 200, 0, 255}))
	if avatarOf(t, env, a) == "" {
		t.Fatal("operator set no avatar")
	}
	op.mustRun("peer", "avatar", a, "--clear")
	if avatarOf(t, env, a) != "" {
		t.Fatal("operator clear left an avatar")
	}
	if code, _, _ := op.run("peer", "avatar", a); code != 1 {
		t.Fatalf("no FILE or --clear: exit %d, want 1", code)
	}
	// A peer credential is refused on the admin path.
	u := profileUser(t, env.URL, aSec)
	if code, _, errs := u.run("peer", "avatar", a, "--clear"); code != 2 || !strings.HasPrefix(errs, "forbidden: ") {
		t.Fatalf("peer on admin path: exit %d stderr %q", code, errs)
	}
}

func TestAvatarAsPrincipal(t *testing.T) {
	env := testhub.Start(t)
	prn, pSec := env.NewPrincipal("P")
	x, _, err := env.Hub.AddPeer(context.Background(), "session", "X", prn)
	if err != nil {
		t.Fatal(err)
	}
	p := profileUser(t, env.URL, pSec)
	p.mustRun("--as", x, "avatar", "set", writeAvatarFile(t, "x.png", color.White))
	if avatarOf(t, env, x) == "" {
		t.Fatal("principal --as set no avatar")
	}
	p.mustRun("--as", x, "avatar", "clear")
	if avatarOf(t, env, x) != "" {
		t.Fatal("principal --as clear left an avatar")
	}
}
