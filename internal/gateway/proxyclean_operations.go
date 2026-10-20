package gateway

import (
	"context"
	"errors"
	"net/url"
	"time"

	"opencode2api/internal/config"
)

var (
	ErrProxyCleanDisabled     = errors.New("proxyclean_disabled")
	ErrProxyCleanBusy         = errors.New("proxyclean_busy")
	ErrProxyCleanUnavailable  = errors.New("proxyclean_unavailable")
	ErrProxyCleanNodeNotFound = errors.New("proxyclean_node_not_found")
	ErrProxyCleanState        = errors.New("proxyclean_state_failed")
)

type ProxyCleanNodeView struct {
	ExitIP              string     `json:"exit_ip,omitempty"`
	ExitCountry         string     `json:"exit_country,omitempty"`
	ExitCheckedAt       *time.Time `json:"exit_checked_at,omitempty"`
	ExitExpiresAt       *time.Time `json:"exit_expires_at,omitempty"`
	Protocol            string     `json:"protocol"`
	ProbeLatencyMS      *int64     `json:"probe_latency_ms,omitempty"`
	ConsecutiveFailures uint32     `json:"consecutive_failures"`
	ID                  string     `json:"id"`
	Endpoint            string     `json:"endpoint"`
	Healthy             bool       `json:"healthy"`
	Disabled            bool       `json:"disabled"`
	CooldownUntil       *time.Time `json:"cooldown_until,omitempty"`
	LastProbeAt         *time.Time `json:"last_probe_at,omitempty"`
	LastProbeOK         *bool      `json:"last_probe_ok,omitempty"`
}
type ProxyCleanNodesView struct {
	Nodes     []ProxyCleanNodeView `json:"nodes"`
	Total     int                  `json:"total"`
	Operation string               `json:"operation"`
}

func (p *proxyCleanPool) start(ctx context.Context) {
	p.opMu.Lock()
	p.opContext = ctx
	p.nextProbeAt.Store(p.now().Add(time.Duration(p.cfg.ProbeIntervalSeconds) * time.Second).UnixNano())
	if p.cfg.AutoCollectEnabled() {
		p.nextRefreshAt.Store(p.now().Add(time.Duration(p.cfg.RefreshSeconds) * time.Second).UnixNano())
	} else {
		p.nextRefreshAt.Store(0)
	}
	p.opMu.Unlock()
	go p.run(ctx)
}

// Automatic and manual network work share one slot. Only runtime-owned context
// reaches the provider: returning 202 or closing the browser cannot cancel it.
func (p *proxyCleanPool) launch(operation string) error {
	p.opMu.Lock()
	defer p.opMu.Unlock()
	return p.launchLocked(operation)
}

func (p *proxyCleanPool) launchLocked(operation string) error {
	if !p.cfg.Enabled {
		return ErrProxyCleanDisabled
	}
	if p.opContext == nil || p.opContext.Err() != nil {
		return ErrProxyCleanUnavailable
	}
	if p.operation != "" {
		return ErrProxyCleanBusy
	}
	p.operation = operation
	if operation == "refresh" {
		now := p.now()
		p.lastCollectAttempt.Store(now.UnixNano())
		if p.cfg.AutoCollectEnabled() {
			p.nextRefreshAt.Store(now.Add(time.Duration(p.cfg.RefreshSeconds) * time.Second).UnixNano())
		}
	} else {
		p.nextProbeAt.Store(p.now().Add(time.Duration(p.cfg.ProbeIntervalSeconds) * time.Second).UnixNano())
	}
	ctx := p.opContext
	p.opWG.Add(1)
	go func() {
		defer p.opWG.Done()
		defer func() { p.opMu.Lock(); p.operation = ""; p.opMu.Unlock() }()
		// The collector has per-source limits; this also bounds the whole operation.
		ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		if operation == "refresh" {
			p.refresh(ctx)
		} else {
			p.check(ctx)
		}
	}()
	return nil
}

func (p *proxyCleanPool) nodesView() ProxyCleanNodesView {
	v := ProxyCleanNodesView{Nodes: []ProxyCleanNodeView{}}
	p.opMu.Lock()
	v.Operation = p.operation
	p.opMu.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, n := range p.candidates {
		if len(v.Nodes) >= 1000 {
			break
		}
		item := ProxyCleanNodeView{ID: config.Fingerprint(n.proxy.name), Endpoint: n.proxy.name, Healthy: n.proxy.healthy.Load() && n.exitEligible(p.now()), Disabled: n.disabled(), ConsecutiveFailures: n.failures.Load()}
		if evidence := n.exitEvidence.Load(); evidence != nil {
			item.ExitIP, item.ExitCountry = evidence.IP, evidence.Country
			checked, expires := evidence.Checked.UTC(), evidence.Expires.UTC()
			item.ExitCheckedAt, item.ExitExpiresAt = &checked, &expires
		}
		if endpoint, err := url.Parse(n.proxy.name); err == nil {
			item.Protocol = endpoint.Scheme
		}
		if until := n.cooldownUntil.Load(); until > p.now().UnixNano() {
			t := time.Unix(0, until).UTC()
			item.CooldownUntil = &t
		}
		if at := n.lastProbeAt.Load(); at > 0 {
			t := time.Unix(0, at).UTC()
			item.LastProbeAt = &t
			latency := n.probeLatencyMS.Load()
			item.ProbeLatencyMS = &latency
			ok := n.lastProbeOK.Load()
			item.LastProbeOK = &ok
		}
		v.Nodes = append(v.Nodes, item)
	}
	v.Total = len(v.Nodes)
	return v
}

func (p *proxyCleanPool) setDisabled(id string, disabled bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, n := range p.candidates {
		if config.Fingerprint(n.proxy.name) != id {
			continue
		}
		if err := p.disabledStore.set(id, disabled); err != nil {
			return ErrProxyCleanState
		}
		n.proxy.publicStateMu.Lock()
		n.proxy.publicGeneration++
		n.proxy.publicStateMu.Unlock()
		p.publishLocked()
		return nil
	}
	return ErrProxyCleanNodeNotFound
}

func (m *RuntimeController) ProxyCleanNodes() ProxyCleanNodesView {
	runtime := m.current.Load()
	if runtime == nil || runtime.gateway.publicPool == nil {
		return ProxyCleanNodesView{Nodes: []ProxyCleanNodeView{}}
	}
	return runtime.gateway.publicPool.nodesView()
}
func (m *RuntimeController) ProxyCleanOperation(operation string) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	runtime := m.current.Load()
	if runtime == nil || runtime.gateway.publicPool == nil {
		return ErrProxyCleanDisabled
	}
	if operation != "refresh" && operation != "probe" {
		return ErrProxyCleanUnavailable
	}
	return runtime.gateway.publicPool.launch(operation)
}
func (m *RuntimeController) ProxyCleanSetDisabled(id string, disabled bool) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	runtime := m.current.Load()
	if runtime == nil || runtime.gateway.publicPool == nil {
		return ErrProxyCleanDisabled
	}
	return runtime.gateway.publicPool.setDisabled(id, disabled)
}
