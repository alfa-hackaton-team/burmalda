package polling

import (
	"testing"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/config"
)

func TestRetryDelayBackoffJitterAndRetryAfter(t *testing.T) {
	settings := config.DefaultRuntime()
	for _, test := range []struct {
		attempts int
		minimum  time.Duration
		maximum  time.Duration
	}{{1, 30 * time.Second, 36 * time.Second}, {2, time.Minute, 72 * time.Second},
		{8, 15 * time.Minute, 15 * time.Minute}} {
		delay := RetryDelay(test.attempts, 0, settings)
		if delay < test.minimum || delay > test.maximum {
			t.Fatalf("attempt %d delay %v outside %v..%v", test.attempts, delay, test.minimum, test.maximum)
		}
	}
	if delay := RetryDelay(1, 2*time.Hour, settings); delay < 2*time.Hour {
		t.Fatalf("Retry-After was ignored: %v", delay)
	}
}
