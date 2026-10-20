package gateway

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"time"

	"opencode2api/internal/config"
)

const proxyCleanProbeURL = "https://opencode.ai/"
const proxyCleanMaxBytes = 2 << 20

var nonPublicPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20"} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

func publicProxyIP(ip netip.Addr) bool {
	if !ip.IsValid() || ip.Zone() != "" || ip.Is4In6() || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, p := range nonPublicPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// parseProxyCleanList accepts the published optional socks5:// prefix and
// #country comment, never hostnames, credentials or opaque URL components.
func parseProxyCleanList(r io.Reader, limit int) ([]string, error) {
	return parseProxyCleanListLimit(r, limit, proxyCleanMaxBytes)
}

func parseProxyCleanListLimit(r io.Reader, limit int, maxBytes int64) ([]string, error) {
	return parseProxyList(r, limit, maxBytes, "socks5", false)
}

func parseProxyList(r io.Reader, limit int, maxBytes int64, defaultProtocol string, multiProtocol bool) ([]string, error) {
	limited := &io.LimitedReader{R: r, N: maxBytes + 1}
	s := bufio.NewScanner(limited)
	s.Buffer(make([]byte, 4096), 4096)
	seen := map[string]bool{}
	var out []string
	for s.Scan() {
		if limited.N == 0 {
			return nil, errors.New("source_too_large")
		}
		normalized := normalizeProxyCandidate(s.Text(), defaultProtocol, multiProtocol)
		if normalized == "" {
			continue
		}
		if !seen[normalized] && len(out) < limit {
			seen[normalized] = true
			out = append(out, normalized)
		}
	}
	if limited.N == 0 {
		return nil, errors.New("source_too_large")
	}
	if s.Err() != nil {
		return nil, errors.New("source_invalid")
	}
	if len(out) == 0 {
		return nil, errors.New("source_empty")
	}
	return out, nil
}

func noProxyCleanRedirect(*http.Request, []*http.Request) error {
	return errors.New("redirect_blocked")
}

// Source resolution is pinned to a validated public IP for each TCP dial;
// no environment proxy, DNS rebinding to a private address, or redirect is used.
func proxyCleanSourceClient() *http.Client {
	t := &http.Transport{TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 10 * time.Second}
	t.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.New("source_address")
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, errors.New("source_dns")
		}
		for _, ip := range ips {
			if !publicProxyIP(ip.Unmap()) {
				return nil, errors.New("source_address")
			}
		}
		d := net.Dialer{Timeout: 10 * time.Second}
		for _, ip := range ips {
			conn, e := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if e == nil {
				return conn, nil
			}
		}
		return nil, errors.New("source_connect")
	}
	return &http.Client{Transport: t, Timeout: 20 * time.Second, CheckRedirect: noProxyCleanRedirect}
}

func fetchProxyClean(ctx context.Context, c config.ProxyCleanConfig, client *http.Client) ([]string, error) {
	return fetchProxyCleanLimit(ctx, c, client, proxyCleanMaxBytes)
}

type proxyCleanSourceHTTPError struct{ RetryAfter time.Duration }

func (e *proxyCleanSourceHTTPError) Error() string { return "source_http_status" }

func fetchProxyCleanLimit(ctx context.Context, c config.ProxyCleanConfig, client *http.Client, maxBytes int64) ([]string, error) {
	return fetchProxyList(ctx, c, client, maxBytes, "socks5", false)
}

func fetchProxyList(ctx context.Context, c config.ProxyCleanConfig, client *http.Client, maxBytes int64, defaultProtocol string, multiProtocol bool) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.SourceURL, nil)
	if err != nil {
		return nil, errors.New("source_request")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("source_fetch_failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		delay := time.Duration(0)
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			delay = parseRetryAfter(resp.Header.Get("Retry-After"))
			if delay <= 0 {
				delay = time.Minute
			}
		}
		return nil, &proxyCleanSourceHTTPError{RetryAfter: delay}
	}
	return parseProxyList(resp.Body, c.MaxCandidates, maxBytes, defaultProtocol, multiProtocol)
}

// Both clients enforce end-to-end HTTPS, including the uTLS Bun SOCKS path.
// BunTransport.dial uses utls.Config{ServerName: ...}, not InsecureSkipVerify.
type proxyCleanHTTPS struct{ next http.RoundTripper }

func (t proxyCleanHTTPS) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || r.URL.User != nil {
		return nil, errors.New("public_proxy_requires_https")
	}
	return t.next.RoundTrip(r)
}
func (t proxyCleanHTTPS) CloseIdleConnections() {
	if c, ok := t.next.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

func secureProxyCleanTransport(p *proxyTransport) {
	p.public = true
	if t, ok := p.client.Transport.(*http.Transport); ok {
		// Never inherit a process-wide insecure TLS override.
		t.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	p.client = &http.Client{Transport: proxyCleanHTTPS{p.client.Transport}, CheckRedirect: noProxyCleanRedirect}
	p.bun = &http.Client{Transport: proxyCleanHTTPS{p.bun.Transport}, CheckRedirect: noProxyCleanRedirect}
}

func probeProxyClean(ctx context.Context, p *proxyTransport, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, proxyCleanProbeURL, nil)
	// Use the same verified Bun TLS path as anonymous inference, with no key or body.
	resp, err := p.bun.Do(req)
	if resp != nil {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
	}
	return err == nil && resp != nil && resp.StatusCode >= 200 && resp.StatusCode < 300
}
