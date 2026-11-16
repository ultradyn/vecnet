package store

import "math"

// AvailableDiskBytes reports capacity available to the caller in path's
// filesystem, excluding reserved blocks or space unavailable under disk quotas.
// It is shared by maintenance and the hub's bounded storage backfill.
func AvailableDiskBytes(path string) (int64, error) { return availableDiskBytes(path) }

// diskSpaceBytes saturates rather than wrapping a very large filesystem's
// capacity into a negative value (which maintenance would treat as no space).
func diskSpaceBytes(blocks, blockSize uint64) int64 {
	if blockSize != 0 && blocks > math.MaxInt64/blockSize {
		return math.MaxInt64
	}
	return int64(blocks * blockSize)
}
