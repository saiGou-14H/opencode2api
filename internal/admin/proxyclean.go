package admin

import (
	"net/http"
	"opencode2api/internal/httpx"
)

func (a *Server) handleProxyClean(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, a.manager.ProxyCleanStatus())
}
