package observability

import "github.com/prometheus/client_golang/prometheus"

type volumeStats struct {
	size, free, available float64
}

type VolumeCollector struct {
	read                      func() (volumeStats, error)
	size, free, available, up *prometheus.Desc
}

func NewVolumeCollector(storage, path string) *VolumeCollector {
	labels := prometheus.Labels{"storage": storage}
	return &VolumeCollector{
		read:      func() (volumeStats, error) { return readVolumeStats(path) },
		size:      prometheus.NewDesc("ingestion_storage_volume_size_bytes", "Total capacity of the filesystem backing the storage volume; shared filesystems must not be summed.", nil, labels),
		free:      prometheus.NewDesc("ingestion_storage_volume_free_bytes", "Free filesystem bytes, including blocks reserved for privileged users.", nil, labels),
		available: prometheus.NewDesc("ingestion_storage_volume_available_bytes", "Filesystem bytes available to an unprivileged writer.", nil, labels),
		up:        prometheus.NewDesc("ingestion_storage_volume_collection_up", "Whether the storage volume filesystem was measured successfully.", nil, labels),
	}
}

func (c *VolumeCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range []*prometheus.Desc{c.size, c.free, c.available, c.up} {
		ch <- desc
	}
}

func (c *VolumeCollector) Collect(ch chan<- prometheus.Metric) {
	stats, err := c.read()
	if err != nil {
		ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, 1)
	for _, metric := range []struct {
		desc  *prometheus.Desc
		value float64
	}{
		{c.size, stats.size}, {c.free, stats.free}, {c.available, stats.available},
	} {
		ch <- prometheus.MustNewConstMetric(metric.desc, prometheus.GaugeValue, metric.value)
	}
}
