package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"opencode2api/internal/config"
	"opencode2api/internal/identity"
	wire "opencode2api/internal/protocol"
)

func disablePublicSwitch(g *Gateway) {
	off := false
	g.cfg.ProxyClean.AutoSwitchProxy = &off
	g.publicPool = &proxyCleanPool{}
}

func TestPublicSwitchOffDoesNotRestrictPaidKeysOrTiers(t *testing.T) {
	for _, firstStatus := range []int{503, 400} {
		var keys, urls []string
		g := retryGateway(t, 5, func(r *http.Request) (*http.Response, error) {
			keys = append(keys, r.Header.Get("Authorization"))
			urls = append(urls, r.URL.String())
			if len(keys) == 1 {
				return retryResponse(firstStatus), nil
			}
			return retryResponse(200), nil
		})
		disablePublicSwitch(g)
		resp, _, err := g.doUpstream(WithDiagnosticRequest(context.Background()), retryRoute(), retryBodies(), identity.RequestIDs{})
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if len(keys) != 2 || keys[0] == keys[1] || resp.StatusCode != 200 {
			t.Fatalf("status=%d keys=%v response=%d", firstStatus, keys, resp.StatusCode)
		}
		if firstStatus == 400 && urls[0] == urls[1] {
			t.Fatalf("paid tier fallback suppressed: %v", urls)
		}
	}
}

func TestPublicSwitchOffDoesNotRestrictStaleReasoningRepair(t *testing.T) {
	calls := 0
	g := retryGateway(t, 3, func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			resp := retryResponse(400)
			resp.Body = io.NopCloser(strings.NewReader(`{"error":"reasoning item not found"}`))
			return resp, nil
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "previous_response_id") {
			t.Fatal("stale repair was not applied")
		}
		return retryResponse(200), nil
	})
	disablePublicSwitch(g)
	route := retryRoute()
	route.Protocol = wire.Responses
	route.KeyTiers = nil
	bodies := map[config.Tier][]byte{config.TierZen: []byte(`{"previous_response_id":"old","input":[]}`)}
	resp, _, err := g.doUpstream(WithDiagnosticRequest(context.Background()), route, bodies, identity.RequestIDs{})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if calls != 2 || resp.StatusCode != 200 {
		t.Fatalf("calls=%d status=%d", calls, resp.StatusCode)
	}
}

func TestPublicSwitchOffDoesNotRestrictRotationModels(t *testing.T) {
	var attemptedModels []string
	g := retryGateway(t, 8, func(r *http.Request) (*http.Response, error) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		model, _ := payload["model"].(string)
		attemptedModels = append(attemptedModels, model)
		if len(attemptedModels) <= 3 {
			return retryResponse(503), nil
		}
		return retryResponse(403), nil
	})
	disablePublicSwitch(g)
	g.catalog.Replace(nil, config.SOTAModels)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req = req.WithContext(WithDiagnosticRequest(req.Context()))
	w := httptest.NewRecorder()
	g.handleRotation(w, req, wire.Chat, map[string]any{"model": "sota", "messages": []any{}}, "sota", "sota")
	if len(attemptedModels) != 4 || attemptedModels[0] != attemptedModels[2] || attemptedModels[2] == attemptedModels[3] || w.Code != 403 {
		t.Fatalf("models=%v status=%d", attemptedModels, w.Code)
	}
}
