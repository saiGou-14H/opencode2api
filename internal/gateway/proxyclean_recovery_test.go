package gateway

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestProxyCleanDirectFallbackRecoversWithoutLiveSource(t *testing.T) {
	for _, expired := range []bool{false, true} {
		cfg := proxyCleanTestConfig()
		cfg.ProxyClean.AllowDirectFallback = true
		p, err := newProxyCleanPool(cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer p.closeIdle()
		now := time.Now()
		p.now = func() time.Time { return now }
		if expired {
			p.lastGood = now.Add(-time.Duration(p.cfg.TTLSeconds+1) * time.Second)
		}
		n := p.fallback.nodes[0]
		g := &Gateway{}
		g.syncProxyResult(context.Background(), n.proxy, 0, errors.New("fixture failure"))
		p.fallback.MarkFailure(n, nil, errors.New("fixture failure"))
		cursor := p.current().CursorFor("")
		if cursor.Next() != nil {
			t.Fatal("failed fallback remained eligible")
		}
		calls := 0
		p.probe = func(_ context.Context, proxy *proxyTransport, _ time.Duration) bool {
			calls++
			if proxy != n.proxy {
				t.Error("expired source candidate probed")
			}
			return true
		}
		p.check(context.Background())
		if calls != 0 {
			t.Fatal("fallback cooldown ignored")
		}
		now = now.Add(time.Duration(p.cfg.CooldownSeconds+1) * time.Second)
		p.check(context.Background())
		cursor = p.current().CursorFor("")
		if calls != 1 || cursor.Next() != n || !n.proxy.healthy.Load() || n.cooldownUntil.Load() != 0 {
			t.Fatalf("fallback never recovered: expired=%v calls=%d", expired, calls)
		}
	}
}

func TestProxyCleanProbeCannotOverwriteNewerBusinessResult(t *testing.T) {
	for _, businessSuccess := range []bool{false, true} {
		p, _ := fixtureProxyClean(t)
		p.fetch = func(context.Context) ([]string, error) { return []string{"socks5://8.8.8.8:1080"}, nil }
		p.refresh(context.Background())
		n := p.candidates[0]
		started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
		p.probe = func(context.Context, *proxyTransport, time.Duration) bool {
			close(started)
			<-release
			return !businessSuccess
		}
		go func() { p.check(context.Background()); close(done) }()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("probe did not start")
		}
		g := &Gateway{}
		lane := &anonymousNodes{dynamic: p, cooldown: time.Minute}
		if businessSuccess {
			g.syncProxyResult(context.Background(), n.proxy, 200, nil)
			lane.MarkSuccess(n)
		} else {
			g.syncProxyResult(context.Background(), n.proxy, 0, errors.New("newer business failure"))
			lane.MarkFailure(n, &http.Response{StatusCode: 500, Header: make(http.Header)}, nil)
		}
		failures, until, healthy := n.failures.Load(), n.cooldownUntil.Load(), n.proxy.healthy.Load()
		close(release)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("probe did not finish")
		}
		if n.failures.Load() != failures || n.cooldownUntil.Load() != until || n.proxy.healthy.Load() != healthy {
			t.Fatalf("stale probe overwrote newer business state (success=%v)", businessSuccess)
		}
	}
}

func TestProxyCleanPromotesVerifiedStandbyImmediately(t *testing.T) {
	p, _ := fixtureProxyClean(t)
	p.cfg.MaxNodes = 1
	calls := 0
	p.probe = func(context.Context, *proxyTransport, time.Duration) bool { return true }
	p.refresh(context.Background())
	a, b := p.candidates[0], p.candidates[1]
	cursor := p.current().CursorFor("")
	if cursor.Next() != a {
		t.Fatal("initial active was not A")
	}
	p.probe = func(context.Context, *proxyTransport, time.Duration) bool { calls++; return true }
	g := &Gateway{}
	g.syncProxyResult(context.Background(), a.proxy, 0, errors.New("A failed"))
	lane := &anonymousNodes{dynamic: p, cooldown: time.Minute}
	lane.MarkFailure(a, nil, errors.New("A failed"))
	cursor = p.current().CursorFor("")
	if cursor.Next() != b || p.current().Len() != 1 || calls != 0 {
		t.Fatal("verified B not promoted immediately without probing")
	}
	// Promoting a standby does not reset a cursor already issued to another request.
	if len(p.active.Load().pool.nodes) != 1 {
		t.Fatal("max_nodes violated")
	}
}
