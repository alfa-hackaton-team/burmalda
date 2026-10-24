package mljobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/config"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type Runner struct {
	Store    *Store
	Client   Client
	Idle     time.Duration
	Settings func() config.Runtime
}

func (r Runner) Run(ctx context.Context) error {
	if r.Store == nil || r.Client.BaseURL == "" || r.Client.Token == "" || (r.Settings == nil && r.Idle <= 0) {
		return fmt.Errorf("invalid ML worker configuration")
	}
	for ctx.Err() == nil {
		idle, finishTimeout := r.Idle, 5*time.Second
		if r.Settings != nil {
			settings := r.Settings()
			idle, finishTimeout = settings.ML.Idle.Std(), settings.ML.FinishTimeout.Std()
		}
		job, ok, err := r.Store.Claim(ctx)
		if err != nil {
			return err
		}
		if !ok {
			timer := time.NewTimer(idle)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
			continue
		}
		started := time.Now()
		jobCtx, span := otel.Tracer("ingestion/mljobs").Start(ctx, "ml.process_job",
			trace.WithAttributes(attribute.String("source.id", job.SourceID), attribute.String("ml_job.id", job.ID)))
		result := r.Client.Process(jobCtx, job)
		finishCtx, cancel := context.WithTimeout(context.Background(), finishTimeout)
		status, err := r.Store.Finish(finishCtx, job, result)
		cancel()
		span.End()
		if errors.Is(err, ErrLostLease) {
			slog.Warn("discarded stale ML job result", "job_id", job.ID)
			continue
		}
		if err != nil {
			return err
		}
		observability.ML(status, time.Since(started))
		slog.Info("ML job updated", "job_id", job.ID, "succeeded", result.Succeeded,
			"permanent", result.Permanent, "status", status, "source_id", job.SourceID,
			"duration_ms", time.Since(started).Milliseconds(), "trace_id", span.SpanContext().TraceID().String())
	}
	return nil
}
