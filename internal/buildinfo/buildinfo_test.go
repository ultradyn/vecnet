package buildinfo

import "testing"

func TestCurrent(t *testing.T) {
	oldVersion, oldDate := Version, CommitDate
	t.Cleanup(func() { Version, CommitDate = oldVersion, oldDate })

	Version = "2026.1106.2049+8877c73.dirty"
	CommitDate = "2026-11-06T20:49:00Z"
	got := Current()
	if got.Version != Version || got.Commit != "8877c73" || got.CommitDate != CommitDate {
		t.Fatalf("Current() = %+v", got)
	}

	Version, CommitDate = "", ""
	got = Current()
	if got.Version != "dev" || got.Commit != "unknown" || got.CommitDate != "unknown" {
		t.Fatalf("empty Current() = %+v", got)
	}
}
