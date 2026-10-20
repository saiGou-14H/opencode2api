package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"opencode2api/internal/config"
	"opencode2api/internal/identity"
	wire "opencode2api/internal/protocol"
)

func TestRotationRetryCapAndTerminalStatus(t *testing.T) {
	for _, status := range []int{503, 401, 403, 429} {
		calls := 0
		g := retryGateway(t, 2, func(r *http.Request) (*http.Response, error) {
			calls++
			resp := retryResponse(status)
			resp.Header.Set("Retry-After", "120")
			return resp, nil
		})
		g.catalog.Replace(nil, config.SOTAModels)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
		req = req.WithContext(WithDiagnosticRequest(req.Context()))
		w := httptest.NewRecorder()
		g.handleRotation(w, req, wire.Chat, map[string]any{"model": "sota", "messages": []any{}}, "sota", "sota")
		want := 1
		if status == 503 {
			want = 2
		}
		if calls != want {
			t.Fatalf("status=%d calls=%d want=%d body=%s", status, calls, want, w.Body.String())
		}
		if status != 503 && (w.Code != status || w.Header().Get("Retry-After") != "120") {
			t.Fatalf("terminal response not preserved: %d %v", w.Code, w.Header())
		}
	}
}

func TestAttemptCapAcrossAnonymousAndKeyTiers(t *testing.T) {
	calls := 0
	g := retryGateway(t, 3, func(r *http.Request) (*http.Response, error) { calls++; return retryResponse(503), nil })
	route := retryRoute()
	route.Anonymous = true
	resp, _, attempts, err := g.doUpstreamTiers(WithDiagnosticRequest(context.Background()), route, retryBodies(), identity.RequestIDs{}, 0)
	if resp != nil {
		resp.Body.Close()
	}
	if calls != 3 || attempts != 3 {
		t.Fatalf("calls=%d attempts=%d err=%v", calls, attempts, err)
	}
}

func TestAnonymousProtocolFallbackStillWorks(t *testing.T) {
	calls := 0
	g := retryGateway(t, 3, func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return retryResponse(400), nil
		}
		return retryResponse(200), nil
	})
	route := retryRoute()
	route.Anonymous = true
	resp, _, attempts, err := g.doUpstreamTiers(WithDiagnosticRequest(context.Background()), route, retryBodies(), identity.RequestIDs{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if calls != 2 || attempts != 2 || resp.StatusCode != 200 {
		t.Fatalf("calls=%d attempts=%d status=%d", calls, attempts, resp.StatusCode)
	}
}
