package gateway

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"opencode2api/internal/config"
)

func TestProxyCleanDisabledStatePersistsAcrossRefreshAndRestart(t *testing.T) {
	p, _ := fixtureProxyClean(t)
	path := filepath.Join(t.TempDir(), "proxyclean-disabled.json")
	store, err := loadProxyCleanDisabled(path)
	if err != nil {
		t.Fatal(err)
	}
	p.disabledStore = store
	p.refresh(context.Background())
	n := p.candidates[0]
	id := config.Fingerprint(n.proxy.name)
	oldCursor := p.current().CursorFor("")
	if err = p.setDisabled(id, true); err != nil {
		t.Fatal(err)
	}
	if got := oldCursor.Next(); got == n {
		t.Fatal("already issued cursor used disabled node")
	}
	p.refresh(context.Background())
	if !p.candidates[0].disabled() {
		t.Fatal("source refresh revived disabled node")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "8.8.8.8") || !strings.Contains(string(data), id) {
		t.Fatal("persisted state exposes endpoint or loses id")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("state not private")
	}
	restarted, _ := fixtureProxyClean(t)
	restarted.disabledStore, err = loadProxyCleanDisabled(path)
	if err != nil {
		t.Fatal(err)
	}
	restarted.refresh(context.Background())
	if !restarted.candidates[0].disabled() {
		t.Fatal("restart lost disabled flag")
	}
	until := time.Now().Add(time.Hour).UnixNano()
	restarted.blockedUntil.Store(until)
	if err = restarted.setDisabled(id, false); err != nil {
		t.Fatal(err)
	}
	if restarted.candidates[0].disabled() || restarted.current() != nil || restarted.blockedUntil.Load() != until {
		t.Fatal("enable did not preserve lane cooldown")
	}
	again, err := loadProxyCleanDisabled(path)
	if err != nil || again.disabled(id) {
		t.Fatal("enable did not persist", err)
	}
	if !errors.Is(restarted.setDisabled("unknown", true), ErrProxyCleanNodeNotFound) {
		t.Fatal("unknown node accepted")
	}
}

func TestProxyCleanPersistenceFailureDoesNotDisable(t *testing.T) {
	p, _ := fixtureProxyClean(t)
	p.refresh(context.Background())
	p.disabledStore.path = t.TempDir() // rename cannot replace a directory, even as root
	n := p.candidates[0]
	if !errors.Is(p.setDisabled(config.Fingerprint(n.proxy.name), true), ErrProxyCleanState) {
		t.Fatal("persistence failure hidden")
	}
	if n.disabled() {
		t.Fatal("failed persistence changed scheduling")
	}
}

func TestProxyCleanManualOperationsSingleFlightAndRuntimeCancellation(t *testing.T) {
	p, _ := fixtureProxyClean(t)
	off := false
	p.cfg.AutoCollect = &off
	p.cfg.RefreshSeconds = 1 // accelerate a scheduled tick without external I/O
	var calls atomic.Int32
	entered := make(chan struct{})
	exited := make(chan struct{})
	p.fetch = func(ctx context.Context) ([]string, error) {
		calls.Add(1)
		close(entered)
		<-ctx.Done()
		close(exited)
		return nil, ctx.Err()
	}
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.start(root)
	// Neither startup nor a scheduled tick may fetch when auto_collect is off.
	select {
	case <-entered:
		t.Fatal("auto_collect=false fetched on startup")
	case <-time.After(1100 * time.Millisecond):
	}
	manager := &RuntimeController{}
	manager.current.Store(&gatewayRuntime{gateway: &Gateway{publicPool: p}})
	if err := manager.ProxyCleanOperation("refresh"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("manual fetch not started")
	}
	if !errors.Is(manager.ProxyCleanOperation("probe"), ErrProxyCleanBusy) {
		t.Fatal("probe overlapped collection")
	}
	if manager.ProxyCleanNodes().Operation != "refresh" {
		t.Fatal("busy operation not visible")
	}
	// An unrelated request context cannot cancel runtime-owned work.
	request, requestCancel := context.WithCancel(context.Background())
	requestCancel()
	_ = request
	select {
	case <-exited:
		t.Fatal("request cancellation affected worker")
	default:
	}
	cancel()
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("runtime cancellation did not stop fetch")
	}
	p.opWG.Wait()
	if calls.Load() != 1 || !errors.Is(p.launch("refresh"), ErrProxyCleanUnavailable) {
		t.Fatal("stopped runtime accepted work")
	}
	manager.current.Store(&gatewayRuntime{gateway: &Gateway{}})
	if !errors.Is(manager.ProxyCleanOperation("probe"), ErrProxyCleanDisabled) || !errors.Is(manager.ProxyCleanSetDisabled("id", true), ErrProxyCleanDisabled) {
		t.Fatal("global disabled gate bypassed")
	}
}

func TestProxyCleanNodeViewsAndDisableRaceWithProbe(t *testing.T) {
	p, _ := fixtureProxyClean(t)
	p.fetch = func(context.Context) ([]string, error) { return []string{"socks5://8.8.8.8:1080"}, nil }
	p.refresh(context.Background())
	view := p.nodesView()
	if view.Total != 1 || len(view.Nodes[0].ID) != 16 || view.Nodes[0].Endpoint != "socks5://8.8.8.8:1080" || view.Nodes[0].LastProbeAt == nil || view.Nodes[0].LastProbeOK == nil || !*view.Nodes[0].LastProbeOK {
		t.Fatalf("missing operational fields: %+v", view)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	p.probe = func(context.Context, *proxyTransport, time.Duration) bool { close(entered); <-release; return true }
	go func() { p.check(context.Background()); close(done) }()
	<-entered
	if err := p.setDisabled(view.Nodes[0].ID, true); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-done
	if !p.nodesView().Nodes[0].Disabled || p.current().Len() != 0 {
		t.Fatal("in-flight probe revived disabled node")
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				p.nodesView()
				p.current()
			}
		}()
	}
	for i := 0; i < 100; i++ {
		if err := p.setDisabled(view.Nodes[0].ID, i%2 == 0); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}

func TestProxyCleanOperationalSwitchHotUpdateKeepsState(t *testing.T) {
	p, _ := fixtureProxyClean(t)
	p.refresh(context.Background())
	n := p.candidates[0]
	id := config.Fingerprint(n.proxy.name)
	if err := p.setDisabled(id, true); err != nil {
		t.Fatal(err)
	}
	p.blockedUntil.Store(time.Now().Add(time.Minute).UnixNano())
	cfg := proxyCleanTestConfig()
	off := false
	cfg.ProxyClean.AutoCollect = &off
	cfg.ProxyClean.AutoSwitchProxy = &off
	next, err := newProxyCleanPool(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer next.closeIdle()
	next.inherit(p)
	if next.active.Load() != p.active.Load() || !next.candidates[0].disabled() || next.current() != nil {
		t.Fatal("operational switch reset state or cooldown")
	}
}
