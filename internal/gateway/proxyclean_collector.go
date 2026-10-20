package gateway

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"opencode2api/internal/config"
)

// ProxyCleanSourceStatus contains no addresses or upstream response bodies.
type ProxyCleanSourceStatus struct {
	ID             string     `json:"id"`
	Status         string     `json:"status"`
	CandidateCount int        `json:"candidate_count"`
	LastFetchAt    *time.Time `json:"last_fetch_at,omitempty"`
	LastError      string     `json:"last_error"`
}
type proxyCleanInputSource struct {
	id, url, defaultProtocol string
	multiProtocol            bool
}

// Shared across hot updates so a late response from the previous runtime still
// establishes cooldown before its replacement can fetch that same source.
type proxyCleanCollectorState struct {
	mu        sync.Mutex
	nextFetch map[string]time.Time
	inFlight  map[string]bool
	statuses  map[string]ProxyCleanSourceStatus
}
type proxyCleanCollector struct {
	sources []proxyCleanInputSource
	cfg     config.ProxyCleanConfig
	client  *http.Client
	shared  *proxyCleanCollectorState
	now     func() time.Time
}

func newProxyCleanCollector(cfg config.ProxyCleanConfig) *proxyCleanCollector {
	cfg.Defaults()
	c := &proxyCleanCollector{cfg: cfg, client: proxyCleanSourceClient(), now: time.Now, shared: &proxyCleanCollectorState{nextFetch: map[string]time.Time{}, inFlight: map[string]bool{}, statuses: map[string]ProxyCleanSourceStatus{}}}
	c.client.Timeout = 45 * time.Second
	for _, id := range cfg.Sources {
		c.sources = append(c.sources, proxyCleanInputSource{id: id, url: proxyCleanSourceURLs[id], defaultProtocol: "socks5"})
	}
	for _, s := range cfg.CustomSources {
		c.sources = append(c.sources, proxyCleanInputSource{id: "custom-" + config.Fingerprint(s.URL), url: s.URL, defaultProtocol: s.DefaultProtocol, multiProtocol: true})
	}
	return c
}
func (c *proxyCleanCollector) inherit(old *proxyCleanCollector) {
	if c != nil && old != nil {
		c.shared = old.shared
	}
}
func (c *proxyCleanCollector) view() []ProxyCleanSourceStatus {
	c.shared.mu.Lock()
	defer c.shared.mu.Unlock()
	out := make([]ProxyCleanSourceStatus, 0, len(c.sources))
	for _, s := range c.sources {
		v, ok := c.shared.statuses[s.id]
		if !ok {
			v = ProxyCleanSourceStatus{ID: s.id, Status: "pending"}
		}
		out = append(out, v)
	}
	return out
}
func (c *proxyCleanCollector) fetchOne(ctx context.Context, source proxyCleanInputSource) []string {
	id := source.id
	c.shared.mu.Lock()
	if c.now().Before(c.shared.nextFetch[id]) || c.shared.inFlight[id] {
		c.shared.mu.Unlock()
		return nil
	}
	c.shared.inFlight[id] = true
	c.shared.mu.Unlock()
	at := c.now().UTC()
	state := ProxyCleanSourceStatus{ID: id, Status: "error", LastFetchAt: &at}
	retryAfter := time.Duration(0)
	defer func() {
		c.shared.mu.Lock()
		defer c.shared.mu.Unlock()
		if retryAfter > 0 {
			until := c.now().Add(retryAfter)
			if until.After(c.shared.nextFetch[id]) {
				c.shared.nextFetch[id] = until
			}
		}
		c.shared.statuses[id] = state
		c.shared.inFlight[id] = false
	}()
	if source.url == "" {
		state.LastError = "source_invalid"
		return nil
	}
	cfg := c.cfg
	cfg.SourceURL = source.url
	maxBytes, timeout := proxyCleanSourceBudget(id)
	sourceCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	nodes, err := fetchProxyList(sourceCtx, cfg, c.client, maxBytes, source.defaultProtocol, source.multiProtocol)
	if err != nil {
		var httpErr *proxyCleanSourceHTTPError
		if errors.As(err, &httpErr) {
			retryAfter = httpErr.RetryAfter
		}
		state.LastError = "source_fetch_failed"
		switch err.Error() {
		case "source_empty":
			state.Status = "empty"
			state.LastError = "source_empty"
		case "source_invalid", "source_too_large", "source_http_status":
			state.LastError = err.Error()
		}
		return nil
	}
	state.Status = "ready"
	state.CandidateCount = len(nodes)
	return nodes
}

// Each source is independently bounded, with at most three parallel fetches.
// Failed or rate-limited sources never extend a previous snapshot's TTL.
func (c *proxyCleanCollector) fetch(ctx context.Context) ([]string, error) {
	defer c.client.CloseIdleConnections()
	lists := make([][]string, len(c.sources))
	jobs := make(chan int)
	var wg sync.WaitGroup
	workers := 3
	if len(lists) < workers {
		workers = len(lists)
	}
	for n := 0; n < workers; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				lists[i] = c.fetchOne(ctx, c.sources[i])
			}
		}()
	}
	for i := range lists {
		select {
		case jobs <- i:
		case <-ctx.Done():
		}
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]string, 0, c.cfg.MaxCandidates)
	seen := map[string]bool{}
	for offset := 0; len(out) < c.cfg.MaxCandidates; offset++ {
		any := false
		for _, list := range lists {
			if offset >= len(list) {
				continue
			}
			any = true
			node := list[offset]
			if !seen[node] {
				seen[node] = true
				out = append(out, node)
				if len(out) == c.cfg.MaxCandidates {
					break
				}
			}
		}
		if !any {
			break
		}
	}
	if len(out) == 0 {
		return nil, errors.New("source_fetch_failed")
	}
	return out, nil
}
