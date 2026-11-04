package hubid_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hubid"
)

func key(t *testing.T, digit string) hubid.Key {
	t.Helper()
	k, err := hubid.KeyFromSeedHex(strings.Repeat(digit, 64))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func notice() envelope.HubNotice {
	return envelope.HubNotice{
		NoticeID: "ntc_01JD3Q5H0X2Z4B6D8F0H2K4M6P", Kind: "moving", Message: "Moving tonight",
		Hub: "https://hub.example", IssuedAt: "2026-11-05T07:00:00.000Z", At: "2026-11-05T23:00:00.000Z",
		ExpiresAt: "2026-11-06T07:00:00.000Z", NewURL: "https://new.example",
	}
}

func TestNoticeBytesGolden(t *testing.T) {
	got, err := hubid.NoticeBytes(notice())
	if err != nil {
		t.Fatal(err)
	}
	const want = "vecnet-hub-notice-v1\nnotice_id=ntc_01JD3Q5H0X2Z4B6D8F0H2K4M6P\nkind=moving\nhub=https://hub.example\nissued_at=2026-11-05T07:00:00.000Z\nat=2026-11-05T23:00:00.000Z\nexpires_at=2026-11-06T07:00:00.000Z\nnew_url=https://new.example\nmessage_sha256=4fbd5e5eb483363102015a3c307071f1f0fa50c5548c1b3893fc90abb24483c8\n"
	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestNoticeSignVerifyRoundTrip(t *testing.T) {
	k := key(t, "1")
	n := notice()
	if err := k.SignNotice(&n); err != nil {
		t.Fatal(err)
	}
	if err := hubid.VerifyNotice(k.Pubkey(), n); err != nil {
		t.Fatal(err)
	}
	changes := map[string]func(*envelope.HubNotice){
		"notice_id":  func(n *envelope.HubNotice) { n.NoticeID = "ntc_01JD3Q5H0X2Z4B6D8F0H2K4M6Q" },
		"hub":        func(n *envelope.HubNotice) { n.Hub = "https://other.example" },
		"issued_at":  func(n *envelope.HubNotice) { n.IssuedAt = "2026-11-05T07:01:00.000Z" },
		"at":         func(n *envelope.HubNotice) { n.At = "2026-11-05T23:01:00.000Z" },
		"expires_at": func(n *envelope.HubNotice) { n.ExpiresAt = "2026-11-06T07:01:00.000Z" },
		"new_url":    func(n *envelope.HubNotice) { n.NewURL = "https://other.example" },
		"message":    func(n *envelope.HubNotice) { n.Message += "!" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			changed := n
			change(&changed)
			if err := hubid.VerifyNotice(k.Pubkey(), changed); err == nil {
				t.Fatal("tampering accepted")
			}
		})
	}
	t.Run("kind", func(t *testing.T) {
		changed := notice()
		changed.Kind, changed.NewURL = "maintenance", ""
		if err := k.SignNotice(&changed); err != nil {
			t.Fatal(err)
		}
		if err := hubid.VerifyNotice(k.Pubkey(), changed); err != nil {
			t.Fatal(err)
		}
		changed.Kind = "restart"
		if _, err := hubid.NoticeBytes(changed); err != nil {
			t.Fatalf("kind mutation is structurally invalid: %v", err)
		}
		if err := hubid.VerifyNotice(k.Pubkey(), changed); err == nil {
			t.Fatal("kind tampering accepted")
		}
	})
	if err := hubid.VerifyNotice(key(t, "2").Pubkey(), n); err == nil {
		t.Fatal("wrong key accepted")
	}
	n.ClearedAt, n.ClearedHow = "2026-11-05T08:00:00.000Z", "operator"
	if err := hubid.VerifyNotice(k.Pubkey(), n); err != nil {
		t.Fatalf("clear metadata signed: %v", err)
	}
}

func TestChallengeBindsPublicURL(t *testing.T) {
	k := key(t, "1")
	c := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("c", 16)))
	sig, err := k.SignChallenge("HTTPS://A.Example:443/", c)
	if err != nil {
		t.Fatal(err)
	}
	if err := hubid.VerifyChallenge(k.Pubkey(), "https://a.example", c, sig); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][2]string{{"https://b.example", c}, {"https://a.example", base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("d", 16)))}} {
		if err := hubid.VerifyChallenge(k.Pubkey(), args[0], args[1], sig); err == nil {
			t.Fatal("challenge binding lost")
		}
	}
	pub, _ := hubid.DecodePubkey(k.Pubkey())
	raw, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !ed25519.Verify(pub, []byte("vecnet-hub-challenge-v1\nhttps://a.example\n"+c+"\n"), raw) {
		t.Fatal("noncanonical challenge bytes")
	}
}

