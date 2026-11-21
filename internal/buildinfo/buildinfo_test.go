package buildinfo

import "testing"

func TestCurrent(t *testing.T) {
	oldVersion, oldCommit, oldDate := Version, Commit, CommitDate
	t.Cleanup(func() { Version, Commit, CommitDate = oldVersion, oldCommit, oldDate })

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

	// An explicit Commit wins and keeps a plain release version comparable.
	Version, Commit, CommitDate = "0.2.8", "1a64b347", "2026-11-21T06:13:45+11:00"
	got = Current()
	if got.Version != "0.2.8" || got.Commit != "1a64b347" {
		t.Fatalf("explicit-commit Current() = %+v", got)
	}
}
