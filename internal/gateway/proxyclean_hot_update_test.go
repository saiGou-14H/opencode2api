package gateway

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"opencode2api/internal/config"
)

func TestProxyCleanAutoCollectOffKeepsOriginalTTLAndManualRefresh(t *testing.T) {
	old, now := fixtureProxyClean(t)
	old.refresh(context.Background())
	snapshot := old.active.Load()
	expiry := snapshot.expires
	oldNode := snapshot.pool.nodes[0]
	cfg := proxyCleanTestConfig()
	off := false
	cfg.ProxyClean.AutoCollect = &off
	next, err := newProxyCleanPool(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer next.closeIdle()
	next.now = old.now
	next.inherit(old)
	if next.active.Load() != snapshot || next.current().Len() != 2 || next.current().nodes[0] != oldNode || !next.active.Load().expires.Equal(expiry) {
		t.Fatal("turning collection off reset healthy pool or TTL")
	}
	var fetches atomic.Int32
	fetched := make(chan struct{}, 1)
	next.fetch = func(context.Context) ([]string, error) {
		fetches.Add(1)
		fetched <- struct{}{}
		return []string{"socks5://9.9.9.9:1080"}, nil
	}
	next.probe = func(context.Context, *proxyTransport, time.Duration) bool { return true }
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	next.start(root)
	select {
	case <-fetched:
		t.Fatal("collection off fetched at startup")
	case <-time.After(20 * time.Millisecond):
	}
	// Ordinary health checks neither extend the inherited source TTL nor fetch.
	next.check(context.Background())
	if fetches.Load() != 0 || !next.active.Load().expires.Equal(expiry) {
		t.Fatal("probe refreshed list TTL")
	}
	*now = expiry.Add(time.Second)
	if next.current().Len() != 0 {
		t.Fatal("collection off reused expired list")
	}
	if err = next.launch("refresh"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fetched:
	case <-time.After(time.Second):
		t.Fatal("manual refresh unavailable")
	}
	next.opWG.Wait()
	if fetches.Load() != 1 || next.current().Len() != 1 || !next.active.Load().expires.After(expiry) {
		t.Fatal("manual refresh did not renew source after auto collection disabled")
	}
	cancel()
}

func TestProxyCleanSwitchTogglesPreserveRestrictionAndNodeCooldown(t *testing.T) {
	old, _ := fixtureProxyClean(t)
	old.refresh(context.Background())
	n := old.candidates[0]
	lane := &anonymousNodes{dynamic: old, cooldown: time.Minute}
	lane.MarkFailure(n, nil, errors.New("fixture transport failure"))
	old.blockedUntil.Store(time.Now().Add(time.Hour).UnixNano())
	until, nodeUntil, failures := old.blockedUntil.Load(), n.cooldownUntil.Load(), n.failures.Load()
	for _, enabled := range []bool{false, true} {
		cfg := proxyCleanTestConfig()
		value := enabled
		cfg.ProxyClean.AutoSwitchProxy = &value
		cfg.ProxyClean.AutoCollect = &value
		next, err := newProxyCleanPool(cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer next.closeIdle()
		next.inherit(old)
		if next.candidates[0] != n || n.cooldownUntil.Load() != nodeUntil || n.failures.Load() != failures || next.blockedUntil.Load() != until || next.current() != nil {
			t.Fatal("operational toggle reset node or lane restriction")
		}
		old = next
	}
	// Policy changes invalidate the list rather than extending its freshness,
	// but must still preserve the business-level authentication/quota gate.
	cfg := proxyCleanTestConfig()
	cfg.ProxyClean.TTLSeconds++
	changed, err := newProxyCleanPool(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer changed.closeIdle()
	changed.inherit(old)
	if changed.active.Load() != nil || changed.blockedUntil.Load() != until || changed.current() != nil {
		t.Fatal("TTL policy change cleared restriction or retained incompatible snapshot")
	}
}

func TestProxyCleanLateOldWorkerCannotOverwriteDisabledState(t *testing.T) {
	old, _ := fixtureProxyClean(t)
	path := filepath.Join(t.TempDir(), "proxyclean-disabled.json")
	store, err := loadProxyCleanDisabled(path)
	if err != nil {
		t.Fatal(err)
	}
	old.disabledStore = store
	old.refresh(context.Background())
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	old.fetch = func(context.Context) ([]string, error) {
		close(entered)
		<-release
		return []string{"socks5://8.8.8.8:1080", "socks5://1.1.1.1:1080"}, nil
	}
	go func() { old.refresh(context.Background()); close(done) }()
	<-entered
	cfg := proxyCleanTestConfig()
	off := false
	cfg.ProxyClean.AutoCollect = &off
	next, err := newProxyCleanPool(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer next.closeIdle()
	next.inherit(old)
	id := config.Fingerprint(next.candidates[0].proxy.name)
	if err = next.setDisabled(id, true); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Even a late completion before the old runtime observes cancellation must
	// consult the shared disable store and cannot overwrite its persisted IDs.
	close(release)
	<-done
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(saved) {
		t.Fatal("old worker overwrote disabled state", err)
	}
	if !next.candidates[0].disabled() || !old.candidates[0].disabled() {
		t.Fatal("old/new runtime disable registries diverged")
	}
	cursor := old.current().CursorFor("")
	if node := cursor.Next(); node != nil && config.Fingerprint(node.proxy.name) == id {
		t.Fatal("late old worker revived disabled node")
	}
	if err = next.setDisabled(id, false); err != nil {
		t.Fatal(err)
	}
	if old.candidates[0].disabled() {
		t.Fatal("enable not synchronized across inherited runtimes")
	}
	reloaded, err := loadProxyCleanDisabled(path)
	if err != nil || reloaded.disabled(id) {
		t.Fatal("latest enable not persisted", err)
	}
}
