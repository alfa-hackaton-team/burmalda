package observability

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestStorageFailureDoesNotPublishZeroOrStaleUsage(t *testing.T) {
	collector := NewStorageCollector("s3", nil)
	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)
	metrics := func() map[string]float64 {
		t.Helper()
		families, err := registry.Gather()
		if err != nil {
			t.Fatal(err)
		}
		values := map[string]float64{}
		for _, family := range families {
			values[family.GetName()] = family.Metric[0].GetGauge().GetValue()
		}
		return values
	}
	if _, ok := metrics()["ingestion_storage_used_bytes"]; ok {
		t.Fatal("uncollected storage reported as zero")
	}
	objects := int64(3)
	collector.record(StorageUsage{Bytes: 35, Objects: &objects}, nil, time.Unix(1234, 0))
	values := metrics()
	if values["ingestion_storage_used_bytes"] != 35 || values["ingestion_storage_objects"] != 3 || values["ingestion_storage_collection_up"] != 1 {
		t.Fatalf("invalid inventory: %v", values)
	}
	collector.record(StorageUsage{}, errors.New("unavailable"), time.Unix(1235, 0))
	values = metrics()
	if _, ok := values["ingestion_storage_used_bytes"]; ok {
		t.Fatal("stale bytes published after failed collection")
	}
	if values["ingestion_storage_collection_up"] != 0 || values["ingestion_storage_last_success_timestamp_seconds"] != 1234 {
		t.Fatalf("failure hidden: %v", values)
	}
	collector.record(StorageUsage{}, nil, time.Unix(1236, 0))
	values = metrics()
	if size, ok := values["ingestion_storage_used_bytes"]; !ok || size != 0 || values["ingestion_storage_collection_up"] != 1 {
		t.Fatalf("empty storage not recovered: %v", values)
	}
}

func TestStorageCollectionCancelsInFlightRequest(t *testing.T) {
	started := make(chan struct{})
	collector := NewStorageCollector("postgres", func(ctx context.Context) (StorageUsage, error) {
		close(started)
		<-ctx.Done()
		return StorageUsage{}, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		collector.Run(ctx, func() (time.Duration, time.Duration) { return time.Minute, time.Minute })
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("collection did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("collection did not stop")
	}
}
