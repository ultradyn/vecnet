package hub_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestAvatarR2DurableContentUnchanged(t *testing.T) {
	env := testhub.Start(t)
	enableR2Durable(t, env)
	input := r2AvatarPNG(t)
	sha, err := env.Hub.AdminSetAvatar(context.Background(), env.OperatorPeer, input)
	if err != nil {
		t.Fatal(err)
	}
	caller := auth.Caller{Kind: "operator", PeerID: env.OperatorPeer}
	before, _, err := env.Hub.Avatar(context.Background(), caller, env.OperatorPeer)
	if err != nil {
		t.Fatal(err)
	}
	env.Now.Advance(48 * time.Hour)
	if err := env.Hub.Blobs.Transfers.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, afterSHA, err := env.Hub.Avatar(context.Background(), caller, env.OperatorPeer)
	if err != nil || sha != afterSHA || !bytes.Equal(before, after) {
		t.Fatal("attachment cleanup changed avatar PNG")
	}
}

func r2AvatarPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.NRGBA{R: 255, A: 255})
	var input bytes.Buffer
	if err := png.Encode(&input, img); err != nil {
		t.Fatal(err)
	}
	return input.Bytes()
}
