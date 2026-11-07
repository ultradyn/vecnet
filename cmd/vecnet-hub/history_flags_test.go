package main

import (
	"flag"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/prefs"
)

func TestHistoryRetentionBootstrapFlags(t *testing.T) {
	for _, tc := range []struct {
		raw string
		ok  bool
	}{{"true", true}, {"false", true}, {"1", false}, {"TRUE", false}, {"synthetic-rejected-value", false}} {
		t.Run(tc.raw, func(t *testing.T) {
			fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			resolve := registerPreferenceFlags(fs, func(k string) string {
				switch k {
				case "VECNET_RETENTION_HISTORY":
					return "48h"
				case "VECNET_RETENTION_ARCHIVE_MODE":
					return tc.raw
				}
				return ""
			})
			if err := fs.Parse([]string{"--retention-history=24h"}); err != nil {
				t.Fatal(err)
			}
			cfg, err := resolve()
			if !tc.ok {
				if err == nil || strings.Contains(err.Error(), tc.raw) {
					t.Fatal("invalid boolean accepted or diagnostic leaked input")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.MessageHistoryRetention != 24*time.Hour || cfg.ArchiveMode != (tc.raw == "true") {
				t.Fatal("bootstrap flags did not resolve")
			}
			history := cfg.PreferenceBootstrap["retention.history"]
			archive := cfg.PreferenceBootstrap["retention.archive_mode"]
			if history.Value != prefs.Duration(86400) || history.Source != prefs.SourceFlag || history.Origin != "--retention-history" || archive.Origin != "VECNET_RETENTION_ARCHIVE_MODE" {
				t.Fatal("source/origin precedence changed")
			}
		})
	}
}
