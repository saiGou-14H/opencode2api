package gateway

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"opencode2api/internal/config"
)

// ProxyCleanStatus contains operational counts and safe codes only. No proxy
// endpoints, credentials, upstream headers or fetch error text leave the worker.
type ProxyCleanStatus struct {
	Total          int                      `json:"total"`
	Healthy        int                      `json:"healthy"`
	Cooling        int                      `json:"cooling"`
	Disabled       int                      `json:"disabled"`
	LastProbeAt    *time.Time               `json:"last_probe_at,omitempty"`
	NextProbeAt    *time.Time               `json:"next_probe_at,omitempty"`
	NextRefreshAt  *time.Time               `json:"next_refresh_at,omitempty"`
	Operation      string                   `json:"operation"`
	Sources        []ProxyCleanSourceStatus `json:"sources"`
	Enabled        bool                     `json:"enabled"`
	Scope          string                   `json:"scope"`
	SourceStatus   string                   `json:"source_status"`
	CandidateCount int                      `json:"candidate_count"`
	HealthyCount   int                      `json:"healthy_count"`
	LastRefreshAt  *time.Time               `json:"last_refresh_at,omitempty"`
	ExpiresAt      *time.Time               `json:"expires_at,omitempty"`
	LastError      string                   `json:"last_error"`
	CooldownUntil  *time.Time               `json:"cooldown_until,omitempty"`
}

type proxyCleanSnapshot struct {
	pool    *anonymousNodes
	expires time.Time
}

type proxyCleanPool struct {
	geo                *proxyCleanGeo
	observeExit        func(context.Context, *proxyTransport) (string, error)
	lastCollectAttempt atomic.Int64
	lastProbeAt        atomic.Int64
	nextProbeAt        atomic.Int64
	nextRefreshAt      atomic.Int64
	disabledStore      *proxyCleanDisabledStore
	opMu               sync.Mutex
	opContext          context.Context
	operation          string
	opWG               sync.WaitGroup
	collector          *proxyCleanCollector
	cfg                config.ProxyCleanConfig
	performance        config.PerformanceConfig
	headerTimeout      time.Duration
	active             atomic.Pointer[proxyCleanSnapshot]
	blockedUntil       *atomic.Int64
	fallback           *anonymousNodes
	mu                 sync.Mutex
	status             ProxyCleanStatus
	candidates         []*anonymousNode
	lastGood           time.Time
	fetch              func(context.Context) ([]string, error)
	probe              func(context.Context, *proxyTransport, time.Duration) bool
	now                func() time.Time
}

func newProxyCleanPool(cfg config.Config) (*proxyCleanPool, error) {
	c := cfg.ProxyClean
	c.Defaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	p := &proxyCleanPool{geo: newProxyCleanGeo(), observeExit: observeProxyCleanExit, cfg: c, performance: cfg.Performance, headerTimeout: cfg.Performance.AttemptTimeout(time.Duration(cfg.Retry.TimeoutSeconds) * time.Second), now: time.Now, probe: probeProxyClean}
	p.disabledStore, _ = loadProxyCleanDisabled("")
	p.blockedUntil = &atomic.Int64{}
	p.status = ProxyCleanStatus{Enabled: c.Enabled, Scope: "anonymous-only", SourceStatus: "loading"}
	p.collector = newProxyCleanCollector(c)
	p.fetch = p.collector.fetch
	if c.AllowDirectFallback {
		direct, err := transportPool([]string{"direct"}, cfg.Performance, p.headerTimeout)
		if err != nil {
			return nil, err
		}
		for _, t := range direct.items {
			secureProxyCleanTransport(t)
		}
		p.fallback = anonymousPool(true, direct, time.Duration(c.CooldownSeconds)*time.Second)
		p.fallback.strict = true
		p.fallback.blockedUntil = p.blockedUntil
	}
	return p, nil
}

// current is also checked by each cursor: an expired last-good list is never
// rescued by anonymousCursor's legacy unhealthy/cooling fallback.
func (p *proxyCleanPool) current() *anonymousNodes {
	if p.blockedUntil.Load() > p.now().UnixNano() {
		return nil
	}
	s := p.active.Load()
	if s != nil && p.now().Before(s.expires) {
		rebuild := len(s.pool.nodes) == 0
		for _, n := range s.pool.nodes {
			if n.disabled() || !n.exitEligible(p.now()) || !n.proxy.healthy.Load() || n.cooldownUntil.Load() > p.now().UnixNano() {
				rebuild = true
				break
			}
		}
		if rebuild {
			// Promotion only scans already validated candidates: no network I/O and
			// no fresh probe is launched on a request's path.
			p.mu.Lock()
			p.publishLocked()
			s = p.active.Load()
			p.mu.Unlock()
		}
		if s != nil && p.now().Before(s.expires) {
			for _, n := range s.pool.nodes {
				if !n.disabled() && n.exitEligible(p.now()) && n.proxy.healthy.Load() && n.cooldownUntil.Load() <= p.now().UnixNano() {
					return s.pool
				}
			}
		}
	}
	return p.fallback
}

