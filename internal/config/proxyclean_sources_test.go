package config

import "testing"

func TestProxyCleanSourcesValidationAndClone(t *testing.T) {
	c := ProxyCleanConfig{}
	c.Defaults()
	if len(c.Sources) != 1 || c.Sources[0] != "proxyclean" {
		t.Fatal(c.Sources)
	}
	for _, sources := range [][]string{{"unknown"}, {"hookzof", "hookzof"}, {"https://localhost/"}} {
		bad := c
		bad.Sources = sources
		if bad.Validate() == nil {
			t.Fatalf("accepted %v", sources)
		}
	}
	c.Sources = append([]string(nil), ProxyCleanSourceIDs...)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	original := Config{ProxyClean: c}
	cloned := Clone(original)
	if !cloned.ProxyClean.Equal(original.ProxyClean) {
		t.Fatal("clone unequal")
	}
	cloned.ProxyClean.Sources[0] = "hookzof"
	if original.ProxyClean.Sources[0] != "proxyclean" {
		t.Fatal("clone leaked slice")
	}
	if cloned.ProxyClean.Equal(original.ProxyClean) {
		t.Fatal("different source selections compare equal")
	}
}
