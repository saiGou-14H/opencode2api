package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func cnFixture(t *testing.T, country string) (*proxyCleanPool, *time.Time) {
	t.Helper()
	p, now := fixtureProxyClean(t)
	p.cfg.RequiredExitCountry = "CN"
	p.observeExit = func(context.Context, *proxyTransport) (string, error) { return "8.8.4.4", nil }
	p.geo.fetch = func(context.Context, string) (string, time.Duration, error) { return country, 0, nil }
	return p, now
}

func TestProxyCleanCNAdmission(t *testing.T) {
	for _, country := range []string{"CN", "HK", "MO", "TW", "US", ""} {
		t.Run(country, func(t *testing.T) {
			p, _ := cnFixture(t, country)
			p.refresh(context.Background())
			cursor := p.current().CursorFor("")
			accepted := cursor.Next() != nil
			if accepted != (country == "CN") {
				t.Fatalf("country %q admitted=%v", country, accepted)
			}
			if p.fallback != nil {
				t.Fatal("CN direct fallback")
			}
			view := p.nodesView()
			if view.Nodes[0].Healthy != accepted {
				t.Fatal("status bypassed evidence")
			}
			if country != "" && (view.Nodes[0].ExitIP != "8.8.4.4" || view.Nodes[0].ExitCountry != country || view.Nodes[0].ExitCheckedAt == nil) {
				t.Fatal("missing safe evidence")
			}
		})
	}
}

func TestProxyCleanCNEvidenceExpiresAndFailureClears(t *testing.T) {
	p, now := cnFixture(t, "CN")
	p.refresh(context.Background())
	old := p.current()
	cursor := old.CursorFor("")
	n := p.candidates[0]
	expired := *n.exitEvidence.Load()
	expired.Expires = time.Now().Add(-time.Second)
	n.exitEvidence.Store(&expired)
	// An already-issued cursor must not retain admission after expiration.
	old.nodes = old.nodes[:1]
	if cursor.Next() != nil {
		t.Fatal("cursor reused expired evidence")
	}
	*now = now.Add(11 * time.Minute)
	if p.current().Len() != 0 || p.view().Healthy != 0 {
		t.Fatal("expired evidence still published")
	}
	p.check(context.Background())
	if p.current().Len() == 0 {
		t.Fatal("fresh probe did not recover")
	}
	p.observeExit = func(context.Context, *proxyTransport) (string, error) { return "", errors.New("unknown") }
	p.check(context.Background())
	if p.current().Len() != 0 || n.exitEvidence.Load() != nil {
		t.Fatal("failed echo retained evidence")
	}
	// Business success cannot manufacture a replacement proof.
	(&Gateway{}).syncProxyResult(context.Background(), n.proxy, 200, nil)
	(&anonymousNodes{}).MarkSuccess(n)
	p.mu.Lock()
	p.publishLocked()
	p.mu.Unlock()
	if p.current().Len() != 0 {
		t.Fatal("business success bypassed country gate")
	}
}

func TestProxyCleanCNRequiresTargetAndNewProbeGeneration(t *testing.T) {
	p, _ := cnFixture(t, "CN")
	var echoes atomic.Int32
	p.observeExit = func(context.Context, *proxyTransport) (string, error) { echoes.Add(1); return "8.8.4.4", nil }
	p.probe = func(context.Context, *proxyTransport, time.Duration) bool { return false }
	p.refresh(context.Background())
	if echoes.Load() != 0 || p.current().Len() != 0 {
		t.Fatal("target failure admitted")
	}
	for _, n := range p.candidates {
		n.cooldownUntil.Store(0)
	}
	p.probe = func(context.Context, *proxyTransport, time.Duration) bool { return true }
	p.check(context.Background())
	started, release := make(chan struct{}), make(chan struct{})
	p.cfg.ProbeConcurrency = 1
	p.candidates = p.candidates[:1]
	p.observeExit = func(context.Context, *proxyTransport) (string, error) {
		close(started)
		<-release
		return "8.8.4.4", nil
	}
	done := make(chan struct{})
	go func() { p.check(context.Background()); close(done) }()
	<-started
	n := p.candidates[0]
	(&Gateway{}).syncProxyResult(context.Background(), n.proxy, 200, nil)
	(&anonymousNodes{}).MarkSuccess(n)
	close(release)
	<-done
	if n.exitEvidence.Load() != nil || p.current().Len() != 0 {
		t.Fatal("stale probe restored country admission")
	}
}

