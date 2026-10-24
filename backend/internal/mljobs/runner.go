package mljobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

type Runner struct {
	Store  *Store
	Client Client
	Idle   time.Duration
}

func (r Runner) Run(ctx context.Context) error {
	if r.Store == nil || r.Client.BaseURL == "" || r.Client.Token == "" || r.Idle <= 0 {
		return fmt.Errorf("invalid ML worker configuration")
	}
	for ctx.Err() == nil {
		job, ok, err := r.Store.Claim(ctx)
		if err != nil {
			return err
		}
		if !ok {
			timer := time.NewTimer(r.Idle)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
			continue
		}
		result := r.Client.Process(ctx, job)
		finishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = r.Store.Finish(finishCtx, job, result)
		cancel()
		if errors.Is(err, ErrLostLease) {
			slog.Warn("discarded stale ML job result", "job_id", job.ID)
			continue
		}
		if err != nil {
			return err
		}
		slog.Info("ML job updated", "job_id", job.ID, "succeeded", result.Succeeded,
			"permanent", result.Permanent)
	}
	return nil
}
