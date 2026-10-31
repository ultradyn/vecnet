package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testSecret(raw string) string {
	return "whsec_" + base64.StdEncoding.EncodeToString([]byte(raw))
}

func TestSignKnownVector(t *testing.T) {
	raw := "0123456789abcdef0123456789abcdef"
	secret := testSecret(raw)
	id := "dly_x"
	ts := time.Unix(1700000000, 0)
	body := []byte(`{"a":1}`)

	mac := hmac.New(sha256.New, []byte(raw))
	mac.Write([]byte(id + "." + "1700000000" + "." + string(body)))
	want := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))

	got, err := Sign(secret, id, ts, body)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if !hmac.Equal([]byte(got), []byte(want)) {
		t.Fatalf("Sign = %q, want %q", got, want)
	}
	// The key is the decoded bytes, not the literal secret string.
	bad := hmac.New(sha256.New, []byte(secret))
	bad.Write([]byte(id + ".1700000000." + string(body)))
	if got == "v1,"+base64.StdEncoding.EncodeToString(bad.Sum(nil)) {
		t.Fatal("Sign used the literal secret string as the key")
	}
	// Verify round-trips the known vector.
	if err := Verify([]string{secret}, id, "1700000000", got, body, ts); err != nil {
		t.Fatalf("Verify known vector: %v", err)
	}
}

func TestVerifyWindowAndMulti(t *testing.T) {
	secret := NewSecret()
	if !strings.HasPrefix(secret, "whsec_") {
		t.Fatalf("NewSecret = %q, want whsec_ prefix", secret)
	}
	if k, err := KeyBytes(secret); err != nil || len(k) != 32 {
		t.Fatalf("KeyBytes(NewSecret()) = %d bytes, %v; want 32, nil", len(k), err)
	}
	now := time.Unix(1800000000, 0)
	id := "dly_window"
	body := []byte(`{"type":"delivery","n":42}`)

	// Timestamp 301 s in the past → ErrTimestamp.
	old := now.Add(-301 * time.Second)
	oldSig, err := Sign(secret, id, old, body)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify([]string{secret}, id, strconv.FormatInt(old.Unix(), 10), oldSig, body, now); !errors.Is(err, ErrTimestamp) {
		t.Fatalf("ts now-301s: err = %v, want ErrTimestamp", err)
	}
	// 301 s in the future → ErrTimestamp as well; exactly 300 s either way is accepted.
	future := now.Add(301 * time.Second)
	futSig, _ := Sign(secret, id, future, body)
	if err := Verify([]string{secret}, id, strconv.FormatInt(future.Unix(), 10), futSig, body, now); !errors.Is(err, ErrTimestamp) {
		t.Fatalf("ts now+301s: err = %v, want ErrTimestamp", err)
	}
	for _, d := range []time.Duration{-300 * time.Second, 300 * time.Second} {
		edge := now.Add(d)
		s, _ := Sign(secret, id, edge, body)
		if err := Verify([]string{secret}, id, strconv.FormatInt(edge.Unix(), 10), s, body, now); err != nil {
			t.Fatalf("ts now%+v: err = %v, want nil", d, err)
		}
	}
	// Garbage timestamp header → ErrTimestamp.
	if err := Verify([]string{secret}, id, "not-a-number", oldSig, body, now); !errors.Is(err, ErrTimestamp) {
		t.Fatalf("garbage ts: err = %v, want ErrTimestamp", err)
	}

	tsHeader := strconv.FormatInt(now.Unix(), 10)
	good, err := Sign(secret, id, now, body)
	if err != nil {
		t.Fatal(err)
	}

	// 9 signatures (the good one last) → ErrSignature.
	nine := strings.Repeat("v1,bad ", 8) + good
	if err := Verify([]string{secret}, id, tsHeader, nine, body, now); !errors.Is(err, ErrSignature) {
		t.Fatalf("9 signatures: err = %v, want ErrSignature", err)
	}
	// 8 signatures (the good one last) is still within the limit.
	eight := strings.Repeat("v1,bad ", 7) + good
	if err := Verify([]string{secret}, id, tsHeader, eight, body, now); err != nil {
		t.Fatalf("8 signatures: err = %v, want nil", err)
	}

	// "v1,bad v1,<good>" → nil.
	if err := Verify([]string{secret}, id, tsHeader, "v1,bad "+good, body, now); err != nil {
		t.Fatalf("v1,bad v1,<good>: err = %v, want nil", err)
	}

	// Body with one byte changed → ErrSignature.
	tampered := append([]byte(nil), body...)
	tampered[len(tampered)-2] ^= 0x01
	if err := Verify([]string{secret}, id, tsHeader, good, tampered, now); !errors.Is(err, ErrSignature) {
		t.Fatalf("tampered body: err = %v, want ErrSignature", err)
	}
	// Different id → ErrSignature.
	if err := Verify([]string{secret}, "dly_other", tsHeader, good, body, now); !errors.Is(err, ErrSignature) {
		t.Fatalf("different id: err = %v, want ErrSignature", err)
	}
	// Wrong secret → ErrSignature.
	if err := Verify([]string{NewSecret()}, id, tsHeader, good, body, now); !errors.Is(err, ErrSignature) {
		t.Fatalf("wrong secret: err = %v, want ErrSignature", err)
	}
}

