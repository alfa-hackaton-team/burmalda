package observability

import (
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestVolumeFailureDoesNotPublishFakeCapacity(t *testing.T) {
	collector := NewVolumeCollector("postgres", "/unused")
	failed := false
	collector.read = func() (volumeStats, error) {
		if failed {
			return volumeStats{}, errors.New("volume unavailable")
		}
		return volumeStats{size: 1000, free: 300, available: 200}, nil
	}
	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)
	for _, failure := range []bool{false, true} {
		failed = failure
		families, err := registry.Gather()
		if err != nil {
			t.Fatal(err)
		}
		values := map[string]float64{}
		for _, family := range families {
			values[family.GetName()] = family.Metric[0].GetGauge().GetValue()
		}
		if failed {
			if len(values) != 1 || values["ingestion_storage_volume_collection_up"] != 0 {
				t.Fatalf("failed volume published capacity: %v", values)
			}
		} else if values["ingestion_storage_volume_size_bytes"] != 1000 || values["ingestion_storage_volume_free_bytes"] != 300 || values["ingestion_storage_volume_available_bytes"] != 200 || values["ingestion_storage_volume_collection_up"] != 1 {
			t.Fatalf("reserved space lost: %v", values)
		}
	}
}
