package gateway

import "time"

func proxyCleanTime(nanos int64) *time.Time {
	if nanos <= 0 {
		return nil
	}
	t := time.Unix(0, nanos).UTC()
	return &t
}

// transportHealthyLocked deliberately ignores business cooldown and the lane
// gate. A 401/403/429 is not a transport outage and must not drive exit refill.
// The caller holds p.mu. Expired list entries cannot satisfy the low watermark.
func (p *proxyCleanPool) transportHealthyLocked(now time.Time) int {
	if p.lastGood.IsZero() || !now.Before(p.lastGood.Add(time.Duration(p.cfg.TTLSeconds)*time.Second)) {
		return 0
	}
	count := 0
	for _, n := range p.candidates {
		if !n.disabled() && n.proxy.healthy.Load() {
			count++
		}
	}
	return count
}

func (p *proxyCleanPool) maybeRefill() bool {
	// Admission and timestamp update share the same operation lock, so two
	// schedulers cannot both pass the minimum-interval check around a fast fetch.
	p.opMu.Lock()
	defer p.opMu.Unlock()
	now := p.now()
	if !p.cfg.Enabled || !p.cfg.AutoCollectEnabled() || p.cfg.MinHealthyNodes == 0 || p.operation != "" || p.blockedUntil.Load() > now.UnixNano() {
		return false
	}
	last := p.lastCollectAttempt.Load()
	if last > 0 && now.Before(time.Unix(0, last).Add(time.Duration(p.cfg.RefillIntervalSeconds)*time.Second)) {
		return false
	}
	p.mu.Lock()
	healthy := p.transportHealthyLocked(now)
	p.mu.Unlock()
	if healthy >= p.cfg.MinHealthyNodes {
		return false
	}
	return p.launchLocked("refresh") == nil
}

// A lightweight one-second coordinator starts only bounded, single-flight
// operations. It never itself performs network work or adds queued operations.
func (p *proxyCleanPool) schedule() {
	now := p.now().UnixNano()
	if p.cfg.AutoCollectEnabled() {
		if due := p.nextRefreshAt.Load(); due > 0 && due <= now {
			if p.launch("refresh") == nil {
				return
			}
		}
	}
	if due := p.nextProbeAt.Load(); due > 0 && due <= now {
		if p.launch("probe") == nil {
			return
		}
	}
	p.maybeRefill()
}
