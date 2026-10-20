package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"testing"
	"time"
)

func TestProxyCleanGlobalTogglePreservesSourceCooldown(t *testing.T) {
	cfg := proxyCleanTestConfig()
	manager := &RuntimeController{logger: slog.Default()}
	first, err := manager.build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer first.gateway.publicPool.closeIdle()
	manager.current.Store(first)
	shared := first.gateway.publicPool.collector.shared
	until := time.Now().Add(time.Hour)
	shared.mu.Lock()
	shared.nextFetch["proxyclean"] = until
	shared.statuses["proxyclean"] = ProxyCleanSourceStatus{ID: "proxyclean", Status: "error", LastError: "source_http_429"}
	shared.mu.Unlock()
	cfg.ProxyClean.Enabled = false
	disabled, err := manager.build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	manager.current.Store(disabled)
	cfg.ProxyClean.Enabled = true
	cfg.ProxyClean.TTLSeconds++
	next, err := manager.build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer next.gateway.publicPool.closeIdle()
	collector := next.gateway.publicPool.collector
	if collector.shared != shared {
		t.Fatal("global toggle replaced source state")
	}
	calls := 0
	collector.client = &http.Client{Transport: proxyCleanRoundTrip(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("unexpected fetch") })}
	_, _ = collector.fetch(context.Background())
	if calls != 0 || collector.view()[0].LastError != "source_http_429" {
		t.Fatal("global toggle bypassed source Retry-After")
	}
	// A late response still belongs to the shared source admission state.
	later := until.Add(time.Hour)
	shared.mu.Lock()
	shared.nextFetch["proxyclean"] = later
	shared.mu.Unlock()
	collector.shared.mu.Lock()
	observed := collector.shared.nextFetch["proxyclean"]
	collector.shared.mu.Unlock()
	if !observed.Equal(later) {
		t.Fatal("late source restriction lost")
	}
}
