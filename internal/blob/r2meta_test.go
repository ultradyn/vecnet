package blob

import (
	"testing"
	"time"
)

func TestR2RetentionAndKey(t *testing.T) {
	now := time.Date(2026, 11, 5, 3, 4, 5, 0, time.UTC)
	for _, tt := range []struct {
		pref, class string
		duration    time.Duration
	}{{"1h", "1h", time.Hour}, {"6h", "6h", 6 * time.Hour}, {"12h", "12h", 12 * time.Hour}, {"1d", "1d", 24 * time.Hour}, {"7d", "7d", 7 * 24 * time.Hour}, {"30d", "30d", 30 * 24 * time.Hour}, {"off", "keep", 0}} {
		class, end, err := R2Retention(tt.pref, now)
		if err != nil || class != tt.class {
			t.Fatalf("retention %s rejected", tt.pref)
		}
		if tt.duration == 0 {
			if end != nil {
				t.Fatal("keep has a deadline")
			}
		} else if end == nil || !end.Equal(now.Add(tt.duration)) {
			t.Fatal("incorrect logical expiry")
		}
		key, err := R2ObjectKey("0123456789abcdef0123456789abcdef", "blo_00000000000000000000000001", class)
		prefix := "attachments/v1/exp/" + class + "/"
		if class == "keep" {
			prefix = "attachments/v1/keep/"
		}
		if err != nil || key != prefix+"0123456789abcdef0123456789abcdef/blo_00000000000000000000000001" {
			t.Fatal("class-first key mismatch")
		}
	}
	for _, pref := range []string{"", "keep", "2h", "OFF"} {
		if _, _, err := R2Retention(pref, now); err == nil {
			t.Fatal("invalid retention accepted")
		}
	}
	for _, tt := range [][3]string{{"../path", "blo_00000000000000000000000001", "1d"}, {"0123456789abcdef0123456789abcdef", "../key", "1d"}, {"0123456789abcdef0123456789abcdef", "blo_00000000000000000000000001", "2h"}} {
		if _, err := R2ObjectKey(tt[0], tt[1], tt[2]); err == nil {
			t.Fatal("unsafe key accepted")
		}
	}
}

func TestR2DownloadTTL(t *testing.T) {
	now := time.Date(2026, 11, 5, 3, 4, 5, 123000000, time.UTC)
	if ttl, err := R2DownloadTTL(now, nil); err != nil || ttl != 600*time.Second {
		t.Fatal("keep TTL")
	}
	for _, tt := range []struct{ remaining, want time.Duration }{{time.Hour, 600 * time.Second}, {600 * time.Second, 600 * time.Second}, {600*time.Second - time.Nanosecond, 599 * time.Second}, {1900 * time.Millisecond, time.Second}, {time.Second, time.Second}, {999 * time.Millisecond, 0}, {0, 0}, {-time.Second, 0}} {
		end := now.Add(tt.remaining)
		ttl, err := R2DownloadTTL(now, &end)
		if tt.want == 0 {
			if err == nil {
				t.Fatal("expired/subsecond object received capability")
			}
		} else if err != nil || ttl != tt.want {
			t.Fatal("expiry floor/cap mismatch")
		}
	}
}
