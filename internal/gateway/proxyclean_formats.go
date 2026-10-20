package gateway

import (
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// normalizeProxyCandidate supports transport schemes implemented by both the
// standard and Bun clients. List credentials and non-public destinations are
// deliberately rejected; this collector is for public, untrusted proxies.
func normalizeProxyCandidate(line, defaultProtocol string, multiProtocol bool) string {
	line = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
	if line == "" || strings.HasPrefix(line, "#") {
		return ""
	}
	// Only whitespace-delimited # comments are comments; URL fragments are not.
	for i := 0; i < len(line); i++ {
		if line[i] == '#' && i > 0 && (line[i-1] == ' ' || line[i-1] == '\t') {
			line = strings.TrimSpace(line[:i])
			break
		}
	}
	if !strings.Contains(line, "://") {
		line = defaultProtocol + "://" + line
	}
	u, err := url.Parse(line)
	if err != nil || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if multiProtocol {
		switch scheme {
		case "http", "https", "socks5", "socks5h":
		default:
			return ""
		}
	} else if scheme != "socks5" {
		return ""
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		return ""
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !publicProxyIP(ip) {
		return ""
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return ""
	}
	return scheme + "://" + net.JoinHostPort(ip.String(), strconv.Itoa(n))
}
