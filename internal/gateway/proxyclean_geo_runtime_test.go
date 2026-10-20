package gateway

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"
)

func TestProxyCleanControllerTogglesPreserveGeoBudget(t *testing.T) {
	for _, toggle := range []string{"public_pool", "anonymous_lane"} {
		t.Run(toggle, func(t *testing.T) {
			cfg := proxyCleanTestConfig()
			cfg.ProxyClean.RequiredExitCountry = "CN"
			manager := &RuntimeController{logger: slog.Default()}
			first, err := manager.build(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer first.gateway.publicPool.closeIdle()
			manager.current.Store(first)
			geo := first.gateway.publicPool.geo
			now := time.Now()
			geo.now = func() time.Time { return now }
			calls := 0
			geo.fetch = func(context.Context, string) (string, time.Duration, error) {
				calls++
				if calls == 1 {
					return "CN", 0, nil
				}
				return "", time.Hour, errors.New("service 429")
			}
			if _, err := geo.lookup(context.Background(), "8.8.4.4"); err != nil {
				t.Fatal(err)
			}
			now = now.Add(2 * time.Second)
			if _, err := geo.lookup(context.Background(), "9.9.9.9"); err == nil {
				t.Fatal("429 fixture accepted")
			}
			until := geo.next
			if toggle == "public_pool" {
				cfg.ProxyClean.Enabled = false
			} else {
				cfg.Anonymous = false
			}
			disabled, err := manager.build(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if disabled.gateway.publicPool != nil {
				t.Fatal("fixture did not disable public pool")
			}
			manager.current.Store(disabled)
			cfg.ProxyClean.Enabled, cfg.Anonymous = true, true
			cfg.ProxyClean.TTLSeconds++ // policy change still cannot reset service budget
			next, err := manager.build(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer next.gateway.publicPool.closeIdle()
			restored := next.gateway.publicPool.geo
			if restored != geo || manager.proxyCleanGeo != geo {
				t.Fatal("toggle replaced controller geo state")
			}
			if country, err := restored.lookup(context.Background(), "8.8.4.4"); err != nil || country != "CN" {
				t.Fatal("toggle discarded IP cache")
			}
			if _, err := restored.lookup(context.Background(), "1.1.1.1"); err == nil || calls != 2 || !restored.next.Equal(until) {
				t.Fatal("toggle bypassed service Retry-After")
			}
			// Late service feedback from the old worker also updates the new runtime.
			geo.next = until.Add(time.Hour)
			if !restored.next.Equal(geo.next) {
				t.Fatal("late geo cooldown lost")
			}
			// Shared singleflight admission remains context-cancellable across toggles.
			geo.gate <- struct{}{}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := restored.lookup(ctx, "1.1.1.1"); err == nil {
				t.Fatal("shared geo gate ignored cancellation")
			}
			<-geo.gate
		})
	}
}
