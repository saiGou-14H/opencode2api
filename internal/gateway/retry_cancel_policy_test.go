package gateway

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"opencode2api/internal/identity"
)

func publicCancellationFixture(t *testing.T, send retryRoundTrip) (*Gateway, *proxyCleanPool, []*anonymousNode) {
	t.Helper()
	g := retryGateway(t, 6, send)
	p := &proxyCleanPool{now: time.Now, blockedUntil: &atomic.Int64{}}
	var nodes []*anonymousNode
	for i := 0; i < 2; i++ {
		proxy := &proxyTransport{index: i, name: "public-test", public: true, client: &http.Client{Transport: send}}
		proxy.bun = proxy.client
		proxy.healthy.Store(true)
		nodes = append(nodes, &anonymousNode{proxy: proxy})
	}
	pool := &anonymousNodes{nodes: nodes, strict: true, blockedUntil: p.blockedUntil}
	p.active.Store(&proxyCleanSnapshot{pool: pool, expires: time.Now().Add(time.Hour)})
	p.candidates = nodes
	g.publicPool = p
	g.anonymous = &anonymousNodes{dynamic: p, cooldown: time.Second}
	g.cfg.Retry.SameAccountRetries = 2
	return g, p, nodes
}

func TestPublicRejectionRetryWaitCancellationBlocksNextRequest(t *testing.T) {
	for _, status := range []int{401, 403, 429} {
		calls := 0
		g, p, nodes := publicCancellationFixture(t, func(*http.Request) (*http.Response, error) {
			calls++
			resp := retryResponse(status)
			resp.Header.Set("Retry-After", "120")
			return resp, nil
		})
		// 401/403 also wait here; only 429 uses the upstream Retry-After itself.
		g.cfg.Retry.BackoffInitialMS = 1000
		ctx, cancel := context.WithCancel(context.Background())
		timer := time.AfterFunc(15*time.Millisecond, cancel)
		route := retryRoute()
		route.Anonymous = true
		resp, _, err := g.doUpstream(ctx, route, retryBodies(), identity.RequestIDs{})
		timer.Stop()
		cancel()
		if resp != nil || !errors.Is(err, context.Canceled) || calls != 1 {
			t.Fatalf("status=%d resp=%v err=%v calls=%d", status, resp, err, calls)
		}
		if p.blockedUntil.Load() <= time.Now().Add(time.Minute).UnixNano() || nodes[0].failures.Load() != 1 || nodes[1].failures.Load() != 0 {
			t.Fatalf("status=%d final rejection not recorded once", status)
		}
		if !nodes[0].proxy.healthy.Load() || !nodes[1].proxy.healthy.Load() {
			t.Fatal("cancellation marked a reachable proxy unhealthy")
		}
		resp, _, err = g.doUpstream(context.Background(), route, retryBodies(), identity.RequestIDs{})
		if resp != nil || !errors.Is(err, errProxyUnavailable) || calls != 1 {
			t.Fatalf("status=%d new request escaped public cooldown: resp=%v err=%v calls=%d", status, resp, err, calls)
		}
	}
}

func TestPublicCancellationWithoutRejectionDoesNotCoolLane(t *testing.T) {
	calls := 0
	g, p, nodes := publicCancellationFixture(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return retryResponse(200), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(15*time.Millisecond, cancel)
	route := retryRoute()
	route.Anonymous = true
	resp, _, err := g.doUpstream(ctx, route, retryBodies(), identity.RequestIDs{})
	timer.Stop()
	cancel()
	if resp != nil || !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("resp=%v err=%v calls=%d", resp, err, calls)
	}
	if p.blockedUntil.Load() != 0 {
		t.Fatal("ordinary cancellation cooled public lane")
	}
	for _, node := range nodes {
		if node.failures.Load() != 0 || node.cooldownUntil.Load() != 0 || !node.proxy.healthy.Load() {
			t.Fatal("ordinary cancellation changed node health")
		}
	}
	resp, _, err = g.doUpstream(context.Background(), route, retryBodies(), identity.RequestIDs{})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || calls != 2 {
		t.Fatalf("healthy next request blocked: status=%d calls=%d", resp.StatusCode, calls)
	}
}
