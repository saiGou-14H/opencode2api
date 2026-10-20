package config

import (
	"encoding/json"
	"testing"
)

func TestProxyCleanCustomSources(t *testing.T) {
	for _, raw := range []string{`{"custom_sources":["https://example.com/list.txt"]}`, `{"custom_sources":[{"url":"https://example.com/list.txt","default_protocol":"https"}]}`} {
		var c ProxyCleanConfig
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			t.Fatal(err)
		}
		c.Defaults()
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		if len(c.Sources) != 0 {
			t.Fatal("custom-only unexpectedly enabled builtins", c.Sources)
		}
		clone := Clone(Config{ProxyClean: c})
		clone.ProxyClean.CustomSources[0].URL = "https://other.example/list"
		if c.CustomSources[0].URL != "https://example.com/list.txt" {
			t.Fatal("shared custom source slice")
		}
	}
	for _, u := range []string{"http://example.com/list", "https://localhost/list", "https://127.0.0.1/a", "https://10.0.0.1/a", "https://[::1]/a", "https://169.254.169.254/latest", "https://user:pass@example.com/list", "https://example.com/list?token=secret", "https://example.com/list#fragment", "https://example.com:8443/list", "file:///etc/passwd"} {
		c := ProxyCleanConfig{CustomSources: []ProxyCleanCustomSource{{URL: u}}}
		c.Defaults()
		if c.Validate() == nil {
			t.Fatal("unsafe source accepted", u)
		}
	}
	c := ProxyCleanConfig{CustomSources: []ProxyCleanCustomSource{{URL: "https://example.com/a", DefaultProtocol: "socks4"}}}
	c.Defaults()
	if c.Validate() == nil {
		t.Fatal("socks4 accepted")
	}
	c.CustomSources = []ProxyCleanCustomSource{{URL: "https://example.com/a"}, {URL: "https://example.com/a"}}
	c.Defaults()
	if c.Validate() == nil {
		t.Fatal("duplicate accepted")
	}
	c.CustomSources = nil
	for i := 0; i < 11; i++ {
		c.CustomSources = append(c.CustomSources, ProxyCleanCustomSource{URL: "https://example.com/a"})
	}
	c.Defaults()
	if c.Validate() == nil {
		t.Fatal("unbounded sources")
	}
}
