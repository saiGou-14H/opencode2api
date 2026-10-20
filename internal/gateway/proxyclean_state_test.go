package gateway

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProxyCleanStatePathIsFixedAbsoluteSibling(t *testing.T) {
	path, err := proxyCleanStatePath("/var/lib/opencode2api/config.json")
	if err != nil || path != "/var/lib/opencode2api/proxyclean-disabled.json" {
		t.Fatal(path, err)
	}
	path, err = proxyCleanStatePath("config.json")
	if err != nil || !filepath.IsAbs(path) || filepath.Base(path) != "proxyclean-disabled.json" {
		t.Fatal(path, err)
	}
	if _, err = loadProxyCleanDisabled("../proxyclean-disabled.json"); err == nil {
		t.Fatal("relative traversal path accepted")
	}
	root := t.TempDir()
	outside := filepath.Join(root, "other.json")
	if err = os.WriteFile(outside, []byte(`{"disabled":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "proxyclean-disabled.json")
	if err = os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err = loadProxyCleanDisabled(link); err == nil {
		t.Fatal("state symlink followed")
	}
	store, _ := loadProxyCleanDisabled("")
	for _, id := range []string{"../../config.json", "not-a-fingerprint", "fffffffffffffffz"} {
		if err = store.set(id, true); err == nil {
			t.Fatal("invalid id accepted", id)
		}
	}
}
