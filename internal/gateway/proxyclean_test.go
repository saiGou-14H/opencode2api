package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"opencode2api/internal/config"
)

func TestProxyCleanParser(t *testing.T) {
	input := `socks5://8.8.8.8:1080 #US
8.8.8.8:01080 #duplicate
[2606:4700:4700::1111]:1080 #US
socks5://1.1.1.1:65535 #AU
localhost:1080
socks5://user:pass@8.8.8.8:80
http://8.8.8.8:80
8.8.8.8:0
8.8.8.8:65536
8.8.8.8:80/path
127.0.0.1:80
10.0.0.1:80
169.254.169.254:80
100.100.100.200:80
[::1]:80
[::ffff:8.8.8.8]:80
[fd00::1]:80
[fe80::1]:80
[2001:db8::1]:80
[2002:7f00:1::]:80
`
	got, err := parseProxyCleanList(strings.NewReader(input), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != "socks5://8.8.8.8:1080" || got[1] != "socks5://[2606:4700:4700::1111]:1080" {
		t.Fatalf("unexpected parse: %v", got)
	}
	got, err = parseProxyCleanList(strings.NewReader(input), 1)
	if err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	for _, ip := range []string{"0.0.0.0", "192.0.0.8", "192.0.2.1", "198.18.0.1", "198.51.100.1", "203.0.113.1", "224.0.0.1", "240.0.0.1", "255.255.255.255", "::", "64:ff9b::808:808", "2001::1", "3fff::1"} {
		if publicProxyIP(netip.MustParseAddr(ip)) {
			t.Errorf("accepted reserved %s", ip)
		}
	}
	if _, err := parseProxyCleanList(strings.NewReader("bad\n127.0.0.1:80"), 10); err == nil {
		t.Fatal("empty accepted")
	}
	if _, err := parseProxyCleanList(strings.NewReader(strings.Repeat("x", 5000)), 10); err == nil {
		t.Fatal("long line accepted")
	}
}

type proxyCleanRoundTrip func(*http.Request) (*http.Response, error)

func (f proxyCleanRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProxyCleanFetchAndHTTPS(t *testing.T) {
	c := config.ProxyCleanConfig{}
	c.Defaults()
	client := &http.Client{Transport: proxyCleanRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" || r.Body != nil || r.URL.String() != config.DefaultProxyCleanSource {
			t.Fatal("source leaked request data")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("8.8.8.8:1080 #US")), Header: make(http.Header)}, nil
	})}
	got, err := fetchProxyClean(context.Background(), c, client)
	if err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	client.Transport = proxyCleanRoundTrip(func(*http.Request) (*http.Response, error) { return nil, errors.New("secret URL must not escape") })
	if _, err := fetchProxyClean(context.Background(), c, client); err == nil || err.Error() != "source_fetch_failed" {
		t.Fatal(err)
	}
	called := false
	secure := proxyCleanHTTPS{proxyCleanRoundTrip(func(*http.Request) (*http.Response, error) { called = true; return nil, nil })}
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	if _, err := secure.RoundTrip(req); err == nil || called {
		t.Fatal("plaintext allowed")
	}
	if noProxyCleanRedirect(nil, nil) == nil {
		t.Fatal("redirect allowed")
	}
	// Default source TLS does not trust a locally generated certificate.
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer server.Close()
	raw := &http.Client{Timeout: time.Second}
	if resp, err := raw.Get(server.URL); err == nil {
		resp.Body.Close()
		t.Fatal("untrusted TLS accepted")
	}
}

func proxyCleanTestConfig() config.Config {
	c := config.Config{Anonymous: true, ZenKeys: []string{"paid-key"}, Performance: config.PerformanceConfig{ConnectTimeoutSeconds: 1}, Retry: config.RetryConfig{MaxAttempts: 2, TimeoutSeconds: 5}}
	c.ProxyClean.Defaults()
	c.ProxyClean.Enabled = true
	c.ProxyClean.MaxCandidates = 3
	c.ProxyClean.MaxNodes = 2
	c.ProxyClean.ProbeConcurrency = 2
	return c
}

