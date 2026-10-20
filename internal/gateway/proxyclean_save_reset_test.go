package gateway

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

func TestConfigSaveBuildClearsOnlyPublicSnapshot(t *testing.T) {
	cfg := proxyCleanTestConfig()
	off := false
	cfg.ProxyClean.AutoCollect = &off
	m := &RuntimeController{logger: slog.Default()}
	first, err := m.build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer first.gateway.publicPool.closeIdle()
	fixture, _ := fixtureProxyClean(t)
	fixture.refresh(context.Background())
	old := first.gateway.publicPool
	old.candidates = fixture.candidates
	old.lastGood = fixture.lastGood
	old.active.Store(fixture.active.Load())
	if old.current().Len() == 0 {
		t.Fatal("fixture empty")
	}
	m.current.Store(first)
	until := time.Now().Add(time.Hour).UnixNano()
	old.blockedUntil.Store(until)
	old.collector.shared.mu.Lock()
	old.collector.shared.nextFetch["proxyclean"] = time.Now().Add(time.Hour)
	old.collector.shared.inFlight["proxyclean"] = true
	old.collector.shared.mu.Unlock()
	regular, err := m.build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer regular.gateway.publicPool.closeIdle()
	if regular.gateway.publicPool.active.Load() != old.active.Load() {
		t.Fatal("ordinary apply lost snapshot")
	}
	next, err := m.build(cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	defer next.gateway.publicPool.closeIdle()
	p := next.gateway.publicPool
	if p.current().Len() != 0 || len(p.candidates) != 0 || !p.lastGood.IsZero() {
		t.Fatal("save inherited old nodes or evidence")
	}
	if p.blockedUntil != old.blockedUntil || p.blockedUntil.Load() != until || p.geo != old.geo || p.disabledStore != old.disabledStore || p.collector.shared != old.collector.shared {
		t.Fatal("save reset safety state")
	}
	if !p.collector.shared.inFlight["proxyclean"] {
		t.Fatal("inflight source budget reset")
	}
	if old.active.Load() == nil || len(old.candidates) == 0 {
		t.Fatal("build modified active runtime before persistence")
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.start(ctx)
	cancel()
	m.configPath = t.TempDir() // A config file cannot replace an existing directory.
	if _, err := m.SaveConfig(cfg); err == nil {
		t.Fatal("persist failure unexpectedly succeeded")
	}
	if m.current.Load() != first || old.active.Load() == nil {
		t.Fatal("failed persistence cleared active pool")
	}
	// Invalid saves leave the previous runtime published.
	invalid := cfg
	invalid.ProxyClean.RequiredExitCountry = "invalid"
	if _, err := m.SaveConfig(invalid); err == nil {
		t.Fatal("invalid save succeeded")
	}
	if m.current.Load() != first || old.active.Load() == nil {
		t.Fatal("invalid save cleared active pool")
	}
}
