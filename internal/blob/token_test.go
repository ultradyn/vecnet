package blob

import (
	"strings"
	"testing"
	"time"
)

func TestR2V2TokenScopeAndVersionSeparation(t *testing.T) {
	s := &Store{Key: []byte("fake-token-secret")}
	now := time.Unix(1800000000, 0)
	sha := strings.Repeat("a", 64)
	id := "blo_00000000000000000000000001"
	token := s.TokenV2("recipient", sha, id, now.Add(time.Hour))
	if peer, ok := s.VerifyTokenV2(token, sha, id, now); !ok || peer != "recipient" {
		t.Fatal("valid v2 token refused")
	}
	for _, c := range []struct {
		token, sha, id string
		at             time.Time
	}{{token, sha, "blo_00000000000000000000000002", now}, {token, strings.Repeat("b", 64), id, now}, {token, sha, id, now.Add(time.Hour)}, {token + "x", sha, id, now}, {s.Token("recipient", sha, now.Add(time.Hour)), sha, id, now}} {
		if _, ok := s.VerifyTokenV2(c.token, c.sha, c.id, c.at); ok {
			t.Fatal("invalid v2 capability accepted")
		}
	}
	if _, ok := s.VerifyToken(token, sha, now); ok {
		t.Fatal("v2 token authorized disk")
	}
}
