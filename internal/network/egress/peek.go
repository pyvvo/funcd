package egress

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"
)

// errPeekDone is the sentinel returned from GetConfigForClient once the ClientHello (and thus the SNI) has
// been observed. It aborts the handshake immediately — we never complete a real handshake or dial anything;
// we only need ClientHelloInfo.ServerName, which is already captured by the time this fires.
var errPeekDone = errors.New("egress: clienthello peeked")

// parseClientHelloSNI extracts the SNI server_name from a TLS ClientHello record (pure, no I/O). It feeds
// the buffered bytes to a stdlib crypto/tls server handshake purely to capture ClientHelloInfo.ServerName;
// the handshake is deliberately aborted (no real TLS, no dial). Any malformed/short/non-TLS input yields
// ("", false). The result is only a CONFIRMATION HINT — it authorizes nothing unless the forwarder has
// already attested it for the destination IP (the B1 invariant; see buildDestination).
func parseClientHelloSNI(b []byte) (string, bool) {
	if len(b) == 0 {
		return "", false
	}
	var serverName string
	cfg := &tls.Config{ //nolint:gosec // no handshake is completed; this only inspects the ClientHello.
		GetConfigForClient: func(chi *tls.ClientHelloInfo) (*tls.Config, error) {
			serverName = chi.ServerName
			return nil, errPeekDone
		},
	}
	// A zero-length write deadline keeps the aborted handshake from blocking on the discard-writer.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = tls.Server(&bufConn{r: bytes.NewReader(b)}, cfg).HandshakeContext(ctx)
	if serverName == "" {
		return "", false
	}
	return strings.ToLower(serverName), true
}

// parseHTTPHost extracts the Host header from a plaintext HTTP/1.x request preamble (pure, no I/O) using the
// stdlib net/http request parser. A non-HTTP or headerless buffer returns ("", false). Like SNI, the result
// is only a confirmation hint — never the authorization basis.
func parseHTTPHost(b []byte) (string, bool) {
	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(b)))
	if err != nil {
		return "", false
	}
	host := req.Host
	if host == "" {
		return "", false
	}
	// Strip any :port — the destination port comes from SO_ORIGINAL_DST, not the Host header.
	if h, _, splitErr := net.SplitHostPort(host); splitErr == nil {
		host = h
	}
	if host == "" {
		return "", false
	}
	return strings.ToLower(host), true
}

// bufConn adapts a byte buffer to net.Conn so tls.Server can read a peeked ClientHello without any real
// socket. Read drains the buffer then reports io.EOF; Write discards (the handshake reply is never sent);
// the remaining net.Conn surface is inert. It performs no I/O beyond the in-memory buffer.
type bufConn struct {
	r *bytes.Reader
}

func (c *bufConn) Read(p []byte) (int, error)       { return c.r.Read(p) }
func (c *bufConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *bufConn) Close() error                     { return nil }
func (c *bufConn) LocalAddr() net.Addr              { return emptyAddr{} }
func (c *bufConn) RemoteAddr() net.Addr             { return emptyAddr{} }
func (c *bufConn) SetDeadline(time.Time) error      { return nil }
func (c *bufConn) SetReadDeadline(time.Time) error  { return nil }
func (c *bufConn) SetWriteDeadline(time.Time) error { return nil }

// emptyAddr is a zero-value net.Addr for the in-memory bufConn.
type emptyAddr struct{}

func (emptyAddr) Network() string { return "" }
func (emptyAddr) String() string  { return "" }
