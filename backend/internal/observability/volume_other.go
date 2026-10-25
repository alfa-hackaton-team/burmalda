//go:build !linux && !darwin

package observability

import "fmt"

func readVolumeStats(path string) (volumeStats, error) {
	return volumeStats{}, fmt.Errorf("filesystem capacity collection is unsupported on this platform")
}
