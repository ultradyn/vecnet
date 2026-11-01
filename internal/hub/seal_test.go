package hub_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
)

// TestSealRoundTrip: Seal output is "v1:" + base64(nonce|ciphertext) with a
// fresh 12-byte nonce, and Unseal recovers the plaintext.
func TestSealRoundTrip(t *testing.T) {
	env := testhub.Start(t)
	for _, plain := range [][]byte{[]byte("crsr_secret-key-value"), {}, bytes.Repeat([]byte{0xAB}, 4096)} {
		sealed, err := env.Hub.Seal(plain)
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		rest, ok := strings.CutPrefix(sealed, "v1:")
		if !ok {
			t.Fatalf("Seal = %q, want the v1: prefix", sealed)
		}
		raw, err := base64.StdEncoding.DecodeString(rest)
		if err != nil {
			t.Fatalf("Seal payload is not base64: %v", err)
		}
		// 12-byte nonce + ciphertext + 16-byte GCM tag.
		if len(raw) != 12+len(plain)+16 {
			t.Fatalf("sealed payload is %d bytes, want %d", len(raw), 12+len(plain)+16)
		}
		if len(plain) > 0 && bytes.Contains(raw, plain) {
			t.Fatalf("sealed payload contains the plaintext")
		}
		got, err := env.Hub.Unseal(sealed)
		if err != nil {
			t.Fatalf("Unseal: %v", err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("Unseal = %q, want %q", got, plain)
		}
	}

	a, err := env.Hub.Seal([]byte("same"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := env.Hub.Seal([]byte("same"))
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("two Seals of the same plaintext are equal (%q): the nonce is not random", a)
	}
}

// TestUnsealTamperFails: any change to a sealed value, a missing or wrong
// version prefix, truncation, or another hub's key fails to unseal.
func TestUnsealTamperFails(t *testing.T) {
	env := testhub.Start(t)
	sealed, err := env.Hub.Seal([]byte("crsr_secret-key-value"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, "v1:"))
	if err != nil {
		t.Fatal(err)
	}
	enc := func(b []byte) string { return "v1:" + base64.StdEncoding.EncodeToString(b) }

	var bad []string
	for _, i := range []int{0, 11, 12, len(raw) - 1} { // nonce, ciphertext, tag
		flipped := bytes.Clone(raw)
		flipped[i] ^= 0x01
		bad = append(bad, enc(flipped))
	}
	bad = append(bad,
		enc(raw[:len(raw)-1]), // truncated tag
		enc(raw[:11]),         // shorter than a nonce
		"v2:"+strings.TrimPrefix(sealed, "v1:"), // unknown version
		strings.TrimPrefix(sealed, "v1:"),       // no version
		"v1:not base64!",
		"",
	)
	for _, s := range bad {
		if got, err := env.Hub.Unseal(s); !errors.Is(err, hub.ErrUnseal) {
			t.Errorf("Unseal(%q) = %q, %v; want ErrUnseal", s, got, err)
		}
	}

	other := testhub.Start(t) // a fresh store has its own meta.hub_key
	if got, err := other.Hub.Unseal(sealed); !errors.Is(err, hub.ErrUnseal) {
		t.Fatalf("another hub unsealed %q to %q, %v; want ErrUnseal", sealed, got, err)
	}
}
