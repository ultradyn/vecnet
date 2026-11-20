// Package selfupdate is the hub's npm version check and its local one-click
// update. A Checker asks the npm registry, rarely and with a bounded request,
// which version of the vecnet package is tagged latest; Detect decides,
// conservatively, whether this process is a global npm install the hub can
// update itself; a Service combines the two for the operator API and runs at
// most one update job at a time.
//
// Nothing here takes a version from a client: an update installs exactly the
// version the Checker read and validated from the registry.
package selfupdate

import (
	"regexp"
	"strings"
)

// semverRE is SemVer 2.0.0's grammar: no leading zeros in numeric
// identifiers, dot-separated pre-release and build identifiers.
var semverRE = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)` +
	`(?:-((?:0|[1-9][0-9]*|[0-9]*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9]*[a-zA-Z-][0-9a-zA-Z-]*))*))?` +
	`(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

// maxVersionLen bounds a version string: it reaches npm's argv and the UI.
const maxVersionLen = 64

// Version is a parsed SemVer 2.0.0 version.
type Version struct {
	Major, Minor, Patch string // decimal, no leading zeros
	Pre                 []string
	Build               string
}

// ParseVersion parses a strict SemVer 2.0.0 string (no "v" prefix, no
// surrounding space) of at most 64 bytes.
func ParseVersion(s string) (Version, bool) {
	if len(s) > maxVersionLen {
		return Version{}, false
	}
	m := semverRE.FindStringSubmatch(s)
	if m == nil {
		return Version{}, false
	}
	v := Version{Major: m[1], Minor: m[2], Patch: m[3], Build: m[5]}
	if m[4] != "" {
		v.Pre = strings.Split(m[4], ".")
	}
	return v, true
}

// ReleaseVersion reports whether s is a version an npm release of vecnet can
// carry: strict SemVer without build metadata. Development and Fly builds
// ("dev", "2026.1106.2049+8877c73") are not, so the hub never compares them
// with the registry.
func ReleaseVersion(s string) bool {
	v, ok := ParseVersion(s)
	return ok && v.Build == ""
}

// Compare orders a and b by SemVer precedence (build metadata ignored): -1,
// 0 or +1.
func Compare(a, b Version) int {
	for _, p := range [][2]string{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if c := compareNumeric(p[0], p[1]); c != 0 {
			return c
		}
	}
	// A version without pre-release identifiers ranks above one with them.
	switch {
	case len(a.Pre) == 0 && len(b.Pre) == 0:
		return 0
	case len(a.Pre) == 0:
		return 1
	case len(b.Pre) == 0:
		return -1
	}
	for i := 0; i < len(a.Pre) && i < len(b.Pre); i++ {
		x, y := a.Pre[i], b.Pre[i]
		xn, yn := isNumeric(x), isNumeric(y)
		var c int
		switch {
		case xn && yn:
			c = compareNumeric(x, y)
		case xn: // numeric identifiers rank below alphanumeric ones
			c = -1
		case yn:
			c = 1
		default:
			c = strings.Compare(x, y)
		}
		if c != 0 {
			return c
		}
	}
	switch {
	case len(a.Pre) < len(b.Pre):
		return -1
	case len(a.Pre) > len(b.Pre):
		return 1
	}
	return 0
}

// Newer reports whether latest is a release version above current. Either
// failing to parse as a release version is false: never offer an update
// that cannot be ordered.
func Newer(latest, current string) bool {
	if !ReleaseVersion(latest) || !ReleaseVersion(current) {
		return false
	}
	l, _ := ParseVersion(latest)
	c, _ := ParseVersion(current)
	return Compare(l, c) > 0
}

// compareNumeric compares decimal strings without leading zeros, of any
// length, so no identifier can overflow.
func compareNumeric(a, b string) int {
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

func isNumeric(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}
