package gateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"opencode2api/internal/config"
	"opencode2api/internal/identity"
	"opencode2api/internal/models"
	"opencode2api/internal/telemetry"
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
	return waitRetryDelay(ctx, delay)
}

func waitRetryDelay(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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

// Permission/quota responses remain terminal for failover, even when the
// operator opted into bounded retries at the exact same account and exit.
func terminalRetryResponse(resp *http.Response) bool {
	return resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests)
}

// fullRetryAfter does not apply the proxy cooldown cap. Extremely large valid
// delta values saturate instead of overflowing into an immediate retry.
func fullRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	digits := value != ""
	for _, r := range value {
		if r < '0' || r > '9' {
			digits = false
			break
		}
	}
	if digits {
		seconds, err := strconv.ParseUint(value, 10, 64)
		maxDelay := time.Duration(1<<63 - 1)
		if err != nil || seconds > uint64(maxDelay/time.Second) {
			return maxDelay
		}
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		return max(time.Until(when), 0)
	}
	return 0
}

// sameAccountAttempts must run before handing any response to the client.
// The caller has already applied beforeRetry for the initial call. send keeps
// its key, proxy, origin and body fixed, and records each actual HTTP call.
// Health/cooldown observations belong to the caller, once after this returns.
func sameAccountAttempts(ctx context.Context, cfg config.RetryConfig, completed int, send func(int) (*http.Response, error)) (*http.Response, error, int) {
	codes, err := cfg.SameAccountStatuses()
	if err != nil {
		return nil, err, 0
	}
	used := 0
	var lastDenied *http.Response
	for {
		if err := ctx.Err(); err != nil {
			return lastDenied, err, used
		}
		if completed+used >= cfg.MaxAttempts {
			return nil, errAttemptLimit, used
		}
		used++
		resp, err := send(completed + used)
		if terminalRetryResponse(resp) {
			// Keep only owned rejection metadata after its body is discarded.
			// A later network/5xx failure must not erase a denial and enable
			// failover to a different IP. A later successful 2xx can recover.
			lastDenied = &http.Response{StatusCode: resp.StatusCode, Header: resp.Header.Clone(), Body: http.NoBody}
		}
		if ctx.Err() != nil {
			if resp != nil {
				resp.Body.Close()
			}
			return lastDenied, ctx.Err(), used
		}
		retry := err != nil || resp != nil && codes[resp.StatusCode]
		if !retry || used > cfg.SameAccountRetries || completed+used >= cfg.MaxAttempts {
			if lastDenied != nil && !terminalRetryResponse(resp) && (err != nil || resp == nil || resp.StatusCode/100 != 2) {
				if resp != nil {
					resp.Body.Close()
				}
				return lastDenied, nil, used
			}
			return resp, err, used
		}
		delay := retryDelay(cfg, completed+used)
		if resp != nil {
			if resp.StatusCode == http.StatusTooManyRequests {
				delay = max(delay, fullRetryAfter(resp.Header.Get("Retry-After")))
			}
			// Close rather than drain: a configured 2xx SSE response may never end.
			resp.Body.Close()
		}
		if err := waitRetryDelay(ctx, delay); err != nil {
			return lastDenied, err, used
		}
	}
}

func (g *Gateway) sameAccountInference(ctx context.Context, route models.Route, ids identity.RequestIDs, proxy *proxyTransport, client *http.Client, req *http.Request, body []byte, completed int, keyID, channel string, anonymous bool) (*http.Response, error, int) {
	return sameAccountAttempts(ctx, g.cfg.Retry, completed, func(number int) (*http.Response, error) {
		attempt := req.Clone(ctx)
		attempt.Body = io.NopCloser(bytes.NewReader(body))
		attempt.GetBody = nil
		if meta := telemetry.MetaFromContext(ctx); meta != nil {
			meta.Attempts = number
			meta.Tier = string(route.Tier)
		}
		started := time.Now()
		resp, err := doInferenceAttempt(client, attempt, time.Duration(g.cfg.Performance.FirstEventTimeoutSeconds)*time.Second, g.attemptTimeout)
		if ctx.Err() == nil {
			g.recordUpstreamAttempt(ctx, route, ids, number, keyID, channel, anonymous, proxy, resp, err, time.Since(started))
		}
		return resp, err
	})
}
