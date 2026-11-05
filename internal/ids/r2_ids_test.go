package ids_test

import (
	"github.com/XertroV/vecnet/internal/ids"
	"regexp"
	"strings"
	"testing"
)

func TestBlobIDRandom128Bits(t *testing.T) {
	re := regexp.MustCompile(`^blo_[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	seen := map[string]bool{}
	prefixes := map[string]bool{}
	for i := 0; i < 256; i++ {
		id := ids.New(ids.Blo)
		if !re.MatchString(id) || seen[id] || !ids.Is(ids.Blo, id) {
			t.Fatal("malformed or repeated blob ID")
		}
		seen[id] = true
		prefixes[id[4:10]] = true
	}
	if len(prefixes) < 250 {
		t.Fatal("blob IDs contain a timestamp or shared high bits")
	}
	for _, id := range []string{"blo_80000000000000000000000000", "blo_0000000000000000000000000I", "blo_0000000000000000000000000a", "blo_0000000000000000000000000", "msg_00000000000000000000000001"} {
		if ids.Is(ids.Blo, id) {
			t.Fatal("invalid blob ID accepted")
		}
	}
	id := ids.New(ids.Blo)
	p, u, err := ids.Parse(id)
	if err != nil || p != ids.Blo || "blo_"+u.String() != id || strings.ContainsAny(id[4:], "ILOU") {
		t.Fatal("blob ID parse roundtrip")
	}
}