func (p *proxyCleanPool) view() ProxyCleanStatus {
	p.opMu.Lock()
	operation := p.operation
	p.opMu.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	v := p.status
	v.Operation = operation
	v.LastProbeAt = proxyCleanTime(p.lastProbeAt.Load())
	v.LastRefreshAt = proxyCleanTime(p.lastCollectAttempt.Load())
	v.NextProbeAt = proxyCleanTime(p.nextProbeAt.Load())
	if p.cfg.AutoCollectEnabled() {
		v.NextRefreshAt = proxyCleanTime(p.nextRefreshAt.Load())
	}
	nowUnix := p.now().UnixNano()
	fresh := !p.lastGood.IsZero() && p.now().Before(p.lastGood.Add(time.Duration(p.cfg.TTLSeconds)*time.Second))
	v.Total = len(p.candidates)
	for _, n := range p.candidates {
		if n.disabled() {
			v.Disabled++
			continue
		}
		if n.cooldownUntil.Load() > nowUnix {
			v.Cooling++
			continue
		}
		if fresh && n.exitEligible(p.now()) && n.proxy.healthy.Load() {
			v.Healthy++
		}
	}
	if p.cfg.AutoCollectEnabled() && p.cfg.MinHealthyNodes > 0 && p.blockedUntil.Load() <= nowUnix && p.transportHealthyLocked(p.now()) < p.cfg.MinHealthyNodes {
		due := time.Unix(0, p.lastCollectAttempt.Load()).Add(time.Duration(p.cfg.RefillIntervalSeconds) * time.Second)
		if due.Before(p.now()) {
			due = p.now()
		}
		if v.NextRefreshAt == nil || due.Before(*v.NextRefreshAt) {
			v.NextRefreshAt = &due
		}
	}
	if p.collector != nil {
		v.Sources = p.collector.view()
		for _, source := range v.Sources {
			if (source.Status == "error" || source.Status == "empty") && v.LastError == "" {
				v.LastError = "source_partial_failure"
			}
		}
	}
	v.HealthyCount = 0
	now := p.now()
	s := p.active.Load()
	if s != nil && now.Before(s.expires) {
		for _, n := range s.pool.nodes {
			if !n.disabled() && n.exitEligible(p.now()) && n.proxy.healthy.Load() && n.cooldownUntil.Load() <= now.UnixNano() {
				v.HealthyCount++
			}
		}
	}
	if !p.lastGood.IsZero() && !now.Before(p.lastGood.Add(time.Duration(p.cfg.TTLSeconds)*time.Second)) {
		v.SourceStatus = "expired"
	} else if v.HealthyCount > 0 {
		if v.LastError != "" {
			v.SourceStatus = "degraded"
		} else {
			v.SourceStatus = "ready"
		}
	} else if v.SourceStatus != "loading" {
		v.SourceStatus = "empty"
	}
	if p.cfg.RequiredExitCountry == "CN" && v.SourceStatus != "loading" && v.HealthyCount == 0 && p.transportHealthyLocked(now) == 0 {
		// Safe diagnostic: neither source country labels nor unknown/expired
		// observations establish a mainland exit. This is not a network verdict.
		v.LastError = "exit_country_unavailable"
	}
	if until := p.blockedUntil.Load(); until > now.UnixNano() {
		cooldownUntil := time.Unix(0, until).UTC()
		v.CooldownUntil = &cooldownUntil
		v.HealthyCount = 0
		v.SourceStatus = "degraded"
		v.LastError = "upstream_restricted"
	}
	return v
}

