package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opencode2api/internal/config"
	"opencode2api/internal/identity"
	"opencode2api/internal/models"
	wire "opencode2api/internal/protocol"
	"opencode2api/internal/telemetry"
)

type retryRoundTrip func(*http.Request) (*http.Response, error)

func (f retryRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func retryResponse(status int) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}
}
func retryGateway(t *testing.T, max int, fn retryRoundTrip) *Gateway {
	t.Helper()
	cfg := config.Config{Retry: config.RetryConfig{MaxAttempts: max, TimeoutSeconds: 5}, ZenKeys: []string{"key-a", "key-b", "key-c"}, GoKeys: []string{"go-a"}, Anonymous: true, Prefer: config.TierZen,
		Upstream: config.UpstreamConfig{Zen: "http://test/zen", Go: "http://test/go", Anonymous: "http://test/inference"}}
	cfg.Models.Protocols = map[string]string{"retry-test": string(wire.Chat)}
	for _, model := range config.SOTAModels {
		cfg.Models.Protocols[model] = string(wire.Chat)
	}
	g, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range g.transports.items {
		p.client = &http.Client{Transport: fn}
		p.bun = p.client
	}
	return g
}
func retryRoute() models.Route {
	return models.Route{ID: "retry-test", Tier: config.TierZen, Protocol: wire.Chat, KeyTiers: []config.Tier{config.TierZen, config.TierGo}}
}
func retryBodies() map[config.Tier][]byte {
	return map[config.Tier][]byte{config.TierZen: []byte(`{"model":"retry-test","messages":[]}`), config.TierGo: []byte(`{"model":"retry-test","messages":[]}`)}
}

func TestRetryDelay(t *testing.T) {
	cfg := config.RetryConfig{BackoffInitialMS: 10, BackoffMaxMS: 25}
	for n, want := range []time.Duration{0, 10 * time.Millisecond, 20 * time.Millisecond, 25 * time.Millisecond, 25 * time.Millisecond} {
		if got := retryDelay(cfg, n); got != want {
			t.Fatalf("attempt %d: got %v want %v", n, got, want)
		}
	}
	if retryDelay(cfg, int(^uint(0)>>1)) != 25*time.Millisecond {
		t.Fatal("large count did not saturate")
	}
	if retryDelay(config.RetryConfig{}, 3) != 0 {
		t.Fatal("legacy retries must not wait")
	}
	if retryDelay(config.RetryConfig{BackoffInitialMS: 10}, 9) != 10*time.Millisecond {
		t.Fatal("zero max must use fixed initial delay")
	}
}
func TestBeforeRetryCancellationAndLimit(t *testing.T) {
	cfg := config.RetryConfig{MaxAttempts: 2, BackoffInitialMS: 1000}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := beforeRetry(ctx, cfg, 0); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := beforeRetry(context.Background(), cfg, 2); !errors.Is(err, errAttemptLimit) {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := beforeRetry(ctx, cfg, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if time.Since(started) > 500*time.Millisecond {
		t.Fatal("backoff ignored deadline")
	}
}
func TestInferenceRetryFailuresAndCap(t *testing.T) {
	for _, status := range []int{0, 500, 502, 503, 429, 401, 403} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			g := retryGateway(t, 2, func(r *http.Request) (*http.Response, error) {
				calls++
				if status == 0 {
					return nil, io.ErrUnexpectedEOF
				}
				return retryResponse(status), nil
			})
			resp, _, err := g.doUpstream(WithDiagnosticRequest(context.Background()), retryRoute(), retryBodies(), identity.RequestIDs{})
			if resp != nil {
				resp.Body.Close()
			}
			want := 2
			if status == 401 || status == 403 || status == 429 {
				want = 1
			}
			if calls != want {
				t.Fatalf("calls=%d want=%d err=%v", calls, want, err)
			}
			if status == 0 && err == nil {
				t.Fatal("network error lost")
			}
		})
	}
}
func TestInferenceRetrySuccessAndBackoff(t *testing.T) {
	calls := 0
	g := retryGateway(t, 3, func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return retryResponse(503), nil
		}
		return retryResponse(200), nil
	})
	g.cfg.Retry.BackoffInitialMS = 15
	started := time.Now()
	resp, _, err := g.doUpstream(WithDiagnosticRequest(context.Background()), retryRoute(), retryBodies(), identity.RequestIDs{})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if calls != 2 || resp.StatusCode != 200 {
		t.Fatalf("calls=%d status=%d", calls, resp.StatusCode)
	}
	if time.Since(started) < 15*time.Millisecond {
		t.Fatal("retry did not back off")
	}
}
func TestInferenceCancellationDuringBackoff(t *testing.T) {
	calls := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := retryGateway(t, 3, func(r *http.Request) (*http.Response, error) { calls++; cancel(); return retryResponse(503), nil })
	g.cfg.Retry.BackoffInitialMS = 1000
	resp, _, err := g.doUpstream(WithDiagnosticRequest(ctx), retryRoute(), retryBodies(), identity.RequestIDs{})
	if resp != nil || !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("resp=%v err=%v calls=%d", resp, err, calls)
	}
}
func TestAnonymousAttemptCapAndDenial(t *testing.T) {
	for _, status := range []int{503, 401, 403, 429} {
		for _, max := range []int{1, 2, 3} {
			calls := 0
			g := retryGateway(t, max, func(r *http.Request) (*http.Response, error) { calls++; return retryResponse(status), nil })
			// More static proxies than the budget exercises the former floor bug.
			proxies := &proxyTransports{}
			for i := 0; i < 6; i++ {
				p := &proxyTransport{index: i, name: "direct", client: g.transports.items[0].client, bun: g.transports.items[0].bun}
				p.healthy.Store(true)
				proxies.items = append(proxies.items, p)
			}
			g.anonymous = anonymousPool(true, proxies, 0)
			route := retryRoute()
			route.Anonymous = true
			resp, _, attempts, err := g.doUpstreamTiers(WithDiagnosticRequest(context.Background()), route, retryBodies(), identity.RequestIDs{}, 0)
			if resp != nil {
				resp.Body.Close()
			}
			want := max
			if status == 401 || status == 403 || status == 429 {
				want = 1
			}
			if calls != want || attempts != want {
				t.Fatalf("status=%d cap=%d calls=%d attempts=%d err=%v", status, max, calls, attempts, err)
			}
		}
	}
}
func TestStaleReasoningReplaySharesCap(t *testing.T) {
	for _, max := range []int{1, 2} {
		calls := 0
		g := retryGateway(t, max, func(r *http.Request) (*http.Response, error) {
			calls++
			resp := retryResponse(400)
			resp.Body = io.NopCloser(strings.NewReader(`{"error":"reasoning item not found"}`))
			return resp, nil
		})
		route := retryRoute()
		route.Protocol = wire.Responses
		route.KeyTiers = nil
		bodies := map[config.Tier][]byte{config.TierZen: []byte(`{"previous_response_id":"old","input":[]}`)}
		resp, _, err := g.doUpstream(WithDiagnosticRequest(context.Background()), route, bodies, identity.RequestIDs{})
		if resp != nil {
			resp.Body.Close()
		}
		if err != nil || calls != max {
			t.Fatalf("cap=%d calls=%d err=%v", max, calls, err)
		}
	}
}
func TestInferenceAttemptDoesNotFollowRedirect(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: retryRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		resp := retryResponse(307)
		resp.Header.Set("Location", "http://test/other")
		return resp, nil
	})}
	req, _ := http.NewRequest(http.MethodPost, "http://test/first", strings.NewReader("test"))
	resp, err := doInferenceAttempt(client, req, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if calls != 1 || req.GetBody != nil {
		t.Fatalf("invisible replay allowed: calls=%d", calls)
	}
}

