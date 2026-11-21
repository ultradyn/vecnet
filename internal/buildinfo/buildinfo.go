// Package buildinfo exposes build metadata injected by the release build.
package buildinfo

import "strings"

// Version, Commit and CommitDate are set with -ldflags. Development builds
// retain safe, useful defaults. Commit lets a release-versioned build (for
// example a Fly deploy of exactly "0.2.8", which keeps the update check
// comparable) still report its source commit without "+hash" build metadata.
var (
	Version    = "dev"
	Commit     = ""
	CommitDate = "unknown"
)

// Info is the public build metadata returned by the health endpoint.
type Info struct {
	Version    string `json:"version"`
	Commit     string `json:"commit"`
	CommitDate string `json:"commit_date"`
}

// Current returns normalized metadata, including the hash carried by the
// established date+hash version string. A dirty build keeps that marker on
// its version while presenting the underlying commit hash separately.
func Current() Info {
	version := strings.TrimSpace(Version)
	if version == "" {
		version = "dev"
	}
	commit := "unknown"
	if c := strings.TrimSpace(Commit); c != "" {
		commit = c
	} else if _, tail, ok := strings.Cut(version, "+"); ok {
		tail = strings.TrimSuffix(tail, ".dirty")
		if tail != "" {
			commit = tail
		}
	}
	date := strings.TrimSpace(CommitDate)
	if date == "" {
		date = "unknown"
	}
	return Info{Version: version, Commit: commit, CommitDate: date}
}
