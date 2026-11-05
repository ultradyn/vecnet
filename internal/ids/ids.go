// Package ids mints and parses vecnet IDs (PROTOCOL §1.3). Blob IDs are
// random 128-bit values; the other IDs are prefixed ULIDs:
// "<prefix>_" followed by a 26-character Crockford base32 ULID.
package ids

import (
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// Prefix is the type tag in front of every vecnet id.
type Prefix string

// Id prefixes of PROTOCOL §1.3, hub notices, and the isolated R2 object prefix.
const (
	Msg  Prefix = "msg"
	Dly  Prefix = "dly"
	Thr  Prefix = "thr"
	Peer Prefix = "peer"
	Prn  Prefix = "prn"
	Team Prefix = "team"
	Grp  Prefix = "grp"
	Job  Prefix = "job"
	Apr  Prefix = "apr"
	Src  Prefix = "src"
	Ntc  Prefix = "ntc"
	Blo  Prefix = "blo" // random object identity; no timestamp or authority
)

// ErrBadID reports an id that is not "<known prefix>_<Crockford ULID>".
var ErrBadID = errors.New("ids: malformed id")

var known = map[Prefix]bool{
	Msg: true, Dly: true, Thr: true, Peer: true, Prn: true,
	Team: true, Grp: true, Job: true, Apr: true, Src: true, Ntc: true, Blo: true,
}

var (
	mu      sync.Mutex
	entropy = ulid.Monotonic(rand.Reader, 0)
)

// New mints a uniformly random 128-bit ID for Blo, otherwise "<p>_" + a ULID from a process-wide monotonic crypto/rand source,
// so non-blob ids minted by this process are strictly increasing as strings.
func New(p Prefix) string {
	if p == Blo {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			panic("ids: random blob ID unavailable")
		}
		return "blo_" + ulid.ULID(random).String()
	}
	mu.Lock()
	u := ulid.MustNew(ulid.Timestamp(time.Now()), entropy)
	mu.Unlock()
	return string(p) + "_" + u.String()
}

// Parse splits s on its first "_", requires a known prefix and
// strictly parses the rest as upper-case Crockford base32 matching
// ^[0-9A-HJKMNP-TV-Z]{26}$ (rejecting I, L, O, U, lower case and overflow).
// Its ULID return value is an encoding container only for Blo, not a timestamp.
func Parse(s string) (Prefix, ulid.ULID, error) {
	head, tail, ok := strings.Cut(s, "_")
	if !ok || !known[Prefix(head)] {
		return "", ulid.ULID{}, ErrBadID
	}
	if strings.ToUpper(tail) != tail {
		// ulid.ParseStrict is case-insensitive; §1.3's pattern is upper-case only.
		return "", ulid.ULID{}, ErrBadID
	}
	u, err := ulid.ParseStrict(tail)
	if err != nil {
		return "", ulid.ULID{}, ErrBadID
	}
	return Prefix(head), u, nil
}

// Is reports whether s is a well-formed id with prefix p.
func Is(p Prefix, s string) bool {
	got, _, err := Parse(s)
	return err == nil && got == p
}

// IsGroup reports whether s is a well-formed team_ or grp_ id.
func IsGroup(s string) bool {
	return Is(Team, s) || Is(Grp, s)
}
