package config

import "testing"

func TestProxyCleanDefaultsAndScope(t *testing.T) {
	var c ProxyCleanConfig
	c.Defaults()
	if c.Enabled || c.AllowDirectFallback || c.Scope != "anonymous-only" || c.SourceURL != DefaultProxyCleanSource {
		t.Fatalf("unsafe defaults: %+v", c)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ProxyCleanConfig){func(c *ProxyCleanConfig) { c.Scope = "all" }, func(c *ProxyCleanConfig) { c.SourceURL = "http://127.0.0.1/list" }, func(c *ProxyCleanConfig) { c.SourceURL = DefaultProxyCleanSource + "?token=secret" }, func(c *ProxyCleanConfig) { c.SourceURL = "https://user:secret@raw.githubusercontent.com/list" }, func(c *ProxyCleanConfig) { c.ProbeConcurrency = 33 }, func(c *ProxyCleanConfig) { c.TTLSeconds = c.RefreshSeconds - 1 }, func(c *ProxyCleanConfig) { c.MaxCandidates = c.MaxNodes - 1 }} {
		bad := c
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatalf("accepted unsafe config: %+v", bad)
		}
	}
}
