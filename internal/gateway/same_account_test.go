package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"opencode2api/internal/config"
	"opencode2api/internal/identity"
	wire "opencode2api/internal/protocol"
	"opencode2api/internal/telemetry"
)

func TestSameAccountRetryStaysOnExactRoute(t *testing.T) {
	for _, status := range []int{401, 403, 429, 503, 200} {
		calls := 0
		var endpoint, key, body string
		g := retryGateway(t, 3, func(r *http.Request) (*http.Response, error) {
			calls++
			b, _ := io.ReadAll(r.Body)
			if calls == 1 {
				endpoint, key, body = r.URL.String(), r.Header.Get("Authorization"), string(b)
			}
			if endpoint != r.URL.String() || key != r.Header.Get("Authorization") || body != string(b) {
				t.Fatal("same-account retry changed route or body")
			}
			return retryResponse(status), nil
		})
		off := false
		g.cfg.ProxyClean.AutoSwitchProxy = &off
		g.cfg.Retry.SameAccountRetries = 2
		g.cfg.Retry.SameAccountStatusCodes = "200,401,403,429,503"
		meta := &telemetry.RequestMeta{}
		ctx := telemetry.WithRequestMeta(WithDiagnosticRequest(context.Background()), meta)
		resp, _, err := g.doUpstream(ctx, retryRoute(), retryBodies(), identity.RequestIDs{})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if calls != 3 || meta.Attempts != 3 {
			t.Fatalf("status=%d calls=%d meta=%d", status, calls, meta.Attempts)
		}
	}
}

func TestSameAccountRetryNetworkAndGlobalCap(t *testing.T) {
	for _, networkErr := range []error{syscall.ECONNREFUSED, syscall.ENETUNREACH, io.ErrUnexpectedEOF} {
		for _, cap := range []int{1, 2, 3} {
			calls := 0
			key := ""
			g := retryGateway(t, cap, func(r *http.Request) (*http.Response, error) {
				calls++
				if key == "" {
					key = r.Header.Get("Authorization")
				}
				if key != r.Header.Get("Authorization") {
					t.Fatal("network retry changed account")
				}
				return nil, networkErr
			})
			g.cfg.Retry.SameAccountRetries = 100
			_, _, err := g.doUpstream(WithDiagnosticRequest(context.Background()), retryRoute(), retryBodies(), identity.RequestIDs{})
			if calls != cap || !errors.Is(err, networkErr) {
				t.Fatalf("cap=%d calls=%d err=%v", cap, calls, err)
			}
		}
	}
}

func TestSameAccountNetworkFallbackOnlyAfterLocalBudget(t *testing.T) {
	var keys []string
	g := retryGateway(t, 3, func(r *http.Request) (*http.Response, error) {
		keys = append(keys, r.Header.Get("Authorization"))
		return nil, syscall.ECONNREFUSED
	})
	g.cfg.Retry.SameAccountRetries = 1
	g.doUpstream(WithDiagnosticRequest(context.Background()), retryRoute(), retryBodies(), identity.RequestIDs{})
	if len(keys) != 3 || keys[0] != keys[1] || keys[1] == keys[2] {
		t.Fatalf("unexpected key sequence %v", keys)
	}
}

