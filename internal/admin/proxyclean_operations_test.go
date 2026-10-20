package admin

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opencode2api/internal/config"
	"opencode2api/internal/gateway"
)

func TestProxyCleanOperationsRequireSessionAndCSRF(t *testing.T) {
	a := New(&gateway.RuntimeController{}, nil, nil, slog.Default())
	a.sessions[tokenDigest("fixture")] = adminSession{AuthVersion: config.Fingerprint(""), CSRF: "fixture-csrf", Expires: time.Now().Add(time.Minute)}
	handler := a.Handler()
	for _, path := range []string{"/api/proxyclean/refresh", "/api/proxyclean/probe", "/api/proxyclean/nodes/node/disable", "/api/proxyclean/nodes/node/enable"} {
		for _, level := range []int{0, 1, 2} {
			r := httptest.NewRequest(http.MethodPost, path, nil)
			if level > 0 {
				r.AddCookie(&http.Cookie{Name: adminCookieName, Value: "fixture"})
			}
			if level > 1 {
				r.Header.Set("X-CSRF-Token", "fixture-csrf")
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			expected := []int{401, 403, 409}[level]
			if w.Code != expected {
				t.Fatalf("%s auth level %d: got %d, want %d (%s)", path, level, w.Code, expected, w.Body.String())
			}
			if level == 2 && !strings.Contains(w.Body.String(), "proxyclean_disabled") {
				t.Fatal("disabled gate missing")
			}
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/api/proxyclean/nodes", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("nodes unprotected")
	}
	r.AddCookie(&http.Cookie{Name: adminCookieName, Value: "fixture"})
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"nodes":[]`) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestProxyCleanSafeOperationErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{{gateway.ErrProxyCleanBusy, 409, "proxyclean_busy"}, {gateway.ErrProxyCleanUnavailable, 503, "proxyclean_unavailable"}, {gateway.ErrProxyCleanNodeNotFound, 404, "proxyclean_node_not_found"}, {gateway.ErrProxyCleanState, 500, "proxyclean_state_failed"}} {
		w := httptest.NewRecorder()
		writeProxyCleanError(w, tc.err)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
