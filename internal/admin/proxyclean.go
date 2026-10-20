package admin

import (
	"errors"
	"net/http"
	"opencode2api/internal/gateway"
	"opencode2api/internal/httpx"
)

func (a *Server) handleProxyClean(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, a.manager.ProxyCleanStatus())
}
func (a *Server) handleProxyCleanNodes(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, a.manager.ProxyCleanNodes())
}
func writeProxyCleanError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	code := "proxyclean_state_failed"
	switch {
	case errors.Is(err, gateway.ErrProxyCleanDisabled):
		status = http.StatusConflict
		code = "proxyclean_disabled"
	case errors.Is(err, gateway.ErrProxyCleanBusy):
		status = http.StatusConflict
		code = "proxyclean_busy"
	case errors.Is(err, gateway.ErrProxyCleanUnavailable):
		status = http.StatusServiceUnavailable
		code = "proxyclean_unavailable"
	case errors.Is(err, gateway.ErrProxyCleanNodeNotFound):
		status = http.StatusNotFound
		code = "proxyclean_node_not_found"
	}
	writeAdminError(w, status, code, code)
}
func (a *Server) proxyCleanOperation(w http.ResponseWriter, operation string) {
	if err := a.manager.ProxyCleanOperation(operation); err != nil {
		writeProxyCleanError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "operation": operation})
}
func (a *Server) handleProxyCleanRefresh(w http.ResponseWriter, r *http.Request) {
	a.proxyCleanOperation(w, "refresh")
}
func (a *Server) handleProxyCleanProbe(w http.ResponseWriter, r *http.Request) {
	a.proxyCleanOperation(w, "probe")
}
func (a *Server) proxyCleanDisable(w http.ResponseWriter, r *http.Request, disabled bool) {
	if err := a.manager.ProxyCleanSetDisabled(r.PathValue("id"), disabled); err != nil {
		writeProxyCleanError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
func (a *Server) handleProxyCleanDisable(w http.ResponseWriter, r *http.Request) {
	a.proxyCleanDisable(w, r, true)
}
func (a *Server) handleProxyCleanEnable(w http.ResponseWriter, r *http.Request) {
	a.proxyCleanDisable(w, r, false)
}
