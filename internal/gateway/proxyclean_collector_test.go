package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"opencode2api/internal/config"
)

type collectorRoundTrip func(*http.Request) (*http.Response, error)

func (f collectorRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProxyCleanCollectorPartialFailureFairMerge(t *testing.T) {
	cfg := config.ProxyCleanConfig{Sources: []string{"proxyclean", "proxifly", "thespeedx"}, MaxCandidates: 3, MaxNodes: 3}
	cfg.Defaults()
	c := newProxyCleanCollector(cfg)
	c.client = &http.Client{Transport: collectorRoundTrip(func(r *http.Request) (*http.Response, error) {
		body := "8.8.8.8:1080\n1.1.1.1:1080\n9.9.9.9:1080\n"
		status := 200
		if r.URL.String() == proxyCleanSourceURLs["proxifly"] {
			body = "socks5://8.8.8.8:1080\nsocks5://208.67.222.222:1080\n"
		}
		if r.URL.String() == proxyCleanSourceURLs["thespeedx"] {
			status = 503
			body = "do not expose arbitrary upstream errors"
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	nodes, err := c.fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"socks5://8.8.8.8:1080", "socks5://1.1.1.1:1080", "socks5://208.67.222.222:1080"}
	if len(nodes) != len(want) {
		t.Fatal(nodes)
	}
	for i := range want {
		if nodes[i] != want[i] {
			t.Fatalf("nodes=%v want=%v", nodes, want)
		}
	}
	stats := c.view()
	if len(stats) != 3 || stats[0].Status != "ready" || stats[1].Status != "ready" || stats[2].Status != "error" || stats[2].LastError != "source_http_status" {
		t.Fatal(stats)
	}
	// Returned status slices must not allow callers to overwrite worker state.
	stats[0].Status = "changed"
	if c.view()[0].Status != "ready" {
		t.Fatal("shared status slice")
	}
}

func TestProxyCleanCollectorAllFailedAndCancellation(t *testing.T) {
	cfg := config.ProxyCleanConfig{Sources: []string{"hookzof", "gfpcom", "dpangestuw", "roosterkid"}}
	cfg.Defaults()
	c := newProxyCleanCollector(cfg)
	c.client = &http.Client{Transport: collectorRoundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("127.0.0.1:80\nhttp://8.8.8.8:80\n"))}, nil
	})}
	if nodes, err := c.fetch(context.Background()); err == nil || len(nodes) != 0 {
		t.Fatalf("%v %v", nodes, err)
	}
	for _, s := range c.view() {
		if s.Status != "empty" || s.CandidateCount != 0 {
			t.Fatal(s)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.fetch(ctx); err != context.Canceled {
		t.Fatalf("cancel=%v", err)
	}
}

func TestProxyCleanCollectorRetryAfter(t *testing.T) {
	cfg := config.ProxyCleanConfig{Sources: []string{"gfpcom"}}
	cfg.Defaults()
	c := newProxyCleanCollector(cfg)
	now := time.Now()
	c.now = func() time.Time { return now }
	calls := 0
	c.client = &http.Client{Transport: collectorRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"7200"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	_, _ = c.fetch(context.Background())
	_, _ = c.fetch(context.Background())
	if calls != 1 {
		t.Fatalf("rate limit ignored: %d", calls)
	}
	now = now.Add(2*time.Hour + time.Second)
	_, _ = c.fetch(context.Background())
	if calls != 2 {
		t.Fatal(calls)
	}
}

func TestProxyCleanLargeSourceBudget(t *testing.T) {
	large := strings.Repeat("socks5://8.8.8.8:1080\n", 110000)
	if _, err := parseProxyCleanList(strings.NewReader(large), 1); err == nil {
		t.Fatal("legacy budget unexpectedly accepted oversized list")
	}
	budget, _ := proxyCleanSourceBudget("gfpcom")
	nodes, err := parseProxyCleanListLimit(strings.NewReader(large), 1, budget)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("large source %v %v", nodes, err)
	}
}

func TestProxyCleanSourceRegistryIsHTTPSAllowlist(t *testing.T) {
	if len(proxyCleanSourceURLs) != len(config.ProxyCleanSourceIDs) {
		t.Fatal("registry mismatch")
	}
	for _, id := range config.ProxyCleanSourceIDs {
		u := proxyCleanSourceURLs[id]
		if !strings.HasPrefix(u, "https://raw.githubusercontent.com/") {
			t.Fatalf("unsafe source for %s", id)
		}
	}
}
