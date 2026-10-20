package gateway

import (
	"log/slog"
	"testing"
	"time"
)

func TestProxyCleanGlobalDisableEnablePreservesLaneCooldown(t *testing.T) {
	cfg := proxyCleanTestConfig()
	manager := &RuntimeController{logger: slog.Default()}
	first, err := manager.build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer first.gateway.publicPool.closeIdle()
	manager.current.Store(first)
	until := time.Now().Add(time.Hour).UnixNano()
	first.gateway.publicPool.blockedUntil.Store(until)
	cfg.ProxyClean.Enabled = false
	disabled, err := manager.build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	manager.current.Store(disabled)
	cfg.ProxyClean.Enabled = true
	next, err := manager.build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer next.gateway.publicPool.closeIdle()
	if next.gateway.publicPool.blockedUntil.Load() != until || next.gateway.publicPool.current() != nil {
		t.Fatal("global toggle cleared lane cooldown")
	}
}