func TestRotationOverlap(t *testing.T) {
	oldS, newS := NewSecret(), NewSecret()
	if oldS == newS {
		t.Fatal("NewSecret returned the same secret twice")
	}
	now := time.Unix(1750000000, 0)
	id := "dly_rot"
	body := []byte(`{"x":"y"}`)
	ts := strconv.FormatInt(now.Unix(), 10)

	header, err := SignAll([]string{oldS, newS}, id, now, body)
	if err != nil {
		t.Fatalf("SignAll: %v", err)
	}
	parts := strings.Split(header, " ")
	if len(parts) != 2 {
		t.Fatalf("SignAll header has %d parts, want 2: %q", len(parts), header)
	}
	so, _ := Sign(oldS, id, now, body)
	sn, _ := Sign(newS, id, now, body)
	if parts[0] != so || parts[1] != sn {
		t.Fatalf("SignAll = %q, want %q", header, so+" "+sn)
	}
	if err := Verify([]string{oldS}, id, ts, header, body, now); err != nil {
		t.Fatalf("verify against [old]: %v", err)
	}
	if err := Verify([]string{newS}, id, ts, header, body, now); err != nil {
		t.Fatalf("verify against [new]: %v", err)
	}
	if err := Verify([]string{NewSecret()}, id, ts, header, body, now); !errors.Is(err, ErrSignature) {
		t.Fatalf("verify against unrelated secret: err = %v, want ErrSignature", err)
	}
}

func TestKeyBytesBounds(t *testing.T) {
	cases := []struct {
		name   string
		secret string
		ok     bool
	}{
		{"16-byte key", testSecret(strings.Repeat("k", 16)), false},
		{"23-byte key", testSecret(strings.Repeat("k", 23)), false},
		{"24-byte key", testSecret(strings.Repeat("k", 24)), true},
		{"64-byte key", testSecret(strings.Repeat("k", 64)), true},
		{"65-byte key", testSecret(strings.Repeat("k", 65)), false},
		{"no prefix", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), false},
		{"bad base64", "whsec_!!!not base64!!!", false},
	}
	for _, c := range cases {
		k, err := KeyBytes(c.secret)
		if c.ok {
			if err != nil {
				t.Errorf("%s: err = %v, want nil", c.name, err)
			}
			continue
		}
		if !errors.Is(err, ErrBadSecret) {
			t.Errorf("%s: err = %v (key %d bytes), want ErrBadSecret", c.name, err, len(k))
		}
	}
	// Sign with a bad secret propagates ErrBadSecret.
	if _, err := Sign(testSecret(strings.Repeat("k", 16)), "dly_x", time.Unix(1, 0), nil); !errors.Is(err, ErrBadSecret) {
		t.Fatalf("Sign with 16-byte key: err = %v, want ErrBadSecret", err)
	}
}