func TestEndorsementVerifies(t *testing.T) {
	old, next, other := key(t, "1"), key(t, "2"), key(t, "3")
	const at = "2026-11-05T07:00:00.000Z"
	prev := envelope.HubKeyPrevious{Pubkey: old.Pubkey(), RotatedAt: at, Endorsement: old.Endorse(next.Pubkey(), at)}
	if err := hubid.VerifyEndorsement(prev, next.Pubkey()); err != nil {
		t.Fatal(err)
	}
	if err := hubid.VerifyEndorsement(prev, other.Pubkey()); err == nil {
		t.Fatal("wrong current key accepted")
	}
	swapped := prev
	swapped.Pubkey = next.Pubkey()
	if err := hubid.VerifyEndorsement(swapped, old.Pubkey()); err == nil {
		t.Fatal("swapped keys accepted")
	}
	pub, _ := hubid.DecodePubkey(old.Pubkey())
	raw, err := base64.RawURLEncoding.DecodeString(prev.Endorsement)
	if err != nil || !ed25519.Verify(pub, []byte("vecnet-hub-rotation-v1\nold="+old.Pubkey()+"\nnew="+next.Pubkey()+"\nrotated_at="+at+"\n"), raw) {
		t.Fatal("noncanonical rotation bytes")
	}
	prev.RotatedAt = "2026-11-05T08:00:00.000Z"
	if err := hubid.VerifyEndorsement(prev, next.Pubkey()); err == nil {
		t.Fatal("altered rotation time accepted")
	}
}

func TestPubkeyAndFingerprintFormat(t *testing.T) {
	k := key(t, "1")
	pub := k.Pubkey()
	if !strings.HasPrefix(pub, "ed25519:") || len(pub) != 51 {
		t.Fatal(pub)
	}
	decoded, err := hubid.DecodePubkey(pub)
	if err != nil || len(decoded) != ed25519.PublicKeySize || base64.RawURLEncoding.EncodeToString(decoded) != strings.TrimPrefix(pub, "ed25519:") {
		t.Fatalf("DecodePubkey: %x, %v", decoded, err)
	}
	fp, err := hubid.Fingerprint(pub)
	if err != nil || !strings.HasPrefix(fp, "SHA256:") || len(fp) != 50 {
		t.Fatalf("Fingerprint: %q, %v", fp, err)
	}
	for _, bad := range []string{"", strings.Repeat("0", 63), strings.Repeat("z", 64), strings.Repeat("0", 66)} {
		if _, err := hubid.KeyFromSeedHex(bad); err == nil {
			t.Errorf("seed %q accepted", bad)
		}
	}
	for _, bad := range []string{"", "rsa:" + pub[8:], pub + "=", pub[:50], pub + "\n"} {
		if _, err := hubid.DecodePubkey(bad); err == nil {
			t.Errorf("pubkey %q accepted", bad)
		}
		if _, err := hubid.Fingerprint(bad); err == nil {
			t.Errorf("fingerprint accepted %q", bad)
		}
	}
}

func TestNormalizeURL(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"HTTPS://Hub.Example:443/", "https://hub.example"}, {"http://x:80", "http://x"},
		{"https://x:8443/base/", "https://x:8443/base"}, {"http://[::1]:80/", "http://[::1]"},
		{"http://[fe80::1%25eth0]/", "http://[fe80::1%25eth0]"},
		{"http://[fe80::1%25eth0]:8080/", "http://[fe80::1%25eth0]:8080"},
		{"HTTPS://[FE80::1%25eth0]:443/", "https://[fe80::1%25eth0]"},
		{"https://[fe80::1%25eth0]:8443/a%2Fb///", "https://[fe80::1%25eth0]:8443/a%2Fb"},
		{"https://x/a%2Fb///", "https://x/a%2Fb"},
		{"https://user:pass@x", ""}, {"https://x?q=1", ""}, {"https://x?", ""},
		{"https://x#frag", ""}, {"https://x#", ""}, {"ftp://x", ""}, {"https:///path", ""},
		{"https://x\n", ""}, {"https://x:bad", ""},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := hubid.NormalizeURL(tc.raw)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("accepted %q", tc.raw)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
			if again, err := hubid.NormalizeURL(got); err != nil || again != got {
				t.Fatalf("normalizing %q again got %q, %v", got, again, err)
			}
		})
	}
}

func TestValidChallenge(t *testing.T) {
	for _, size := range []int{15, 16, 64, 65} {
		c := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("c", size)))
		if got := hubid.ValidChallenge(c); got != (size == 16 || size == 64) {
			t.Errorf("size %d = %v", size, got)
		}
	}
	c := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("c", 16)))
	for _, bad := range []string{c + "=", "+" + c[1:], c + "\n", c[:len(c)-1] + "x", ""} {
		if hubid.ValidChallenge(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}
