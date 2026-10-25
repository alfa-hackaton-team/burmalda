package observability

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

type PoolCollector struct {
	stats                                                   func() *pgxpool.Stat
	connections, acquires, wait, empty, canceled, emptyWait *prometheus.Desc
}

func NewPoolCollector(pool *pgxpool.Pool) *PoolCollector {
	return &PoolCollector{stats: pool.Stat,
		connections: prometheus.NewDesc("ingestion_postgres_pool_connections", "Connections in this process's pool; max and total are not additional connection states.", []string{"state"}, nil),
		acquires:    prometheus.NewDesc("ingestion_postgres_pool_acquires_total", "Successful pool acquisitions.", nil, nil),
		wait:        prometheus.NewDesc("ingestion_postgres_pool_acquire_seconds_total", "Total time spent acquiring a connection, including immediate acquisitions.", nil, nil),
		empty:       prometheus.NewDesc("ingestion_postgres_pool_empty_acquires_total", "Acquisitions that had to wait for a connection.", nil, nil),
		canceled:    prometheus.NewDesc("ingestion_postgres_pool_canceled_acquires_total", "Canceled or timed out connection acquisitions.", nil, nil),
		emptyWait:   prometheus.NewDesc("ingestion_postgres_pool_empty_wait_seconds_total", "Total wait time for acquisitions when no connection was available.", nil, nil),
	}
}

func (c *PoolCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{c.connections, c.acquires, c.wait, c.empty, c.canceled, c.emptyWait} {
		ch <- d
	}
}

func (c *PoolCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.stats()
	for state, n := range map[string]int32{"acquired": s.AcquiredConns(), "idle": s.IdleConns(), "constructing": s.ConstructingConns(), "total": s.TotalConns(), "max": s.MaxConns()} {
		ch <- prometheus.MustNewConstMetric(c.connections, prometheus.GaugeValue, float64(n), state)
	}
	for _, item := range []struct {
		d *prometheus.Desc
		v float64
	}{
		{c.acquires, float64(s.AcquireCount())}, {c.wait, s.AcquireDuration().Seconds()},
		{c.empty, float64(s.EmptyAcquireCount())}, {c.canceled, float64(s.CanceledAcquireCount())}, {c.emptyWait, s.EmptyAcquireWaitTime().Seconds()},
	} {
		ch <- prometheus.MustNewConstMetric(item.d, prometheus.CounterValue, item.v)
	}
}
