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

type proxyCleanCollector struct {
	cfg       config.ProxyCleanConfig
	client    *http.Client
	mu        sync.Mutex
	statuses  []ProxyCleanSourceStatus
	nextFetch map[string]time.Time
	now       func() time.Time
}

func newProxyCleanCollector(cfg config.ProxyCleanConfig) *proxyCleanCollector {
	cfg.Defaults()
	c := &proxyCleanCollector{cfg: cfg, client: proxyCleanSourceClient(), now: time.Now, nextFetch: make(map[string]time.Time)}
	c.client.Timeout = 45 * time.Second
	for _, id := range cfg.Sources {
		c.statuses = append(c.statuses, ProxyCleanSourceStatus{ID: id, Status: "pending"})
	}
	return c
}

func (c *proxyCleanCollector) view() []ProxyCleanSourceStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ProxyCleanSourceStatus(nil), c.statuses...)
}

// fetch has no unbounded retries and a fixed concurrency of three. Each source
// is independently bounded by the HTTP timeout and parser limit. Failed sources
// contribute no stale nodes to a new snapshot; all-source failure leaves the
// pool's previous snapshot usable only until its original TTL.
func (c *proxyCleanCollector) fetch(ctx context.Context) ([]string, error) {
	defer c.client.CloseIdleConnections()
	lists := make([][]string, len(c.cfg.Sources))
	states := make([]ProxyCleanSourceStatus, len(lists))
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
				id := c.cfg.Sources[i]
				c.mu.Lock()
				blocked := c.now().Before(c.nextFetch[id])
				previous := c.statuses[i]
				c.mu.Unlock()
				if blocked {
					states[i] = previous
					continue
				}
				at := c.now().UTC()
				state := ProxyCleanSourceStatus{ID: id, Status: "error", LastFetchAt: &at}
				url, known := proxyCleanSourceURLs[id]
				if !known {
					state.LastError = "source_invalid"
					states[i] = state
					continue
				}
				cfg := c.cfg
				cfg.SourceURL = url
				maxBytes, timeout := proxyCleanSourceBudget(id)
				sourceCtx, cancel := context.WithTimeout(ctx, timeout)
				nodes, err := fetchProxyCleanLimit(sourceCtx, cfg, c.client, maxBytes)
				cancel()
				if err != nil {
					var httpErr *proxyCleanSourceHTTPError
					if errors.As(err, &httpErr) && httpErr.RetryAfter > 0 {
						c.mu.Lock()
						c.nextFetch[id] = c.now().Add(httpErr.RetryAfter)
						c.mu.Unlock()
					}
					state.LastError = "source_fetch_failed"
					switch err.Error() {
					case "source_empty":
						state.Status = "empty"
						state.LastError = "source_empty"
					case "source_invalid", "source_too_large", "source_http_status":
						state.LastError = err.Error()
					}
				} else {
					state.Status = "ready"
					state.CandidateCount = len(nodes)
					lists[i] = nodes
				}
				states[i] = state
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
	c.mu.Lock()
	c.statuses = states
	c.mu.Unlock()
	// Round-robin merge prevents the first large repository from starving every
	// other source. Addresses are normalized and filtered before reaching here.
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
