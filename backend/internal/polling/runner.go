package polling

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

type Processor interface {
	Poll(context.Context, Lease) (Outcome, error)
}

type Runner struct {
	Store         *Store
	Processor     Processor
	LeaseDuration time.Duration
	IdleInterval  time.Duration
	ReapBatch     int
}

func (r Runner) Run(ctx context.Context) error {
	if r.Store == nil || r.Processor == nil || r.LeaseDuration < 3*time.Second ||
		r.IdleInterval <= 0 || r.ReapBatch <= 0 {
		return fmt.Errorf("invalid polling runner configuration")
	}
	for ctx.Err() == nil {
		closed, err := r.Store.ReapExpired(ctx, r.ReapBatch)
		if err != nil {
			return err
		}
		if closed > 0 {
			slog.Warn("recovered abandoned source polls", "count", closed)
		}
		lease, ok, err := r.Store.Claim(ctx, r.LeaseDuration)
		if err != nil {
			return err
		}
		if !ok {
			timer := time.NewTimer(r.IdleInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
			continue
		}
		if err := r.processOne(ctx, lease); err != nil && ctx.Err() == nil {
			return err
		}
	}
	return nil
}

func (r Runner) processOne(ctx context.Context, lease Lease) error {
	pollCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopped := make(chan struct{})
	renewErrors := make(chan error, 1)
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(r.LeaseDuration / 3)
		defer ticker.Stop()
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-ticker.C:
				if err := r.Store.Renew(pollCtx, lease, r.LeaseDuration); err != nil {
					renewErrors <- err
					cancel()
					return
				}
			}
		}
	}()
	result, pollErr := r.Processor.Poll(pollCtx, lease)
	cancel()
	<-stopped
	select {
	case renewErr := <-renewErrors:
		if !errors.Is(renewErr, context.Canceled) {
			return fmt.Errorf("renew poll %s: %w", lease.PollID, renewErr)
		}
	default:
	}
	if pollErr != nil {
		result.Success = false
		result.Error = pollErr.Error()
	}
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	if err := r.Store.Finish(finishCtx, lease, result); err != nil {
		if errors.Is(err, ErrLostLease) {
			slog.Warn("discarded stale poll result", "source_id", lease.SourceID, "poll_id", lease.PollID)
			return nil
		}
		return err
	}
	slog.Info("source poll completed", "source_id", lease.SourceID, "poll_id", lease.PollID, "success", result.Success)
	return nil
}
