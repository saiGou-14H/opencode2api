package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"opencode2api/internal/config"
	"opencode2api/internal/identity"
	"opencode2api/internal/models"
	wire "opencode2api/internal/protocol"
)

func TestPublicPoolEmptyDoesNotFallBackToPaid(t *testing.T) {
	paidCalls := 0
	g := retryGateway(t, 5, func(*http.Request) (*http.Response, error) { paidCalls++; return retryResponse(200), nil })
	g.publicPool = &proxyCleanPool{}
	g.anonymous = &anonymousNodes{}
	if !g.transports.hasHealthy() {
		t.Fatal("test requires healthy static/paid transports")
	}
	route := retryRoute()
	route.Anonymous = true
	resp, _, attempts, err := g.doUpstreamTiers(WithDiagnosticRequest(context.Background()), route, retryBodies(), identity.RequestIDs{}, 0)
	if resp != nil || !errors.Is(err, errProxyUnavailable) || attempts != 0 || paidCalls != 0 {
		t.Fatalf("resp=%v err=%v attempts=%d paid=%d", resp, err, attempts, paidCalls)
	}
	// Exercise the real handler to prove the typed failure is returned as 503.
	g.catalog = models.Catalog(config.TierZen, map[string]string{"retry-free": string(wire.Chat)})
	g.catalog.Replace([]string{"retry-free"}, []string{"retry-free"})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"retry-free","messages":[]}`))
	req = req.WithContext(WithDiagnosticRequest(req.Context()))
	w := httptest.NewRecorder()
	g.handleInference(wire.Chat)(w, req)
	if w.Code != http.StatusServiceUnavailable || paidCalls != 0 || !strings.Contains(w.Body.String(), "proxy_error") {
		t.Fatalf("status=%d paid=%d body=%s", w.Code, paidCalls, w.Body.String())
	}
}

func TestPublicPoolFailureNeverUsesPaidFallback(t *testing.T) {
	for _, status := range []int{400, 401, 403, 429, 503} {
		paidCalls, anonymousCalls := 0, 0
		g := retryGateway(t, 5, func(r *http.Request) (*http.Response, error) {
			if r.Header.Get("Authorization") == "Bearer public" {
				anonymousCalls++
				return retryResponse(status), nil
			}
			paidCalls++
			return retryResponse(200), nil
		})
		g.publicPool = &proxyCleanPool{}
		route := retryRoute()
		route.Anonymous = true
		resp, _, _, err := g.doUpstreamTiers(WithDiagnosticRequest(context.Background()), route, retryBodies(), identity.RequestIDs{}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if resp == nil {
			t.Fatal("missing anonymous response")
		}
		resp.Body.Close()
		want := 2
		if status == 401 || status == 403 || status == 429 {
			want = 1
		}
		if resp.StatusCode != status || paidCalls != 0 || anonymousCalls != want {
			t.Fatalf("status=%d got=%d paid=%d anonymous=%d", status, resp.StatusCode, paidCalls, anonymousCalls)
		}
	}
}
