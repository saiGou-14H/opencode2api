package gateway

import (
	"opencode2api/internal/config"
	"time"
)

// Audited against the repositories' trees/READMEs and HTTP GET on 2026-10-20.
// gfpcom publishes finished nodes in its wiki, NOT main/sources/socks5.txt
// (that file is a list of other source URLs and must never be followed).
var proxyCleanSourceURLs = map[string]string{
	"proxifly-cn": "https://raw.githubusercontent.com/proxifly/free-proxy-list/main/proxies/countries/CN/data.txt",
	"xyzs-cn":     "https://raw.githubusercontent.com/xyzs996/free-proxy-health-list/main/proxies/countries/cn/data.json",
	"monosans-cn": "https://raw.githubusercontent.com/monosans/proxy-list/main/proxies.json",
	"proxyclean":  config.DefaultProxyCleanSource,
	"proxifly":    "https://raw.githubusercontent.com/proxifly/free-proxy-list/main/proxies/protocols/socks5/data.txt",
	"thespeedx":   "https://raw.githubusercontent.com/TheSpeedX/PROXY-List/master/socks5.txt",
	"roosterkid":  "https://raw.githubusercontent.com/roosterkid/openproxylist/main/SOCKS5_RAW.txt",
	"hookzof":     "https://raw.githubusercontent.com/hookzof/socks5_list/master/proxy.txt",
	"gfpcom":      "https://raw.githubusercontent.com/wiki/gfpcom/free-proxy-list/lists/socks5.txt",
	"dpangestuw":  "https://raw.githubusercontent.com/dpangestuw/Free-Proxy/main/socks5_proxies.txt",
}

func proxyCleanSourceBudget(id string) (int64, time.Duration) {
	if id == "gfpcom" {
		return 32 << 20, 45 * time.Second
	}
	if id == "proxyclean" {
		return proxyCleanMaxBytes, 20 * time.Second
	}
	return 4 << 20, 25 * time.Second
}
