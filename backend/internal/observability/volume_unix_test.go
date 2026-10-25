//go:build linux || darwin

package observability

import (
	"path/filepath"
	"testing"
)

func TestVolumePathAndCapacity(t *testing.T) {
	path := t.TempDir()
	stats, err := readVolumeStats(path)
	if err != nil || stats.size <= 0 || stats.available > stats.free || stats.free > stats.size {
		t.Fatalf("invalid capacity %+v: %v", stats, err)
	}
	if _, err := readVolumeStats(filepath.Join(path, "not-mounted")); err == nil {
		t.Fatal("missing volume reported as parent filesystem")
	}
}
