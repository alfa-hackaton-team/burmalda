//go:build linux || darwin

package observability

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func readVolumeStats(path string) (volumeStats, error) {
	var fs unix.Statfs_t
	if err := unix.Statfs(path, &fs); err != nil {
		return volumeStats{}, err
	}
	stats := volumeStats{
		size:      float64(fs.Blocks) * float64(fs.Bsize),
		free:      float64(fs.Bfree) * float64(fs.Bsize),
		available: float64(fs.Bavail) * float64(fs.Bsize),
	}
	if stats.size <= 0 || stats.free < 0 || stats.available < 0 || stats.free > stats.size || stats.available > stats.free {
		return volumeStats{}, fmt.Errorf("invalid filesystem capacity for %s", path)
	}
	return stats, nil
}
