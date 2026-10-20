package admin

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"opencode2api/internal/config"
	"opencode2api/internal/gateway"
)

func TestProxyCleanStatusRequiresExistingAdminSession(t *testing.T) {
	a := New(&gateway.RuntimeController{}, nil, nil, slog.Default())
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/proxyclean", nil)
	a.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unprotected status: %d", w.Code)
	}
	a.sessions[tokenDigest("fixture")] = adminSession{AuthVersion: config.Fingerprint(""), Expires: time.Now().Add(time.Minute)}
	r.AddCookie(&http.Cookie{Name: adminCookieName, Value: "fixture"})
	w = httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var status gateway.ProxyCleanStatus
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Enabled || status.Scope != "anonymous-only" || status.SourceStatus != "disabled" {
		t.Fatal(status)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("status cached")
	}
}

func TestProxyCleanConfigUpdateOmission(t *testing.T) {
	var update ConfigUpdate
	if err := json.Unmarshal([]byte(`{}`), &update); err != nil || update.ProxyClean != nil {
		t.Fatal("legacy omission not preserved", err)
	}
	if err := json.Unmarshal([]byte(`{"proxyclean":{"enabled":false}}`), &update); err != nil || update.ProxyClean == nil || update.ProxyClean.Enabled {
		t.Fatal("explicit disable lost", err)
	}
}
