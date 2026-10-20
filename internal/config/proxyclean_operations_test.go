package config

import (
	"encoding/json"
	"testing"
)

func TestProxyCleanOperationalSwitchDefaultsAndClone(t *testing.T) {
	var c ProxyCleanConfig
	if !c.AutoCollectEnabled() || !c.AutoSwitchEnabled() {
		t.Fatal("legacy defaults changed")
	}
	if err := json.Unmarshal([]byte(`{"auto_collect":false,"auto_switch_proxy":false}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.AutoCollectEnabled() || c.AutoSwitchEnabled() {
		t.Fatal("explicit false ignored")
	}
	cloned := Clone(Config{ProxyClean: c})
	*cloned.ProxyClean.AutoCollect = true
	*cloned.ProxyClean.AutoSwitchProxy = true
	if c.AutoCollectEnabled() || c.AutoSwitchEnabled() {
		t.Fatal("clone shared switch pointers")
	}
}
