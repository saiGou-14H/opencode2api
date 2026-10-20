package gateway

import (
	"context"
	"io"
	"net/http"
	"opencode2api/internal/config"
	"strings"
	"testing"
)

func TestChinaCollectorsUseProtocolAwareAdapters(t *testing.T) {
	cfg := config.ProxyCleanConfig{Sources: []string{"proxifly-cn", "xyzs-cn", "monosans-cn"}}
	cfg.Defaults()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	c := newProxyCleanCollector(cfg)
	c.client = &http.Client{Transport: collectorRoundTrip(func(r *http.Request) (*http.Response, error) {
		var body string
		switch r.URL.String() {
		case proxyCleanSourceURLs["proxifly-cn"]:
			body = "http://8.8.8.8:8080\nsocks5://1.1.1.1:1080\nsocks4://9.9.9.9:1080"
		case proxyCleanSourceURLs["xyzs-cn"]:
			body = `[{"host":"9.9.9.9","port":8080,"country":"CN","protocol":"https","checkType":"https_connect_probe"}]`
		case proxyCleanSourceURLs["monosans-cn"]:
			body = `[{"host":"208.67.222.222","port":1080,"protocol":"socks5","geolocation":{"country":{"iso_code":"CN"}}},{"host":"8.8.4.4","port":80,"protocol":"http","geolocation":{"country":{"iso_code":"HK"}}}]`
		default:
			t.Errorf("unexpected source URL")
			body = ""
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	nodes, err := c.fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"http://8.8.8.8:8080", "http://9.9.9.9:8080", "socks5://208.67.222.222:1080", "socks5://1.1.1.1:1080"}
	if len(nodes) != len(want) {
		t.Fatal(nodes)
	}
	for i := range want {
		if nodes[i] != want[i] {
			t.Fatal(nodes)
		}
	}
	for _, v := range c.view() {
		if v.Status != "ready" {
			t.Fatal(v)
		}
	}
}
