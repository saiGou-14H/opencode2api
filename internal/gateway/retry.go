package gateway

import (
	"context"
	"errors"
	"net/http"
	"time"

	"opencode2api/internal/config"
)

var errAttemptLimit = errors.New("upstream attempt limit reached")

// retryDelay is overflow-safe even for very large attempt counts. completed is
// the number of calls already made; there is no delay before the first call.
func retryDelay(cfg config.RetryConfig, completed int) time.Duration {
	if completed <= 0 || cfg.BackoffInitialMS <= 0 {
		return 0
	}
	delay := time.Duration(cfg.BackoffInitialMS) * time.Millisecond
	cap := time.Duration(cfg.BackoffMaxMS) * time.Millisecond
	if cap <= 0 {
		cap = delay
	}
	for n := 1; n < completed && delay < cap; n++ {
		if delay > cap/2 {
			return cap
		}
		delay *= 2
	}
	if delay > cap {
		return cap
	}
	return delay
}

// beforeRetry is shared by inference, rotation and catalog loops. Call it only
// before sending a new attempt, never after releasing a response downstream.
// The parent context is the total request deadline, not a fresh per-try budget.
func beforeRetry(ctx context.Context, cfg config.RetryConfig, completed int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if completed >= cfg.MaxAttempts {
		return errAttemptLimit
	}
	delay := retryDelay(cfg, completed)
	if delay == 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

// Permission and quota failures are terminal. Relay 429 and Retry-After to
// the caller instead of changing exits or credentials to evade the limit.
func terminalRetryResponse(resp *http.Response) bool {
	return resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests)
}
