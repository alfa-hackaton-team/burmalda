package observability

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

type StateCollector struct {
	db                                                          *pgxpool.Pool
	dbUp, sources, stale, overdue, worstSuccessAge, worstDueAge *prometheus.Desc
	articleJobs, mlJobs, articleDueAge, mlDueAge                *prometheus.Desc
}

func NewStateCollector(db *pgxpool.Pool) *StateCollector {
	return &StateCollector{db: db,
		dbUp:            prometheus.NewDesc("ingestion_observer_db_up", "Whether the database state was collected successfully.", nil, nil),
		sources:         prometheus.NewDesc("ingestion_sources", "Enabled sources by health.", []string{"health"}, nil),
		stale:           prometheus.NewDesc("ingestion_sources_stale", "Enabled sources without a successful poll in three configured intervals.", nil, nil),
		overdue:         prometheus.NewDesc("ingestion_sources_overdue", "Enabled sources with next poll in the past.", nil, nil),
		worstSuccessAge: prometheus.NewDesc("ingestion_source_last_success_age_max_seconds", "Longest time since success among enabled sources, using creation time before first success.", nil, nil),
		worstDueAge:     prometheus.NewDesc("ingestion_source_poll_due_age_max_seconds", "Longest overdue source poll.", nil, nil),
		articleJobs:     prometheus.NewDesc("ingestion_article_fetch_jobs", "Article fetch jobs by status.", []string{"status"}, nil),
		mlJobs:          prometheus.NewDesc("ingestion_processing_jobs", "ML processing jobs by status.", []string{"status"}, nil),
		articleDueAge:   prometheus.NewDesc("ingestion_article_job_due_age_max_seconds", "Oldest due article job.", nil, nil),
		mlDueAge:        prometheus.NewDesc("ingestion_processing_job_due_age_max_seconds", "Oldest due ML job.", nil, nil),
	}
}

func (c *StateCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range []*prometheus.Desc{c.dbUp, c.sources, c.stale, c.overdue, c.worstSuccessAge,
		c.worstDueAge, c.articleJobs, c.mlJobs, c.articleDueAge, c.mlDueAge} {
		ch <- desc
	}
}

func (c *StateCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	state, err := c.read(ctx)
	if err != nil {
		ch <- prometheus.MustNewConstMetric(c.dbUp, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.dbUp, prometheus.GaugeValue, 1)
	for _, health := range []string{"UNKNOWN", "HEALTHY", "DEGRADED", "UNAVAILABLE"} {
		ch <- prometheus.MustNewConstMetric(c.sources, prometheus.GaugeValue, state.sources[health], health)
	}
	for _, status := range []string{"QUEUED", "RUNNING", "RETRY", "SUCCEEDED", "DEAD"} {
		ch <- prometheus.MustNewConstMetric(c.articleJobs, prometheus.GaugeValue, state.articleJobs[status], status)
	}
	for _, status := range []string{"QUEUED", "RUNNING", "RETRY", "SUCCEEDED", "FAILED"} {
		ch <- prometheus.MustNewConstMetric(c.mlJobs, prometheus.GaugeValue, state.mlJobs[status], status)
	}
	for _, item := range []struct {
		desc  *prometheus.Desc
		value float64
	}{
		{c.stale, state.stale}, {c.overdue, state.overdue}, {c.worstSuccessAge, state.worstSuccessAge},
		{c.worstDueAge, state.worstDueAge}, {c.articleDueAge, state.articleDueAge}, {c.mlDueAge, state.mlDueAge},
	} {
		ch <- prometheus.MustNewConstMetric(item.desc, prometheus.GaugeValue, item.value)
	}
}

type dbState struct {
	sources, articleJobs, mlJobs                                          map[string]float64
	stale, overdue, worstSuccessAge, worstDueAge, articleDueAge, mlDueAge float64
}

func (c *StateCollector) read(ctx context.Context) (dbState, error) {
	s := dbState{sources: map[string]float64{}, articleJobs: map[string]float64{}, mlJobs: map[string]float64{}}
	for _, item := range []struct {
		query  string
		target map[string]float64
	}{
		{`SELECT health, count(*)::double precision FROM sources WHERE enabled GROUP BY health`, s.sources},
		{`SELECT status, count(*)::double precision FROM article_fetch_jobs GROUP BY status`, s.articleJobs},
		{`SELECT status, count(*)::double precision FROM processing_jobs GROUP BY status`, s.mlJobs},
	} {
		rows, err := c.db.Query(ctx, item.query)
		if err != nil {
			return dbState{}, err
		}
		for rows.Next() {
			var key string
			var value float64
			if err := rows.Scan(&key, &value); err != nil {
				rows.Close()
				return dbState{}, err
			}
			item.target[key] = value
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return dbState{}, err
		}
	}
	err := c.db.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE clock_timestamp()-COALESCE(last_success_at,created_at) > polling_interval_seconds*3*interval '1 second')::double precision,
		count(*) FILTER (WHERE next_poll_at < clock_timestamp())::double precision,
		COALESCE(max(EXTRACT(EPOCH FROM clock_timestamp()-COALESCE(last_success_at,created_at))),0)::double precision,
		COALESCE(max(GREATEST(EXTRACT(EPOCH FROM clock_timestamp()-next_poll_at),0)),0)::double precision
		FROM sources WHERE enabled`).Scan(&s.stale, &s.overdue, &s.worstSuccessAge, &s.worstDueAge)
	if err != nil {
		return dbState{}, err
	}
	err = c.db.QueryRow(ctx, `SELECT COALESCE(max(EXTRACT(EPOCH FROM clock_timestamp()-next_attempt_at)),0)::double precision
		FROM article_fetch_jobs WHERE status IN ('QUEUED','RETRY') AND next_attempt_at<=clock_timestamp()`).Scan(&s.articleDueAge)
	if err != nil {
		return dbState{}, err
	}
	err = c.db.QueryRow(ctx, `SELECT COALESCE(max(EXTRACT(EPOCH FROM clock_timestamp()-next_attempt_at)),0)::double precision
		FROM processing_jobs WHERE status IN ('QUEUED','RETRY') AND next_attempt_at<=clock_timestamp()`).Scan(&s.mlDueAge)
	return s, err
}
