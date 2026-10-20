package gateway

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProxyCleanHTTPConnectRetainsTargetTLSValidation(t *testing.T) {
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer origin.Close()
	var connects atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", 400)
			return
		}
		if r.Host != strings.TrimPrefix(origin.URL, "https://") {
			http.Error(w, "unexpected target", 403)
			return
		}
		upstream, err := net.DialTimeout("tcp", r.Host, time.Second)
		if err != nil {
			http.Error(w, "dial failed", 502)
			return
		}
		client, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			upstream.Close()
			return
		}
		connects.Add(1)
		defer client.Close()
		defer upstream.Close()
		_, _ = buf.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buf.Flush()
		go func() { _, _ = io.Copy(upstream, client); upstream.Close() }()
		_, _ = io.Copy(client, upstream)
	}))
	defer proxy.Close()
	cfg := proxyCleanTestConfig()
	transports, err := transportPool([]string{proxy.URL}, cfg.Performance, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	route := transports.items[0]
	secureProxyCleanTransport(route)
	defer route.client.CloseIdleConnections()
	defer route.bun.CloseIdleConnections()
	for _, client := range []*http.Client{route.client, route.bun} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, origin.URL, nil)
		resp, err := client.Do(req)
		cancel()
		if resp != nil {
			resp.Body.Close()
		}
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "certificate") {
			t.Fatalf("untrusted target accepted or wrong error: %v", err)
		}
	}
	if connects.Load() != 2 {
		t.Fatalf("HTTP CONNECT transport not used: %d", connects.Load())
	}
}

func TestProxyCleanHTTPSProxyCertificateIsVerified(t *testing.T) {
	var calls atomic.Int64
	proxy := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "should not reach CONNECT", 500)
	}))
	defer proxy.Close()
	cfg := proxyCleanTestConfig()
	transports, err := transportPool([]string{proxy.URL}, cfg.Performance, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	route := transports.items[0]
	secureProxyCleanTransport(route)
	defer route.client.CloseIdleConnections()
	defer route.bun.CloseIdleConnections()
	for _, client := range []*http.Client{route.client, route.bun} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.com/", nil)
		resp, err := client.Do(req)
		cancel()
		if resp != nil {
			resp.Body.Close()
		}
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "certificate") {
			t.Fatalf("untrusted HTTPS proxy accepted: %v", err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("TLS bypassed before proxy CONNECT")
	}
}
