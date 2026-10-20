package gateway

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"opencode2api/internal/config"
)

func TestProxyCleanRefillThrottleAndBusinessRestrictions(t *testing.T) {
	p, now := fixtureProxyClean(t)
	p.cfg.MinHealthyNodes = 2
	p.refresh(context.Background())
	p.opContext = context.Background()
	var calls atomic.Int32
	p.fetch = func(context.Context) ([]string, error) { calls.Add(1); return nil, errors.New("fixture failure") }
	// Business cooldown alone must not imply transport failure.
	for _, n := range p.candidates {
		n.cooldownUntil.Store(now.Add(time.Hour).UnixNano())
	}
	*now = now.Add(301 * time.Second)
	if p.maybeRefill() {
		t.Fatal("business cooldown drove proxy collection")
	}
	n := p.candidates[1]
	if err := p.setDisabled(config.Fingerprint(n.proxy.name), true); err != nil {
		t.Fatal(err)
	}
	if !p.maybeRefill() {
		t.Fatal("low watermark did not refill")
	}
	p.opWG.Wait()
	attempted := p.lastCollectAttempt.Load()
	if calls.Load() != 1 || attempted != now.UnixNano() {
		t.Fatal("failed attempt not recorded")
	}
	if p.maybeRefill() {
		t.Fatal("failed source caused tight refill loop")
	}
	*now = now.Add(299 * time.Second)
	if p.maybeRefill() {
		t.Fatal("refill minimum interval shortened")
	}
	*now = now.Add(2 * time.Second)
	off := false
	p.cfg.AutoCollect = &off
	if p.maybeRefill() {
		t.Fatal("auto_collect=false triggered refill")
	}
	p.cfg.AutoCollect = nil
	p.blockedUntil.Store(now.Add(time.Hour).UnixNano())
	if p.maybeRefill() {
		t.Fatal("lane restriction drove exit refill")
	}
	*now = now.Add(time.Hour + time.Second)
	if !p.maybeRefill() {
		t.Fatal("refill did not recover after interval and gate expiry")
	}
	p.opWG.Wait()
	if calls.Load() != 2 || !n.disabled() {
		t.Fatal("refill revived manually disabled node")
	}
}

func TestProxyCleanRefillAndManualOperationsShareSingleFlight(t *testing.T) {
	p, now := fixtureProxyClean(t)
	p.cfg.MinHealthyNodes = 2
	p.opContext = context.Background()
	p.refresh(context.Background())
	p.candidates[0].proxy.healthy.Store(false)
	*now = now.Add(301 * time.Second)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	p.fetch = func(context.Context) ([]string, error) {
		calls.Add(1)
		close(entered)
		<-release
		return nil, errors.New("fixture")
	}
	if !p.maybeRefill() {
		t.Fatal("refill did not start")
	}
	<-entered
	if !errors.Is(p.launch("probe"), ErrProxyCleanBusy) || !errors.Is(p.launch("refresh"), ErrProxyCleanBusy) {
		t.Fatal("manual operation overlapped automatic refill")
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if p.maybeRefill() {
				t.Error("concurrent refill admitted")
			}
		}()
	}
	wg.Wait()
	if v := p.view(); v.Operation != "refresh" {
		t.Fatal("running operation not visible")
	}
	close(release)
	p.opWG.Wait()
	if calls.Load() != 1 || p.maybeRefill() {
		t.Fatal("fast failure bypassed attempt interval")
	}
}

func TestProxyCleanHealthStatusAndProbeMetadata(t *testing.T) {
	p, _ := fixtureProxyClean(t)
	p.refresh(context.Background())
	n := p.candidates[1]
	p.probe = func(_ context.Context, proxy *proxyTransport, _ time.Duration) bool { return proxy != n.proxy }
	p.check(context.Background())
	v := p.view()
	if v.Total != 2 || v.Healthy != 1 || v.Cooling != 1 || v.Disabled != 0 || v.LastProbeAt == nil || v.NextProbeAt == nil {
		t.Fatalf("incorrect status: %+v", v)
	}
	p.blockedUntil.Store(time.Now().Add(time.Minute).UnixNano())
	if v = p.view(); v.Healthy != 1 || v.HealthyCount != 0 || v.LastError != "upstream_restricted" {
		t.Fatal("lane gate corrupted transport statistics", v)
	}
	nodes := p.nodesView()
	if nodes.Nodes[1].ConsecutiveFailures != 1 || nodes.Nodes[1].ProbeLatencyMS == nil || *nodes.Nodes[1].ProbeLatencyMS < 0 {
		t.Fatal("missing probe latency/failure metadata")
	}
	if err := p.setDisabled(config.Fingerprint(p.candidates[0].proxy.name), true); err != nil {
		t.Fatal(err)
	}
	if v = p.view(); v.Disabled != 1 || v.Healthy != 0 || v.Cooling != 1 {
		t.Fatal("disabled count wrong", v)
	}
}

func TestProxyCleanSchedulerAutoOffStillProbes(t *testing.T) {
	p, now := fixtureProxyClean(t)
	p.refresh(context.Background())
	off := false
	p.cfg.AutoCollect = &off
	p.cfg.MinHealthyNodes = 2
	p.opContext = context.Background()
	var fetches, probes atomic.Int32
	p.fetch = func(context.Context) ([]string, error) { fetches.Add(1); return nil, errors.New("unexpected fetch") }
	p.probe = func(context.Context, *proxyTransport, time.Duration) bool { probes.Add(1); return true }
	p.nextProbeAt.Store(now.Add(-time.Second).UnixNano())
	p.nextRefreshAt.Store(now.Add(-time.Second).UnixNano())
	p.schedule()
	p.opWG.Wait()
	if fetches.Load() != 0 || probes.Load() != 2 || p.view().NextRefreshAt != nil {
		t.Fatal("auto-off scheduled collection or stopped probes")
	}
	if !p.view().NextProbeAt.After(*now) {
		t.Fatal("next probe not advanced")
	}
}
