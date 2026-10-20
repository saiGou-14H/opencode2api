package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"opencode2api/internal/config"
)

func TestProxyCleanMultipleProtocolParsing(t *testing.T) {
	input := `8.8.8.8:080
http://8.8.8.8:80
https://8.8.8.8:80
socks5://8.8.8.8:80
socks5h://[2606:4700:4700::1111]:1080 # comment
http://user:pass@1.1.1.1:80
https://127.0.0.1:443
socks4://1.1.1.1:1080
http://proxy.example:80
http://8.8.8.8:80/path
http://8.8.8.8:80?token=x
http://8.8.8.8:80#fragment
`
	got, err := parseProxyList(strings.NewReader(input), 100, 4096, "http", true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"http://8.8.8.8:80", "https://8.8.8.8:80", "socks5://8.8.8.8:80", "socks5h://[2606:4700:4700::1111]:1080"}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("%v", got)
		}
	}
	strict, err := parseProxyCleanList(strings.NewReader(input), 100)
	if err != nil || len(strict) != 1 || strict[0] != "socks5://8.8.8.8:80" {
		t.Fatalf("builtins lost SOCKS5 boundary %v %v", strict, err)
	}
}

func TestProxyCleanCustomSourceCollection(t *testing.T) {
	cfg := config.ProxyCleanConfig{CustomSources: []config.ProxyCleanCustomSource{{URL: "https://example.com/proxies.txt", DefaultProtocol: "https"}}}
	cfg.Defaults()
	c := newProxyCleanCollector(cfg)
	c.client = &http.Client{Transport: collectorRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != cfg.CustomSources[0].URL {
			t.Errorf("wrong URL")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("8.8.8.8:443\nhttp://1.1.1.1:8080\nsocks5h://9.9.9.9:1080\n"))}, nil
	})}
	got, err := c.fetch(context.Background())
	if err != nil || len(got) != 3 {
		t.Fatalf("%v %v", got, err)
	}
	if got[0] != "https://8.8.8.8:443" {
		t.Fatal(got)
	}
	view := c.view()
	if len(view) != 1 || view[0].ID != "custom-"+config.Fingerprint(cfg.CustomSources[0].URL) || view[0].Status != "ready" {
		t.Fatal(view)
	}
}
