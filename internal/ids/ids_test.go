package ids_test

import (
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/ids"
)

func TestNewHasPrefixAndCrockford(t *testing.T) {
	id := ids.New(ids.Msg)
	if !strings.HasPrefix(id, "msg_") || len(id) != 30 {
		t.Fatal(id)
	}
	if strings.ContainsAny(id[4:], "ILOU") {
		t.Fatal(id)
	}
}

func TestParseRoundTrip(t *testing.T) {
	id := ids.New(ids.Peer)
	p, u, err := ids.Parse(id)
	if err != nil {
		t.Fatalf("Parse(%q): %v", id, err)
	}
	if p != ids.Peer {
		t.Fatalf("Parse(%q) prefix = %q, want %q", id, p, ids.Peer)
	}
	if got := "peer_" + u.String(); got != id {
		t.Fatalf("round trip = %q, want %q", got, id)
	}
}

func TestParseRejects(t *testing.T) {
	for _, bad := range []string{"msg_01JD3Q5H0X2Z4B6D8F0H2K4M6L", "peer-01JD3Q2K8M4N6P8R0T2V4X6Z8A", "01JD3Q2K8M4N6P8R0T2V4X6Z8A", ""} {
		if _, _, err := ids.Parse(bad); err == nil {
			t.Fatal(bad)
		}
	}
}

func TestMonotonicWithinMillisecond(t *testing.T) {
	prev := ids.New(ids.Msg)
	for i := 0; i < 1000; i++ {
		next := ids.New(ids.Msg)
		if next <= prev {
			t.Fatalf("id %d not strictly increasing: %q <= %q", i, next, prev)
		}
		prev = next
	}
}

func TestParseRejectsLowerCase(t *testing.T) {
	// §1.3 pins ^[0-9A-HJKMNP-TV-Z]{26}$; ulid.ParseStrict alone is case-insensitive.
	id := ids.New(ids.Msg)
	lower := "msg_" + strings.ToLower(id[4:])
	if _, _, err := ids.Parse(lower); err == nil {
		t.Fatal(lower)
	}
}

func TestIsAndIsGroup(t *testing.T) {
	team, grp, peer := ids.New(ids.Team), ids.New(ids.Grp), ids.New(ids.Peer)
	if !ids.Is(ids.Team, team) || ids.Is(ids.Peer, team) || ids.Is(ids.Peer, "peer_bogus") {
		t.Fatal("Is")
	}
	if !ids.IsGroup(team) || !ids.IsGroup(grp) || ids.IsGroup(peer) || ids.IsGroup("grp_") {
		t.Fatal("IsGroup")
	}
}