func TestSameAccountRetryAfterAndCancellation(t *testing.T) {
	cfg := config.RetryConfig{MaxAttempts: 3, SameAccountRetries: 2}
	calls := 0
	started := time.Now()
	resp, err, used := sameAccountAttempts(context.Background(), cfg, 0, func(int) (*http.Response, error) {
		calls++
		if calls == 1 {
			r := retryResponse(429)
			r.Header.Set("Retry-After", "1")
			return r, nil
		}
		return retryResponse(200), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if used != 2 || time.Since(started) < time.Second {
		t.Fatalf("Retry-After not honored used=%d elapsed=%v", used, time.Since(started))
	}
	for _, value := range []string{"120", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat), "999999999999999999999999999"} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		calls = 0
		resp, err, used = sameAccountAttempts(ctx, cfg, 0, func(int) (*http.Response, error) {
			calls++
			r := retryResponse(429)
			r.Header.Set("Retry-After", value)
			return r, nil
		})
		cancel()
		if resp == nil || resp.StatusCode != 429 || !errors.Is(err, context.DeadlineExceeded) || calls != 1 || used != 1 {
			t.Fatalf("retry-after=%s resp=%v err=%v calls=%d used=%d", value, resp, err, calls, used)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err, used = sameAccountAttempts(ctx, cfg, 0, func(int) (*http.Response, error) { t.Fatal("canceled request sent"); return nil, nil })
	if !errors.Is(err, context.Canceled) || used != 0 {
		t.Fatal(err, used)
	}
}

func TestSameAccountPublicCooldownOnlyAfterFinalResult(t *testing.T) {
	calls := 0
	g := retryGateway(t, 10, func(*http.Request) (*http.Response, error) { return nil, nil })
	p := &proxyCleanPool{now: time.Now, blockedUntil: &atomic.Int64{}}
	proxy := g.transports.items[0]
	proxy.public = true
	node := &anonymousNode{proxy: proxy}
	active := &anonymousNodes{nodes: []*anonymousNode{node}, strict: true, blockedUntil: p.blockedUntil}
	p.active.Store(&proxyCleanSnapshot{pool: active, expires: time.Now().Add(time.Hour)})
	g.publicPool = p
	g.anonymous = &anonymousNodes{dynamic: p, cooldown: time.Second}
	proxy.client = &http.Client{Transport: retryRoundTrip(func(*http.Request) (*http.Response, error) {
		calls++
		if p.blockedUntil.Load() != 0 || node.failures.Load() != 0 {
			t.Fatal("intermediate retry prematurely cooled public lane")
		}
		return retryResponse(403), nil
	})}
	proxy.bun = proxy.client
	g.cfg.Retry.SameAccountRetries = 2
	route := retryRoute()
	route.Anonymous = true
	resp, _, err := g.doUpstream(context.Background(), route, retryBodies(), identity.RequestIDs{})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if calls != 3 || node.failures.Load() != 1 || p.blockedUntil.Load() <= time.Now().UnixNano() {
		t.Fatalf("calls=%d failures=%d gate=%d", calls, node.failures.Load(), p.blockedUntil.Load())
	}
	_, _, err = g.doUpstream(context.Background(), route, retryBodies(), identity.RequestIDs{})
	if !errors.Is(err, errProxyUnavailable) || calls != 3 {
		t.Fatalf("cooldown did not block new request: %v calls=%d", err, calls)
	}
}

func TestSameAccountAutoSwitchOffKeepsNodeButAllowsOrigins(t *testing.T) {
	var endpoints []string
	g := retryGateway(t, 8, func(r *http.Request) (*http.Response, error) {
		endpoints = append(endpoints, r.URL.String())
		return retryResponse(503), nil
	})
	// The second healthy node must never be selected after origin fallback.
	second := &proxyTransport{index: 1, name: "second", client: &http.Client{Transport: retryRoundTrip(func(*http.Request) (*http.Response, error) { t.Fatal("switched proxy"); return retryResponse(200), nil })}}
	second.bun = second.client
	second.healthy.Store(true)
	g.anonymous = anonymousPool(true, &proxyTransports{items: []*proxyTransport{g.transports.items[0], second}}, 0)
	g.publicPool = &proxyCleanPool{}
	off := false
	g.cfg.ProxyClean.AutoSwitchProxy = &off
	g.cfg.Retry.SameAccountRetries = 1
	g.cfg.Retry.SameAccountStatusCodes = "503"
	route := retryRoute()
	route.Anonymous = true
	resp, _, err := g.doUpstream(WithDiagnosticRequest(context.Background()), route, retryBodies(), identity.RequestIDs{})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(endpoints) != 4 || endpoints[0] != endpoints[1] || endpoints[2] != endpoints[3] || endpoints[1] == endpoints[2] {
		t.Fatalf("unexpected same-node origin sequence: %v", endpoints)
	}
}

func TestSameAccountAutoSwitchOffOriginFallbackAndDenials(t *testing.T) {
	for _, status := range []int{400, 401, 403, 429} {
		calls := 0
		firstURL := ""
		g := retryGateway(t, 8, func(r *http.Request) (*http.Response, error) {
			calls++
			if firstURL == "" {
				firstURL = r.URL.String()
			}
			if status == 400 && calls == 2 {
				if r.URL.String() == firstURL {
					t.Fatal("compatibility fallback did not change origin")
				}
				return retryResponse(200), nil
			}
			if status != 400 && r.URL.String() != firstURL {
				t.Fatal("denial changed origin")
			}
			return retryResponse(status), nil
		})
		off := false
		g.cfg.ProxyClean.AutoSwitchProxy = &off
		g.publicPool = &proxyCleanPool{}
		g.cfg.Retry.SameAccountRetries = 2
		route := retryRoute()
		route.Anonymous = true
		resp, _, err := g.doUpstream(WithDiagnosticRequest(context.Background()), route, retryBodies(), identity.RequestIDs{})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		wantCalls, wantStatus := 3, status
		if status == 400 {
			wantCalls, wantStatus = 2, 200
		}
		if calls != wantCalls || resp.StatusCode != wantStatus {
			t.Fatalf("status=%d calls=%d got=%d", status, calls, resp.StatusCode)
		}
	}
}

func TestSameAccountConfiguredSuccessDoesNotReplayCommittedStream(t *testing.T) {
	calls := 0
	g := retryGateway(t, 9, func(*http.Request) (*http.Response, error) {
		calls++
		r := retryResponse(200)
		r.Header.Set("Content-Type", "text/event-stream")
		r.Body = io.NopCloser(io.MultiReader(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"), retryBrokenReader{}))
		return r, nil
	})
	g.cfg.Retry.SameAccountRetries = 2
	g.cfg.Retry.SameAccountStatusCodes = "200"
	g.catalog.Replace([]string{"retry-test"}, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"retry-test","stream":true,"messages":[]}`))
	req = req.WithContext(WithDiagnosticRequest(req.Context()))
	w := httptest.NewRecorder()
	g.handleInference(wire.Chat)(w, req)
	if calls != 3 || w.Code != 200 || !strings.Contains(w.Body.String(), "hello") {
		t.Fatalf("calls=%d status=%d body=%s", calls, w.Code, w.Body.String())
	}
}

func TestSameAccountRotationUsesGlobalCap(t *testing.T) {
	calls := 0
	key := ""
	g := retryGateway(t, 2, func(r *http.Request) (*http.Response, error) {
		calls++
		if key == "" {
			key = r.Header.Get("Authorization")
		}
		if key != r.Header.Get("Authorization") {
			t.Fatal("rotation switched key mid-retry")
		}
		return retryResponse(403), nil
	})
	g.cfg.Retry.SameAccountRetries = 5
	g.catalog.Replace(nil, config.SOTAModels)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req = req.WithContext(WithDiagnosticRequest(req.Context()))
	w := httptest.NewRecorder()
	g.handleRotation(w, req, wire.Chat, map[string]any{"model": "sota", "messages": []any{}}, "sota", "sota")
	if calls != 2 || w.Code != 403 {
		t.Fatalf("calls=%d status=%d", calls, w.Code)
	}
}

func TestSameAccountDenialSurvivesLaterNetworkFailure(t *testing.T) {
	calls := 0
	g := retryGateway(t, 8, func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return retryResponse(403), nil
		}
		return nil, syscall.ECONNREFUSED
	})
	g.cfg.Retry.SameAccountRetries = 2
	resp, _, err := g.doUpstream(WithDiagnosticRequest(context.Background()), retryRoute(), retryBodies(), identity.RequestIDs{})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if calls != 3 || resp.StatusCode != 403 {
		t.Fatalf("denial erased: calls=%d status=%d", calls, resp.StatusCode)
	}
}

func TestSameAccountCanceledRetryAfterStillCoolsPublicLane(t *testing.T) {
	calls := 0
	g := retryGateway(t, 5, func(*http.Request) (*http.Response, error) { return nil, nil })
	p := &proxyCleanPool{now: time.Now, blockedUntil: &atomic.Int64{}}
	proxy := g.transports.items[0]
	proxy.public = true
	node := &anonymousNode{proxy: proxy}
	p.active.Store(&proxyCleanSnapshot{pool: &anonymousNodes{nodes: []*anonymousNode{node}, strict: true, blockedUntil: p.blockedUntil}, expires: time.Now().Add(time.Hour)})
	g.publicPool = p
	g.anonymous = &anonymousNodes{dynamic: p, cooldown: time.Second}
	proxy.client = &http.Client{Transport: retryRoundTrip(func(*http.Request) (*http.Response, error) {
		calls++
		r := retryResponse(429)
		r.Header.Set("Retry-After", "120")
		return r, nil
	})}
	proxy.bun = proxy.client
	g.cfg.Retry.SameAccountRetries = 2
	route := retryRoute()
	route.Anonymous = true
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	resp, _, err := g.doUpstream(ctx, route, retryBodies(), identity.RequestIDs{})
	if resp != nil || !errors.Is(err, context.DeadlineExceeded) || calls != 1 || node.failures.Load() != 1 || p.blockedUntil.Load() <= time.Now().Add(time.Minute).UnixNano() {
		t.Fatalf("resp=%v err=%v calls=%d failures=%d gate=%d", resp, err, calls, node.failures.Load(), p.blockedUntil.Load())
	}
}
