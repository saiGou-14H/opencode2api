package config

import "testing"

func TestProxyCleanRefillDefaultsAndBounds(t *testing.T) {
	var c ProxyCleanConfig
	c.Defaults()
	if c.MinHealthyNodes != 0 || c.RefillIntervalSeconds != 300 {
		t.Fatal("unsafe refill defaults")
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ProxyCleanConfig){func(c *ProxyCleanConfig) { c.MinHealthyNodes = -1 }, func(c *ProxyCleanConfig) { c.MinHealthyNodes = c.MaxNodes + 1 }, func(c *ProxyCleanConfig) { c.RefillIntervalSeconds = 29 }, func(c *ProxyCleanConfig) { c.RefillIntervalSeconds = 86401 }} {
		bad := c
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatal("invalid refill bounds accepted")
		}
	}
	c.MinHealthyNodes = 5
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}
