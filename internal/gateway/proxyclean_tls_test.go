package gateway

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A local SOCKS fixture relays TLS without terminating it. The anonymous Bun
// transport must reject the test origin's untrusted certificate end-to-end.
func TestProxyCleanBunSOCKSVerifiesOriginCertificate(t *testing.T) {
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("untrusted origin received HTTP request") }))
	defer origin.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		greeting := make([]byte, 3)
		if _, err = io.ReadFull(conn, greeting); err != nil {
			return
		}
		conn.Write([]byte{5, 0})
		head := make([]byte, 4)
		if _, err = io.ReadFull(conn, head); err != nil {
			return
		}
		size := 0
		switch head[3] {
		case 1:
			size = 4
		case 4:
			size = 16
		case 3:
			b := make([]byte, 1)
			if _, err = io.ReadFull(conn, b); err != nil {
				return
			}
			size = int(b[0])
		default:
			return
		}
		if _, err = io.CopyN(io.Discard, conn, int64(size+2)); err != nil {
			return
		}
		upstream, err := net.DialTimeout("tcp", strings.TrimPrefix(origin.URL, "https://"), time.Second)
		if err != nil {
			return
		}
		defer upstream.Close()
		conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 1})
		copied := make(chan struct{})
		go func() { io.Copy(upstream, conn); upstream.Close(); close(copied) }()
		io.Copy(conn, upstream)
		conn.Close()
		<-copied
	}()
	cfg := proxyCleanTestConfig()
	transports, err := transportPool([]string{"socks5://" + listener.Addr().String()}, cfg.Performance, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	p := transports.items[0]
	secureProxyCleanTransport(p)
	defer p.bun.CloseIdleConnections()
	p.bun.Timeout = 3 * time.Second
	resp, err := p.bun.Get(origin.URL)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("expected certificate rejection through SOCKS, got %v", err)
	}
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("SOCKS fixture did not stop")
	}
}
