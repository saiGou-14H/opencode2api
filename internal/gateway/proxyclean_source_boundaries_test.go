package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"opencode2api/internal/config"
)

func TestProxyCleanExactByteBudgets(t *testing.T) {
	for _, body := range []string{"8.8.8.8:1080", "8.8.8.8:1080\n", "8.8.8.8:1080\r\n", "8.8.8.8:1080\r\n1.1.1.1:1080\r\n"} {
		got, err := parseProxyList(strings.NewReader(body), 10, int64(len(body)), "socks5", false)
		if err != nil || len(got) == 0 {
			t.Fatalf("exact %q %v", body, err)
		}
		_, err = parseProxyList(strings.NewReader(body), 10, int64(len(body)-1), "socks5", false)
		if err == nil || err.Error() != "source_too_large" {
			t.Fatalf("overflow %q %v", body, err)
		}
	}
}
func TestProxyCleanSourceCooldownSurvivesHotUpdate(t *testing.T) {
	cfg := config.ProxyCleanConfig{Sources: []string{"proxyclean"}}
	cfg.Defaults()
	old := newProxyCleanCollector(cfg)
	now := time.Now()
	old.now = func() time.Time { return now }
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	old.client = &http.Client{Transport: collectorRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		close(entered)
		<-release
		return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"7200"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	done := make(chan struct{})
	go func() { _, _ = old.fetch(context.Background()); close(done) }()
	<-entered
	next := newProxyCleanCollector(cfg)
	next.inherit(old)
	next.now = old.now
	next.client = &http.Client{Transport: collectorRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("8.8.8.8:1080"))}, nil
	})}
	_, _ = next.fetch(context.Background())
	if calls.Load() != 1 {
		t.Fatal("overlapping old/new source request")
	}
	close(release)
	<-done
	_, _ = next.fetch(context.Background())
	if calls.Load() != 1 {
		t.Fatal("hot update lost late Retry-After")
	}
	if next.view()[0].LastError != "source_http_status" {
		t.Fatal("source status not inherited")
	}
	now = now.Add(2*time.Hour + time.Second)
	if _, err := next.fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal(calls.Load())
	}
}
