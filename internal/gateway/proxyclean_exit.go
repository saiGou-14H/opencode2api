package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"sync"
	"time"
)

const proxyCleanExitURL = "https://api.ipify.org?format=json"
const proxyCleanGeoURL = "https://ipwho.is/"

// Evidence describes only the observed echo exit. A destination-dependent proxy
// may use another exit for inference; this is not a cryptographic guarantee.
type proxyCleanExitEvidence struct {
	IP      string
	Country string
	Checked time.Time
	Expires time.Time
}

func (n *anonymousNode) exitEligible(now time.Time) bool {
	if !n.requireCN {
		return true
	}
	e := n.exitEvidence.Load()
	return e != nil && e.Country == "CN" && now.Before(e.Expires)
}

func boundedExitJSON(r io.Reader, value any) error {
	b, err := io.ReadAll(io.LimitReader(r, 4097))
	if err != nil || len(b) > 4096 {
		return errors.New("exit_invalid_response")
	}
	if json.Unmarshal(b, value) != nil {
		return errors.New("exit_invalid_response")
	}
	return nil
}

func observeProxyCleanExit(ctx context.Context, t *proxyTransport) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, proxyCleanExitURL, nil)
	resp, err := t.bun.Do(req)
	if err != nil {
		return "", errors.New("exit_echo_failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errors.New("exit_echo_failed")
	}
	var data struct {
		IP string `json:"ip"`
	}
	if err := boundedExitJSON(resp.Body, &data); err != nil {
		return "", err
	}
	ip, err := netip.ParseAddr(data.IP)
	if err != nil || !publicProxyIP(ip) {
		return "", errors.New("exit_invalid_ip")
	}
	return ip.String(), nil
}

type proxyCleanGeoEntry struct {
	country string
	expires time.Time
}
type proxyCleanGeo struct {
	closeIdle func()
	mu        sync.Mutex
	cache     map[string]proxyCleanGeoEntry
	gate      chan struct{}
	next      time.Time
	now       func() time.Time
	fetch     func(context.Context, string) (string, time.Duration, error)
}

func newProxyCleanGeo() *proxyCleanGeo {
	g := &proxyCleanGeo{cache: make(map[string]proxyCleanGeoEntry), gate: make(chan struct{}, 1), now: time.Now}
	client := proxyCleanSourceClient() // verified TLS, public pinned DNS, no redirects/environment proxy
	g.closeIdle = client.CloseIdleConnections
	g.fetch = func(ctx context.Context, ip string) (string, time.Duration, error) {
		return fetchProxyCleanGeo(ctx, client, ip)
	}
	return g
}

func fetchProxyCleanGeo(ctx context.Context, client *http.Client, ip string) (string, time.Duration, error) {
	addr, err := netip.ParseAddr(ip)
	if err != nil || !publicProxyIP(addr) {
		return "", 0, errors.New("exit_invalid_ip")
	}
	ip = addr.String()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, proxyCleanGeoURL+ip, nil)
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, errors.New("exit_geo_failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		delay := time.Duration(0)
		if resp.StatusCode == 429 || resp.StatusCode == 503 {
			delay = parseRetryAfter(resp.Header.Get("Retry-After"))
			if delay < time.Minute {
				delay = time.Minute
			}
		}
		return "", delay, errors.New("exit_geo_failed")
	}
	var data struct {
		Success bool   `json:"success"`
		IP      string `json:"ip"`
		Country string `json:"country_code"`
	}
	if boundedExitJSON(resp.Body, &data) != nil || !data.Success || data.IP != ip || len(data.Country) != 2 {
		return "", 0, errors.New("exit_geo_invalid")
	}
	return data.Country, 0, nil
}

// A single global query slot coalesces concurrent same-IP misses (cache is
// rechecked after acquiring it) and caps external service concurrency at one.
// Negative caching and a one-query/second rate cap also bound failures.
func (g *proxyCleanGeo) lookup(ctx context.Context, ip string) (string, error) {
	addr, err := netip.ParseAddr(ip)
	if err != nil || !publicProxyIP(addr) {
		return "", errors.New("exit_invalid_ip")
	}
	ip = addr.String()
	g.mu.Lock()
	e, ok := g.cache[ip]
	g.mu.Unlock()
	if ok && g.now().Before(e.expires) {
		return geoEntryResult(e)
	}
	select {
	case g.gate <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-g.gate }()
	g.mu.Lock()
	e, ok = g.cache[ip]
	g.mu.Unlock()
	if ok && g.now().Before(e.expires) {
		return geoEntryResult(e)
	}
	if delay := g.next.Sub(g.now()); delay > 0 {
		if delay > time.Second {
			return "", errors.New("exit_geo_backoff")
		}
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	country, retry, err := g.fetch(ctx, ip)
	g.next = g.now().Add(time.Second)
	if retry > time.Second {
		g.next = g.now().Add(retry)
	}
	ttl := 10 * time.Minute
	if err != nil {
		country = ""
		ttl = 30 * time.Second
	}
	e = proxyCleanGeoEntry{country: country, expires: g.now().Add(ttl)}
	g.mu.Lock()
	if len(g.cache) >= 1024 { // bounded eviction; no credentials or proxy endpoints are keys
		for key := range g.cache {
			delete(g.cache, key)
			break
		}
	}
	g.cache[ip] = e
	g.mu.Unlock()
	return geoEntryResult(e)
}

func geoEntryResult(e proxyCleanGeoEntry) (string, error) {
	if e.country == "" {
		return "", errors.New("exit_geo_failed")
	}
	return e.country, nil
}

func (p *proxyCleanPool) checkExit(ctx context.Context, n *anonymousNode) *proxyCleanExitEvidence {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.cfg.ProbeTimeoutSeconds)*time.Second)
	defer cancel()
	ip, err := p.observeExit(ctx, n.proxy)
	if err != nil {
		return nil
	}
	country, err := p.geo.lookup(ctx, ip)
	if err != nil || ctx.Err() != nil {
		return nil
	}
	now := p.now()
	// Never extend evidence merely because collection refreshed the list TTL.
	ttl := time.Duration(p.cfg.ProbeIntervalSeconds+p.cfg.ProbeTimeoutSeconds) * time.Second
	if ttl > 10*time.Minute {
		ttl = 10 * time.Minute
	}
	return &proxyCleanExitEvidence{IP: ip, Country: country, Checked: now, Expires: now.Add(ttl)}
}
