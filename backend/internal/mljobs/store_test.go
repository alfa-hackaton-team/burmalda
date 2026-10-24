package mljobs

import (
	"testing"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/config"
)

func TestPendingOperationHasMaximumDuration(t *testing.T) {
	settings := config.DefaultRuntime()
	settings.ML.MaxProcessingDuration = config.Duration(10 * time.Minute)
	now := time.Now()
	started := now.Add(-11 * time.Minute)
	job := Job{Attempts: 100, ProcessingStartedAt: &started}
	result := Result{Pending: true, ProcessingID: "operation"}
	status, finished := finishDecision(job, result, settings, now)
	if status != "FAILED" || finished.ErrorCode != "PROCESSING_TIMEOUT" {
		t.Fatalf("expired operation: status=%s result=%+v", status, finished)
	}
	started = now.Add(-time.Minute)
	status, finished = finishDecision(job, result, settings, now)
	if status != "RETRY" || finished.Error != "" {
		t.Fatalf("active operation: status=%s result=%+v", status, finished)
	}
}