// inherit keeps the last-good TTL and node cooldowns on unrelated config
// applies. It shares only immutable snapshots and atomically updated nodes.
func (p *proxyCleanPool) inherit(old *proxyCleanPool) {
	if old == nil {
		return
	}
	p.collector.inherit(old.collector)
	p.blockedUntil = old.blockedUntil
	p.disabledStore = old.disabledStore
	p.geo = old.geo
	if p.fallback != nil {
		p.fallback.blockedUntil = p.blockedUntil
	}
	// Operational switches do not invalidate a fresh source snapshot.
	currentSettings, oldSettings := p.cfg, old.cfg
	currentSettings.MinHealthyNodes = 0
	oldSettings.MinHealthyNodes = 0
	currentSettings.RefillIntervalSeconds = 0
	oldSettings.RefillIntervalSeconds = 0
	currentSettings.AutoCollect = nil
	oldSettings.AutoCollect = nil
	currentSettings.AutoSwitchProxy = nil
	oldSettings.AutoSwitchProxy = nil
	if !currentSettings.Equal(oldSettings) || p.performance != old.performance || p.headerTimeout != old.headerTimeout {
		return
	}
	old.mu.Lock()
	defer old.mu.Unlock()
	p.disabledStore = old.disabledStore
	p.candidates = append([]*anonymousNode(nil), old.candidates...)
	p.lastGood = old.lastGood
	p.lastCollectAttempt.Store(old.lastCollectAttempt.Load())
	p.lastProbeAt.Store(old.lastProbeAt.Load())
	p.status = old.status
	p.active.Store(old.active.Load())
}

func (p *proxyCleanPool) run(ctx context.Context) {
	defer func() {
		p.opMu.Lock()
		p.opContext = nil
		p.opMu.Unlock()
		p.opWG.Wait()
		p.nextProbeAt.Store(0)
		p.nextRefreshAt.Store(0)
		p.closeIdle()
	}()
	if p.cfg.AutoCollectEnabled() {
		_ = p.launch("refresh")
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.schedule()
		}
	}
}

func (p *proxyCleanPool) refresh(ctx context.Context) {
	p.lastCollectAttempt.Store(p.now().UnixNano())
	addresses, err := p.fetch(ctx)
	if ctx.Err() != nil {
		return
	}
	p.mu.Lock()
	now := p.now().UTC()
	p.status.LastRefreshAt = &now
	if err != nil {
		// Only errors produced by our provider may be exposed; injection/network
		// failures are collapsed to a constant rather than stringified.
		p.status.LastError = "source_fetch_failed"
		for _, safe := range []string{"source_empty", "source_invalid", "source_too_large", "source_http_status"} {
			if err.Error() == safe {
				p.status.LastError = safe
			}
		}
		p.status.SourceStatus = "degraded"
		p.mu.Unlock()
		return
	}
	if len(addresses) > p.cfg.MaxCandidates {
		addresses = addresses[:p.cfg.MaxCandidates]
	}
	old := make(map[string]*anonymousNode, len(p.candidates))
	for _, n := range p.candidates {
		old[n.proxy.name] = n
	}
	next := make([]*anonymousNode, 0, len(addresses))
	for _, address := range addresses {
		if n := old[address]; n != nil {
			next = append(next, n)
			delete(old, address)
			continue
		}
		transports, e := transportPool([]string{address}, p.performance, p.headerTimeout)
		if e != nil {
			continue
		}
		t := transports.items[0]
		secureProxyCleanTransport(t)
		t.healthy.Store(false)
		next = append(next, &anonymousNode{proxy: t, disableState: p.disabledStore, requireCN: p.cfg.RequiredExitCountry == "CN"})
	}
	for _, n := range old {
		n.proxy.client.CloseIdleConnections()
		n.proxy.bun.CloseIdleConnections()
	}
	p.candidates = next
	p.lastGood = now
	expires := now.Add(time.Duration(p.cfg.TTLSeconds) * time.Second)
	p.status.ExpiresAt = &expires
	p.status.CandidateCount = len(next)
	p.status.LastError = ""
	p.status.SourceStatus = "empty"
	p.publishLocked()
	p.mu.Unlock()
	p.check(ctx)
}

func (p *proxyCleanPool) publishLocked() {
	nodes := make([]*anonymousNode, 0, p.cfg.MaxNodes)
	now := p.now()
	for _, n := range p.candidates {
		if len(nodes) >= p.cfg.MaxNodes {
			break
		}
		if !n.disabled() && n.exitEligible(p.now()) && n.proxy.healthy.Load() && n.cooldownUntil.Load() <= now.UnixNano() {
			nodes = append(nodes, n)
		}
	}
	expires := p.lastGood.Add(time.Duration(p.cfg.TTLSeconds) * time.Second)
	pool := &anonymousNodes{nodes: nodes, cooldown: time.Duration(p.cfg.CooldownSeconds) * time.Second, strict: true, expires: expires, blockedUntil: p.blockedUntil}
	p.active.Store(&proxyCleanSnapshot{pool: pool, expires: expires})
}

