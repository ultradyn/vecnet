package store

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestAvailableDiskBytes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "disk space \u65e5\u672c\u8a9e")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if available, err := AvailableDiskBytes(dir); err != nil || available < 0 {
		t.Fatalf("available disk bytes = %d, %v", available, err)
	}
	t.Chdir(dir)
	if available, err := AvailableDiskBytes("."); err != nil || available < 0 {
		t.Fatalf("relative directory disk bytes = %d, %v", available, err)
	}
	if _, err := availableDiskBytes(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("capacity probe accepted a missing directory")
	}
	if _, err := availableDiskBytes(dir + "\x00"); err == nil {
		t.Fatal("capacity probe accepted an invalid path")
	}
}

func TestDiskSpaceBytes(t *testing.T) {
	for _, tc := range []struct {
		blocks, size uint64
		want         int64
	}{
		{0, 4096, 0}, {123, 4096, 123 * 4096}, {math.MaxUint64, 0, 0},
		{math.MaxInt64, 1, math.MaxInt64}, {math.MaxUint64, 1, math.MaxInt64},
		{1 << 62, 4096, math.MaxInt64},
	} {
		if got := diskSpaceBytes(tc.blocks, tc.size); got != tc.want {
			t.Errorf("diskSpaceBytes(%d, %d) = %d, want %d", tc.blocks, tc.size, got, tc.want)
		}
	}
}
