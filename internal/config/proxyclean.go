package config

import (
	"fmt"
	"reflect"
)

// ProxyCleanSourceIDs are explicit, audited repository adapters, not arbitrary URLs.
var ProxyCleanSourceIDs = []string{"proxyclean", "proxifly", "thespeedx", "roosterkid", "hookzof", "gfpcom", "dpangestuw", "proxifly-cn", "xyzs-cn", "monosans-cn"}

func (c ProxyCleanConfig) Equal(other ProxyCleanConfig) bool { return reflect.DeepEqual(c, other) }

// DefaultProxyCleanSource is public data, not a private API endpoint.
const DefaultProxyCleanSource = "https://raw.githubusercontent.com/HankNovic/ProxyClean/refs/heads/main/SOCKS5.txt"

// ProxyCleanConfig is deliberately restricted to the anonymous free lane.
// Explicit public adapters and validated custom HTTPS text lists are supported.
type ProxyCleanConfig struct {
	RequiredExitCountry   string                   `json:"required_exit_country"`
	MinHealthyNodes       int                      `json:"min_healthy_nodes"`
	RefillIntervalSeconds int                      `json:"refill_interval_seconds"`
	CustomSources         []ProxyCleanCustomSource `json:"custom_sources,omitempty"`
	AutoSwitchProxy       *bool                    `json:"auto_switch_proxy,omitempty"`
	AutoCollect           *bool                    `json:"auto_collect,omitempty"`
	Sources               []string                 `json:"sources"`
	Enabled               bool                     `json:"enabled"`
	Scope                 string                   `json:"scope"`
	SourceURL             string                   `json:"source_url"`
	RefreshSeconds        int                      `json:"refresh_seconds"`
	TTLSeconds            int                      `json:"ttl_seconds"`
	ProbeIntervalSeconds  int                      `json:"probe_interval_seconds"`
	ProbeTimeoutSeconds   int                      `json:"probe_timeout_seconds"`
	ProbeConcurrency      int                      `json:"probe_concurrency"`
	MaxCandidates         int                      `json:"max_candidates"`
	MaxNodes              int                      `json:"max_nodes"`
	CooldownSeconds       int                      `json:"cooldown_seconds"`
	AllowDirectFallback   bool                     `json:"allow_direct_fallback"`
}

func (c ProxyCleanConfig) AutoSwitchEnabled() bool {
	return c.AutoSwitchProxy == nil || *c.AutoSwitchProxy
}
func (c ProxyCleanConfig) AutoCollectEnabled() bool { return c.AutoCollect == nil || *c.AutoCollect }

func (c *ProxyCleanConfig) Defaults() {
	for i := range c.CustomSources {
		c.CustomSources[i].defaults()
	}
	if len(c.Sources) == 0 && len(c.CustomSources) == 0 {
		c.Sources = []string{"proxyclean"}
	}
	if c.Scope == "" {
		c.Scope = "anonymous-only"
	}
	if c.SourceURL == "" {
		c.SourceURL = DefaultProxyCleanSource
	}
	for _, v := range []struct {
		p *int
		d int
	}{{&c.RefillIntervalSeconds, 300}, {&c.RefreshSeconds, 3600}, {&c.TTLSeconds, 7200}, {&c.ProbeIntervalSeconds, 300}, {&c.ProbeTimeoutSeconds, 8}, {&c.ProbeConcurrency, 8}, {&c.MaxCandidates, 100}, {&c.MaxNodes, 20}, {&c.CooldownSeconds, 60}} {
		if *v.p == 0 {
			*v.p = v.d
		}
	}
}

func (c ProxyCleanConfig) Validate() error {
	if c.RequiredExitCountry != "" && c.RequiredExitCountry != "CN" {
		return fmt.Errorf("proxyclean.required_exit_country must be empty or CN")
	}
	if c.RequiredExitCountry != "" && c.AllowDirectFallback {
		return fmt.Errorf("proxyclean.allow_direct_fallback is incompatible with required_exit_country")
	}
	if err := validateProxyCleanCustomSources(c.CustomSources); err != nil {
		return err
	}
	if (len(c.Sources) == 0 && len(c.CustomSources) == 0) || len(c.Sources) > len(ProxyCleanSourceIDs) {
		return fmt.Errorf("proxyclean.sources must select at least one built-in or custom source")
	}
	seen := map[string]bool{}
	for _, id := range c.Sources {
		known := false
		for _, supported := range ProxyCleanSourceIDs {
			if id == supported {
				known = true
				break
			}
		}
		if !known || seen[id] {
			return fmt.Errorf("proxyclean.sources contains an unknown or duplicate source")
		}
		seen[id] = true
	}
	if c.Scope != "anonymous-only" {
		return fmt.Errorf("proxyclean.scope must be anonymous-only")
	}
	// An exact allowlist also rejects userinfo, queries, fragments and alternate ports.
	if c.SourceURL != DefaultProxyCleanSource {
		return fmt.Errorf("proxyclean.source_url must be the official HTTPS raw list URL")
	}
	for _, v := range []struct {
		name      string
		n, lo, hi int
	}{{"refill_interval_seconds", c.RefillIntervalSeconds, 30, 86400}, {"min_healthy_nodes", c.MinHealthyNodes, 0, c.MaxNodes}, {"refresh_seconds", c.RefreshSeconds, 30, 86400}, {"ttl_seconds", c.TTLSeconds, 30, 172800}, {"probe_interval_seconds", c.ProbeIntervalSeconds, 10, 3600}, {"probe_timeout_seconds", c.ProbeTimeoutSeconds, 1, 30}, {"probe_concurrency", c.ProbeConcurrency, 1, 32}, {"max_candidates", c.MaxCandidates, 1, 1000}, {"max_nodes", c.MaxNodes, 1, 100}, {"cooldown_seconds", c.CooldownSeconds, 1, 3600}} {
		if v.n < v.lo || v.n > v.hi {
			return fmt.Errorf("proxyclean.%s must be between %d and %d", v.name, v.lo, v.hi)
		}
	}
	if c.TTLSeconds < c.RefreshSeconds {
		return fmt.Errorf("proxyclean.ttl_seconds must not be less than refresh_seconds")
	}
	if c.MaxNodes > c.MaxCandidates {
		return fmt.Errorf("proxyclean.max_nodes must not exceed max_candidates")
	}
	return nil
}
