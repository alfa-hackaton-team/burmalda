package polling

import (
	"math/rand"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/config"
)

func RetryDelay(attempts int, retryAfter time.Duration, settings config.Runtime) time.Duration {
	step := settings.ArticleRetry.RetryStep.Std()
	maximum := settings.ArticleRetry.RetryMax.Std()
	delay := step
	for i := 1; i < attempts && delay < maximum; i++ {
		if delay > maximum/2 {
			delay = maximum
		} else {
			delay *= 2
		}
	}
	if delay > maximum {
		delay = maximum
	}
	delay += time.Duration(rand.Int63n(int64(delay/5) + 1))
	if delay > maximum {
		delay = maximum
	}
	if retryAfter > delay {
		delay = retryAfter
	}
	if delay > settings.ArticleRetry.RetryAfterMax.Std() {
		delay = settings.ArticleRetry.RetryAfterMax.Std()
	}
	return delay
}
