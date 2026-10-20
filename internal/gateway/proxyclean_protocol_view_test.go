package gateway

import (
	"context"
	"testing"
)

func TestProxyCleanNodesExposeProtocol(t *testing.T) {
	p, _ := fixtureProxyClean(t)
	p.cfg.MaxCandidates = 4
	p.fetch = func(context.Context) ([]string, error) {
		return []string{"http://8.8.8.8:8080", "https://1.1.1.1:443", "socks5://9.9.9.9:1080", "socks5h://8.8.4.4:1080"}, nil
	}
	p.refresh(context.Background())
	nodes := p.nodesView().Nodes
	if len(nodes) != 4 {
		t.Fatalf("expected four protocols, got %d", len(nodes))
	}
	for i, protocol := range []string{"http", "https", "socks5", "socks5h"} {
		if nodes[i].Protocol != protocol {
			t.Fatalf("protocol %d: %q", i, nodes[i].Protocol)
		}
	}
}