func (p *proxyCleanPool) check(ctx context.Context) {
	p.mu.Lock()
	var nodes []*anonymousNode
	if !p.lastGood.IsZero() && p.now().Before(p.lastGood.Add(time.Duration(p.cfg.TTLSeconds)*time.Second)) {
		nodes = append(nodes, p.candidates...)
	}
	// Explicit direct fallback is not sourced from the expiring public list.
	// It must recover even when no fetch has ever succeeded or the list expired.
	if p.fallback != nil {
		nodes = append(nodes, p.fallback.nodes...)
	}
	p.mu.Unlock()
	jobs := make(chan *anonymousNode)
	var wg sync.WaitGroup
	for i := 0; i < p.cfg.ProbeConcurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range jobs {
				if ctx.Err() != nil {
					continue
				}
				n.proxy.publicStateMu.Lock()
				if n.disabled() || n.cooldownUntil.Load() > p.now().UnixNano() {
					n.proxy.publicStateMu.Unlock()
					continue
				}
				if n.requireCN {
					n.proxy.publicGeneration++
				}
				generation := n.proxy.publicGeneration
				if n.requireCN {
					n.exitEvidence.Store(nil)
					n.proxy.healthy.Store(false)
				}
				n.proxy.publicStateMu.Unlock()
				probeStarted := time.Now()
				healthy := p.probe(ctx, n.proxy, time.Duration(p.cfg.ProbeTimeoutSeconds)*time.Second)
				var evidence *proxyCleanExitEvidence
				if n.requireCN && healthy {
					evidence = p.checkExit(ctx, n)
					healthy = evidence != nil && evidence.Country == "CN"
				}
				latency := time.Since(probeStarted).Milliseconds()
				n.proxy.publicStateMu.Lock()
				// The compare and state commit share the same lock as business updates.
				// A stale success must not erase a later request's failure/cooldown;
				// conversely, a stale failed probe must not erase business recovery.
				if ctx.Err() != nil || n.disabled() || n.proxy.publicGeneration != generation {
					n.proxy.publicStateMu.Unlock()
					continue
				}
				n.proxy.publicGeneration++
				if n.requireCN {
					n.exitEvidence.Store(evidence)
				}
				n.lastProbeAt.Store(p.now().UnixNano())
				n.probeLatencyMS.Store(latency)
				n.lastProbeOK.Store(healthy)
				n.proxy.healthy.Store(healthy)
				if healthy {
					n.failures.Store(0)
					n.cooldownUntil.Store(0)
				} else {
					n.failures.Add(1)
					n.cooldownUntil.Store(p.now().Add(time.Duration(p.cfg.CooldownSeconds) * time.Second).UnixNano())
				}
				n.proxy.publicStateMu.Unlock()
			}
		}()
	}
	for _, n := range nodes {
		if n.disabled() || n.cooldownUntil.Load() > p.now().UnixNano() {
			continue
		}
		select {
		case jobs <- n:
		case <-ctx.Done():
		}
	}
	close(jobs)
	wg.Wait()
	if ctx.Err() != nil {
		return
	}
	p.lastProbeAt.Store(p.now().UnixNano())
	p.nextProbeAt.Store(p.now().Add(time.Duration(p.cfg.ProbeIntervalSeconds) * time.Second).UnixNano())
	p.mu.Lock()
	p.publishLocked()
	p.mu.Unlock()
}

func (p *proxyCleanPool) closeIdle() {
	// The geo client survives hot apply. Closing idle connections is safe for
	// concurrent requests and does not close the client or reset service backoff.
	if p.geo != nil && p.geo.closeIdle != nil {
		p.geo.closeIdle()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, n := range p.candidates {
		n.proxy.client.CloseIdleConnections()
		n.proxy.bun.CloseIdleConnections()
	}
	if p.fallback != nil {
		for _, n := range p.fallback.nodes {
			n.proxy.client.CloseIdleConnections()
			n.proxy.bun.CloseIdleConnections()
		}
	}
}

func (m *RuntimeController) ProxyCleanStatus() ProxyCleanStatus {
	runtime := m.current.Load()
	if runtime == nil || runtime.gateway.publicPool == nil {
		return ProxyCleanStatus{Scope: "anonymous-only", SourceStatus: "disabled"}
	}
	return runtime.gateway.publicPool.view()
}
