package config

import (
	"encoding/json"
	"testing"
)

func TestProxyCleanRequiredExitCountry(t *testing.T) {
	for _, country := range []string{"", "CN", "HK", "MO", "TW", "US", "cn", " CN"} {
		for _, direct := range []bool{false, true} {
			c := ProxyCleanConfig{RequiredExitCountry: country, AllowDirectFallback: direct}
			c.Defaults()
			valid := (country == "" || country == "CN") && !(country == "CN" && direct)
			if (c.Validate() == nil) != valid {
				t.Errorf("country=%q direct=%v validation mismatch", country, direct)
			}
		}
	}
	var c ProxyCleanConfig
	if err := json.Unmarshal([]byte(`{"enabled":true}`), &c); err != nil {
		t.Fatal(err)
	}
	c.Defaults()
	if c.RequiredExitCountry != "" || c.Validate() != nil {
		t.Fatal("legacy config changed")
	}
	if err := json.Unmarshal([]byte(`{"required_exit_country":"CN"}`), &c); err != nil || c.RequiredExitCountry != "CN" {
		t.Fatal("JSON contract")
	}
}

func TestProxyCleanCNSourceIDsAndRegistryLimit(t *testing.T) {
	for _, id := range []string{"proxifly-cn", "xyzs-cn", "monosans-cn"} {
		c := ProxyCleanConfig{Sources: []string{id}, RequiredExitCountry: "CN"}
		c.Defaults()
		if err := c.Validate(); err != nil {
			t.Fatalf("new source %s: %v", id, err)
		}
	}
	c := ProxyCleanConfig{Sources: append([]string(nil), ProxyCleanSourceIDs...), RequiredExitCountry: "CN"}
	c.Defaults()
	if err := c.Validate(); err != nil {
		t.Fatalf("whole registry exceeds config limit: %v", err)
	}
	c.Sources = append(c.Sources, "proxifly-cn")
	if c.Validate() == nil {
		t.Fatal("over-limit registry accepted")
	}
}
