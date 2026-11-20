package selfupdate

import "testing"

func TestParseVersion(t *testing.T) {
	for _, s := range []string{"0.2.6", "1.0.0-rc.1", "1.0.0-alpha.beta.1", "1.0.0+build.5", "10.20.30", "1.0.0-0a"} {
		if _, ok := ParseVersion(s); !ok {
			t.Errorf("ParseVersion(%q) refused", s)
		}
	}
	for _, s := range []string{"", "dev", "v1.2.3", "1.2", "01.2.3", "1.2.3-01", " 1.2.3", "1.2.3 ", "1.2.3-", "1.2.3+",
		"2026.1106.2049+8877c73x+", "1.2.3;rm -rf /", "1.2.3\n", "1.2.3-" + string(make([]byte, 70))} {
		if _, ok := ParseVersion(s); ok {
			t.Errorf("ParseVersion(%q) accepted", s)
		}
	}
}

func TestReleaseVersion(t *testing.T) {
	for s, want := range map[string]bool{
		"0.2.6": true, "0.2.0-rc.1": true, "1.0.0+b": false, "dev": false,
		"2026.1106.2049+8877c73": false, "2026.1106.2049+8877c73.dirty": false,
	} {
		if got := ReleaseVersion(s); got != want {
			t.Errorf("ReleaseVersion(%q) = %v", s, got)
		}
	}
}

func TestCompareOrdersSemverPrecedence(t *testing.T) {
	// SemVer 2.0.0 §11's example chain, plus multi-digit and huge numbers.
	chain := []string{
		"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2",
		"1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.0.10", "1.2.0", "1.10.0", "2.0.0",
		"99999999999999999999999.0.0",
	}
	for i := range chain {
		for j := range chain {
			a, _ := ParseVersion(chain[i])
			b, _ := ParseVersion(chain[j])
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := Compare(a, b); got != want {
				t.Errorf("Compare(%s, %s) = %d, want %d", chain[i], chain[j], got, want)
			}
		}
	}
	a, _ := ParseVersion("1.0.0+one")
	b, _ := ParseVersion("1.0.0+two")
	if Compare(a, b) != 0 {
		t.Error("build metadata must not affect precedence")
	}
}

func TestNewer(t *testing.T) {
	for _, tc := range []struct {
		latest, current string
		want            bool
	}{
		{"0.2.7", "0.2.6", true},
		{"0.3.0", "0.2.6", true},
		{"0.2.6", "0.2.6", false},
		{"0.2.5", "0.2.6", false},
		{"0.2.7-rc.1", "0.2.6", true},
		{"0.2.7", "0.2.7-rc.1", true},
		{"0.2.7", "dev", false},
		{"0.2.7", "2026.1106.2049+8877c73", false},
		{"garbage", "0.2.6", false},
		{"", "0.2.6", false},
	} {
		if got := Newer(tc.latest, tc.current); got != tc.want {
			t.Errorf("Newer(%q, %q) = %v", tc.latest, tc.current, got)
		}
	}
}
