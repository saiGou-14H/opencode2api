package httpx

import (
	"bytes"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// Scripted connection makes read-ahead and the exact consumed-byte budget
// deterministic without involving external proxies or trusting test certificates.
type connectBudgetConn struct {
	input     *bytes.Reader
	written   bytes.Buffer
	readBytes int
	closed    bool
}

func (c *connectBudgetConn) Read(p []byte) (int, error) {
	n, e := c.input.Read(p)
	c.readBytes += n
	return n, e
}
func (c *connectBudgetConn) Write(p []byte) (int, error)      { return c.written.Write(p) }
func (c *connectBudgetConn) Close() error                     { c.closed = true; return nil }
func (c *connectBudgetConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *connectBudgetConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *connectBudgetConn) SetDeadline(time.Time) error      { return nil }
func (c *connectBudgetConn) SetReadDeadline(time.Time) error  { return nil }
func (c *connectBudgetConn) SetWriteDeadline(time.Time) error { return nil }

func connectResponseOfSize(n int) string {
	const prefix = "HTTP/1.1 200 Connection Established\r\nX-Padding: "
	const suffix = "\r\n\r\n"
	return prefix + strings.Repeat("x", n-len(prefix)-len(suffix)) + suffix
}

func TestHTTPConnectHeaderBudgetRejectsOversizedResponses(t *testing.T) {
	for name, response := range map[string]string{
		"status_line":   "HTTP/1.1 200 " + strings.Repeat("x", connectHeaderLimit*2),
		"single_header": "HTTP/1.1 200 OK\r\nX-Padding: " + strings.Repeat("x", connectHeaderLimit*2),
		"many_headers":  "HTTP/1.1 200 OK\r\n" + strings.Repeat("X-Padding: x\r\n", connectHeaderLimit) + "\r\n",
		"one_byte_over": connectResponseOfSize(connectHeaderLimit + 1),
	} {
		t.Run(name, func(t *testing.T) {
			c := &connectBudgetConn{input: bytes.NewReader([]byte(response))}
			_, err := httpConnect(c, nil, "example.com:443")
			if !errors.Is(err, errConnectHeadersTooLarge) {
				t.Fatalf("expected budget error, got %v", err)
			}
			if c.readBytes > connectHeaderLimit {
				t.Fatalf("read %d bytes beyond budget", c.readBytes)
			}
			if !c.closed {
				t.Fatal("oversized proxy connection left open")
			}
		})
	}
}

func TestHTTPConnectHeaderBudgetDoesNotLimitTunnel(t *testing.T) {
	payload := bytes.Repeat([]byte{0x16, 0x03, 0x03, 0x00, 0xff, 0x42}, 50000)
	for name, header := range map[string]string{
		"prefetched_tunnel":  "HTTP/1.1 200 Connection Established\r\n\r\n",
		"exact_header_limit": connectResponseOfSize(connectHeaderLimit),
		"one_byte_under":     connectResponseOfSize(connectHeaderLimit - 1),
	} {
		t.Run(name, func(t *testing.T) {
			input := append([]byte(header), payload...)
			c := &connectBudgetConn{input: bytes.NewReader(input)}
			tunnel, err := httpConnect(c, nil, "example.com:443")
			if err != nil {
				t.Fatal(err)
			}
			if name == "prefetched_tunnel" {
				if _, ok := tunnel.(*bufferedConn); !ok {
					t.Fatal("test did not exercise buffered tunnel bytes")
				}
			}
			got, err := io.ReadAll(tunnel)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, payload) {
				t.Fatalf("tunnel corrupted/truncated: got %d want %d", len(got), len(payload))
			}
			if c.closed {
				t.Fatal("successful tunnel closed")
			}
			if !strings.HasPrefix(c.written.String(), "CONNECT example.com:443 HTTP/1.1\r\n") {
				t.Fatal("missing CONNECT request")
			}
		})
	}
}