func fixtureProxyClean(t *testing.T) (*proxyCleanPool, *time.Time) {
	t.Helper()
	p, err := newProxyCleanPool(proxyCleanTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	p.now = func() time.Time { return now }
	p.fetch = func(context.Context) ([]string, error) {
		return []string{"socks5://8.8.8.8:1080", "socks5://1.1.1.1:1080"}, nil
	}
	p.probe = func(context.Context, *proxyTransport, time.Duration) bool { return true }
	t.Cleanup(p.closeIdle)
	return p, &now
}

func TestProxyCleanLastGoodTTLAndRecovery(t *testing.T) {
	p, now := fixtureProxyClean(t)
	p.refresh(context.Background())
	if p.current().Len() != 2 || p.view().SourceStatus != "ready" {
		t.Fatal(p.view())
	}
	expiry := *p.view().ExpiresAt
	p.fetch = func(context.Context) ([]string, error) { return nil, errors.New("secret") }
	p.refresh(context.Background())
	if p.current().Len() != 2 || p.view().SourceStatus != "degraded" || !p.view().ExpiresAt.Equal(expiry) {
		t.Fatal(p.view())
	}
	*now = expiry.Add(time.Second)
	if p.current().Len() != 0 || p.view().SourceStatus != "expired" {
		t.Fatal(p.view())
	}
	// Refresh succeeds after expiration; source presence alone is not health.
	p.fetch = func(context.Context) ([]string, error) { return []string{"socks5://9.9.9.9:1080"}, nil }
	p.probe = func(context.Context, *proxyTransport, time.Duration) bool { return false }
	p.refresh(context.Background())
	if p.current().Len() != 0 {
		t.Fatal("failed probe entered pool")
	}
	p.probe = func(context.Context, *proxyTransport, time.Duration) bool { return true }
	p.check(context.Background())
	if p.current().Len() != 0 {
		t.Fatal("cooldown ignored")
	}
	*now = now.Add(time.Duration(p.cfg.CooldownSeconds+1) * time.Second)
	p.check(context.Background())
	if p.current().Len() != 1 {
		t.Fatal("node did not recover")
	}
}

func TestProxyCleanStrictPoolIsolationAndHotUpdate(t *testing.T) {
	cfg := proxyCleanTestConfig()
	g, err := New(cfg, slog.Default(), nil)
	if err != nil {
		t.Fatal(err)
	}
	emptyCursor := g.anonymous.CursorFor("")
	if emptyCursor.Next() != nil {
		t.Fatal("empty pool silently used static route")
	}
	if g.catalogAnonymous.Len() != len(g.transports.items) || g.zenNodes.Proxy(g.zenNodes.nodes[0]).public {
		t.Fatal("paid/catalog contaminated")
	}
	p, _ := fixtureProxyClean(t)
	p.refresh(context.Background())
	g.anonymous.dynamic = p
	cursor := g.anonymous.CursorFor("")
	n := cursor.Next()
	if n == nil || !n.proxy.public {
		t.Fatal("missing anonymous public node")
	}
	binding := g.zenNodes.Proxy(g.zenNodes.nodes[0])
	g.syncProxyResult(context.Background(), n.proxy, 0, errors.New("network failed"))
	if binding != g.zenNodes.Proxy(g.zenNodes.nodes[0]) || !binding.healthy.Load() {
		t.Fatal("public failure altered static paid route")
	}
	n.cooldownUntil.Store(time.Now().Add(time.Hour).UnixNano())
	c := anonymousCursor{pool: &anonymousNodes{nodes: []*anonymousNode{n}, strict: true}}
	if c.Next() != nil {
		t.Fatal("strict pool used cooling/unhealthy node")
	}
	replacement, err := newProxyCleanPool(cfg)
	if err != nil {
		t.Fatal(err)
	}
	replacement.inherit(p)
	if replacement.active.Load() != p.active.Load() {
		t.Fatal("unrelated update discarded snapshot")
	}
	cfg.ProxyClean.Enabled = false
	disabled, err := New(cfg, slog.Default(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.publicPool != nil || disabled.anonymous.dynamic != nil || disabled.anonymous.Len() != len(disabled.transports.items) {
		t.Fatal("disable did not restore original static routing")
	}
	// A cursor already issued on the old snapshot is unaffected by disabling.
	if cursor.pool == nil {
		t.Fatal("old request snapshot lost")
	}
}

func TestProxyCleanCancellationConcurrencyAndAtomicReaders(t *testing.T) {
	p, _ := fixtureProxyClean(t)
	var running, peak atomic.Int32
	p.probe = func(ctx context.Context, _ *proxyTransport, _ time.Duration) bool {
		n := running.Add(1)
		defer running.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		<-ctx.Done()
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.refresh(ctx); close(done) }()
	deadline := time.After(time.Second)
	for running.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("probes not started")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel hung")
	}
	if peak.Load() > int32(p.cfg.ProbeConcurrency) || p.current().Len() != 0 {
		t.Fatal("concurrency/cancel violated")
	}
	p.probe = func(context.Context, *proxyTransport, time.Duration) bool { return true }
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				p.current().Len()
				p.view()
			}
		}()
	}
	for i := 0; i < 10; i++ {
		p.refresh(context.Background())
	}
	wg.Wait()
}

func TestProxyCleanExplicitDirectFallback(t *testing.T) {
	cfg := proxyCleanTestConfig()
	cfg.ProxyClean.AllowDirectFallback = true
	p, err := newProxyCleanPool(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.closeIdle()
	c := p.current().CursorFor("")
	n := c.Next()
	if n == nil || n.proxy.name != "direct" {
		t.Fatal("explicit fallback unavailable")
	}
}