type retryBrokenReader struct{}

func (retryBrokenReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestCommittedStreamIsNotReplayed(t *testing.T) {
	calls := 0
	g := retryGateway(t, 5, func(r *http.Request) (*http.Response, error) {
		calls++
		resp := retryResponse(200)
		resp.Header.Set("Content-Type", "text/event-stream")
		resp.Body = io.NopCloser(io.MultiReader(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"), retryBrokenReader{}))
		return resp, nil
	})
	g.catalog.Replace([]string{"retry-test"}, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"retry-test","stream":true,"messages":[{"role":"user","content":"test"}]}`))
	req = req.WithContext(WithDiagnosticRequest(req.Context()))
	w := httptest.NewRecorder()
	g.handleInference(wire.Chat)(w, req)
	if w.Code != 200 || calls != 1 || !strings.Contains(w.Body.String(), "hello") {
		t.Fatalf("status=%d calls=%d body=%s", w.Code, calls, w.Body.String())
	}
}

func TestQuotaResponsePreservesRetryAfter(t *testing.T) {
	for _, retryAfter := range []string{"120", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)} {
		calls := 0
		g := retryGateway(t, 5, func(r *http.Request) (*http.Response, error) {
			calls++
			resp := retryResponse(429)
			resp.Header.Set("Retry-After", retryAfter)
			return resp, nil
		})
		g.catalog.Replace([]string{"retry-test"}, nil)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"retry-test","messages":[]}`))
		req = req.WithContext(WithDiagnosticRequest(req.Context()))
		w := httptest.NewRecorder()
		g.handleInference(wire.Chat)(w, req)
		if calls != 1 || w.Code != 429 || w.Header().Get("Retry-After") != retryAfter {
			t.Fatalf("calls=%d status=%d retry-after=%q", calls, w.Code, w.Header().Get("Retry-After"))
		}
	}
}

func TestInferenceDeadlineDuringRetryWait(t *testing.T) {
	calls := 0
	g := retryGateway(t, 3, func(r *http.Request) (*http.Response, error) { calls++; return retryResponse(503), nil })
	g.cfg.Retry.BackoffInitialMS = 1000
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	resp, _, err := g.doUpstream(WithDiagnosticRequest(ctx), retryRoute(), retryBodies(), identity.RequestIDs{})
	if resp != nil || !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatalf("resp=%v err=%v calls=%d", resp, err, calls)
	}
}

func TestAnonymousNetworkFailureCountsActualAttempts(t *testing.T) {
	calls := 0
	g := retryGateway(t, 1, func(r *http.Request) (*http.Response, error) { calls++; return nil, io.ErrUnexpectedEOF })
	route := retryRoute()
	route.Anonymous = true
	meta := &telemetry.RequestMeta{}
	ctx := telemetry.WithRequestMeta(WithDiagnosticRequest(context.Background()), meta)
	resp, _, err := g.doUpstream(ctx, route, retryBodies(), identity.RequestIDs{})
	if resp != nil || !errors.Is(err, io.ErrUnexpectedEOF) || calls != 1 || meta.Attempts != 1 {
		t.Fatalf("resp=%v err=%v calls=%d recorded=%d", resp, err, calls, meta.Attempts)
	}
}

func TestInferenceInFlightDeadlineStopsRetries(t *testing.T) {
	calls := 0
	g := retryGateway(t, 3, func(r *http.Request) (*http.Response, error) {
		calls++
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	resp, _, err := g.doUpstream(WithDiagnosticRequest(ctx), retryRoute(), retryBodies(), identity.RequestIDs{})
	if resp != nil || !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatalf("resp=%v err=%v calls=%d", resp, err, calls)
	}
}