func TestProxyCleanCountryHotApplyPreservesGateNotSnapshots(t *testing.T) {
	old, _ := fixtureProxyClean(t)
	old.refresh(context.Background())
	old.blockedUntil.Store(time.Now().Add(time.Minute).UnixNano())
	cfg := proxyCleanTestConfig()
	cfg.ProxyClean.RequiredExitCountry = "CN"
	p, err := newProxyCleanPool(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.closeIdle()
	p.inherit(old)
	if p.active.Load() != nil || len(p.candidates) != 0 {
		t.Fatal("country switch inherited unverified nodes")
	}
	if p.blockedUntil != old.blockedUntil || p.disabledStore != old.disabledStore {
		t.Fatal("country switch lost lane/disable state")
	}
}

func TestProxyCleanExitEchoBoundaries(t *testing.T) {
	for _, body := range []string{`{"ip":"8.8.4.4"}`, `{"ip":"127.0.0.1"}`, `{"ip":"host/path"}`, `{"ip":"::ffff:8.8.8.8"}`, `{"ip":"8.8.4.4"} {}`, strings.Repeat("x", 4097)} {
		p := &proxyTransport{bun: &http.Client{Transport: proxyCleanRoundTrip(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != proxyCleanExitURL || r.Header.Get("Authorization") != "" || r.Body != nil {
				t.Fatal("echo request leaked input")
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}}
		_, err := observeProxyCleanExit(context.Background(), p)
		if (err == nil) != (body == `{"ip":"8.8.4.4"}`) {
			t.Errorf("echo accepted %q", body)
		}
	}
}

func TestProxyCleanGeoValidationAndRetry(t *testing.T) {
	for _, body := range []string{`{"success":true,"ip":"8.8.4.4","country_code":"CN"}`, `{"success":false,"ip":"8.8.4.4","country_code":"CN"}`, `{"success":true,"ip":"1.1.1.1","country_code":"CN"}`, `{"country_code":"CN"}`, strings.Repeat("x", 4097)} {
		client := &http.Client{Transport: proxyCleanRoundTrip(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != proxyCleanGeoURL+"8.8.4.4" || r.Header.Get("Authorization") != "" {
				t.Fatal("unsafe geo request")
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}
		_, _, err := fetchProxyCleanGeo(context.Background(), client, "8.8.4.4")
		if (err == nil) != strings.HasPrefix(body, `{"success":true,"ip":"8.8.4.4"`) {
			t.Errorf("geo validation: %q", body)
		}
	}
	client := &http.Client{Transport: proxyCleanRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"120"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	_, delay, err := fetchProxyCleanGeo(context.Background(), client, "8.8.4.4")
	if err == nil || delay != 120*time.Second {
		t.Fatal("lost geo retry-after")
	}
	secure := proxyCleanSourceClient()
	if secure.Transport.(*http.Transport).Proxy != nil || secure.CheckRedirect == nil || secure.Timeout <= 0 {
		t.Fatal("unsafe direct geo client")
	}
}

func TestProxyCleanGeoSingleflightCacheBoundsAndBackoff(t *testing.T) {
	g := newProxyCleanGeo()
	now := time.Now()
	g.now = func() time.Time { return now }
	var calls atomic.Int32
	g.fetch = func(context.Context, string) (string, time.Duration, error) { calls.Add(1); return "CN", 0, nil }
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c, e := g.lookup(context.Background(), "8.8.4.4"); e != nil || c != "CN" {
				t.Error("coalesced lookup failed")
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("same IP was not singleflight")
	}
	now = now.Add(11 * time.Minute)
	if _, err := g.lookup(context.Background(), "8.8.4.4"); err != nil || calls.Load() != 2 {
		t.Fatal("cache did not expire")
	}
	// Start with exact capacity (normal insertion never exceeds this).
	g.cache = make(map[string]proxyCleanGeoEntry)
	for i := 0; i < 1024; i++ {
		g.cache[time.Unix(int64(i), 0).String()] = proxyCleanGeoEntry{}
	}
	now = now.Add(time.Minute)
	g.lookup(context.Background(), "1.1.1.1")
	if len(g.cache) > 1024 {
		t.Fatal("unbounded cache")
	}
	now = now.Add(time.Minute)
	g.fetch = func(context.Context, string) (string, time.Duration, error) {
		calls.Add(1)
		return "", 120 * time.Second, errors.New("429")
	}
	g.lookup(context.Background(), "9.9.9.9")
	before := calls.Load()
	if _, err := g.lookup(context.Background(), "8.8.8.8"); err == nil || calls.Load() != before {
		t.Fatal("service retry-after ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g.gate <- struct{}{}
	if _, err := g.lookup(ctx, "8.8.8.8"); err == nil {
		t.Fatal("gate ignored cancellation")
	}
	<-g.gate
}

func TestProxyCleanCNRestrictionGateSurvivesSuccessfulExitCheck(t *testing.T) {
	for _, status := range []int{401, 403, 429} {
		p, now := cnFixture(t, "CN")
		p.refresh(context.Background())
		lane := &anonymousNodes{dynamic: p, cooldown: time.Minute}
		cursor := lane.CursorFor("")
		node := cursor.Next()
		lane.MarkFailure(node, &http.Response{StatusCode: status, Header: http.Header{"Retry-After": []string{"600"}}}, nil)
		until := p.blockedUntil.Load()
		*now = now.Add(481 * time.Second)
		p.refresh(context.Background())
		if p.blockedUntil.Load() != until || p.current() != nil || cursor.Next() != nil {
			t.Fatalf("CN probe bypassed restriction %d", status)
		}
	}
}

func TestProxyCleanCNConcurrentProbeCannotRestoreOlderEvidence(t *testing.T) {
	p, _ := cnFixture(t, "CN")
	p.fetch = func(context.Context) ([]string, error) { return []string{"socks5://8.8.8.8:1080"}, nil }
	p.refresh(context.Background())
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	p.observeExit = func(context.Context, *proxyTransport) (string, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return "8.8.4.4", nil
		}
		return "", errors.New("newer failed check")
	}
	go func() { p.check(context.Background()); close(done) }()
	<-started
	p.check(context.Background())
	close(release)
	<-done
	if p.candidates[0].exitEvidence.Load() != nil || p.current().Len() != 0 {
		t.Fatal("older passing check erased newer failed check")
	}
}

func TestProxyCleanCNUnavailableStatusAndRestrictionPriority(t *testing.T) {
	for _, country := range []string{"", "HK", "MO", "TW", "CN"} {
		p, _ := cnFixture(t, country)
		if p.view().LastError != "" {
			t.Fatal("loading incorrectly reports country failure")
		}
		p.refresh(context.Background())
		if got := p.view().LastError; (got == "exit_country_unavailable") != (country != "CN") {
			t.Fatalf("country=%q last_error=%q", country, got)
		}
		p.blockedUntil.Store(time.Now().Add(time.Minute).UnixNano())
		if p.view().LastError != "upstream_restricted" {
			t.Fatal("country error hid restriction")
		}
	}
}

func TestProxyCleanGeoIdleLifecycleSharedAcrossApply(t *testing.T) {
	old, _ := cnFixture(t, "CN")
	old.refresh(context.Background())
	var closed atomic.Int32
	old.geo.closeIdle = func() { closed.Add(1) }
	cfg := proxyCleanTestConfig()
	cfg.ProxyClean.RequiredExitCountry = "CN"
	p, err := newProxyCleanPool(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p.inherit(old)
	if p.geo != old.geo {
		t.Fatal("hot apply discarded shared geo client/cache")
	}
	old.closeIdle()
	if closed.Load() != 1 {
		t.Fatal("pool shutdown leaked geo idle connections")
	}
	// Closing idle sockets must not discard proof cache or global backoff.
	p.geo.fetch = func(context.Context, string) (string, time.Duration, error) {
		t.Fatal("cache lost on idle close")
		return "", 0, nil
	}
	if country, err := p.geo.lookup(context.Background(), "8.8.4.4"); err != nil || country != "CN" {
		t.Fatal("shared cache no longer usable")
	}
	p.closeIdle()
	if closed.Load() != 2 {
		t.Fatal("replacement pool did not release idle sockets")
	}
}
