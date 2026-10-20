package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"opencode2api/internal/config"
	"opencode2api/internal/identity"
	"opencode2api/internal/models"
	"opencode2api/internal/protocol"
)

func TestProxyCleanEmptyPoolDoesNotFallBackToPaidKeys(t *testing.T) {
	cfg := proxyCleanTestConfig()
	g, err := New(cfg, slog.Default(), nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	paid := proxyCleanRoundTrip(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})
	for _, p := range g.transports.items {
		p.client.Transport = paid
		p.bun.Transport = paid
	}
	route := models.Route{Anonymous: true, Tier: config.TierZen, Protocol: protocol.Chat, KeyTiers: []config.Tier{config.TierZen}}
	resp, _, used, err := g.doUpstreamTiers(context.Background(), route, map[config.Tier][]byte{config.TierZen: []byte(`{"model":"fixture","messages":[]}`)}, identity.RequestIDs{}, 0)
	if resp != nil {
		resp.Body.Close()
	}
	if calls != 0 || used != 0 || !errors.Is(err, errProxyUnavailable) {
		t.Fatalf("empty public pool fell through: calls=%d used=%d error=%v", calls, used, err)
	}
}

func TestProxyCleanSourceBodySizeLimit(t *testing.T) {
	// Valid small lines still cannot make an unbounded source body acceptable.
	input := strings.Repeat("8.8.8.8:1080 #US\n", proxyCleanMaxBytes/10)
	_, err := parseProxyCleanList(strings.NewReader(input), 2)
	if err == nil || err.Error() != "source_too_large" {
		t.Fatalf("source limit missing: %v", err)
	}
}
