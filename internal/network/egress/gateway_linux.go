//go:build linux

package egress

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/pyvvo/funcd/api/fault"
)

// newGateway returns the Linux transparent egress driver (ADR-0117). It listens on GatewayPort in the
// host netns for F80-redirected worker connections.
func newGateway(d Deps) Gateway { return &linuxGateway{deps: d} }

// linuxGateway is the transparent egress PEP driver. Per accepted connection it recovers the pre-DNAT
// destination (SO_ORIGINAL_DST), authenticates the caller by source IP, peeks an SNI/Host confirmation
// hint, asks the PDP, and splices on ALLOW / refuses on DENY — auditing every connection.
type linuxGateway struct {
	deps Deps

	mu sync.Mutex
	ln net.Listener
}

func (g *linuxGateway) Serve(ctx context.Context) error {
	const op = "egress.linuxGateway.Serve"
	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "tcp", netip.AddrPortFrom(netip.AddrFrom4([4]byte{0, 0, 0, 0}), g.deps.GatewayPort).String())
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "listen on egress gateway port %d", g.deps.GatewayPort)
	}
	g.mu.Lock()
	g.ln = ln
	g.mu.Unlock()

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, aerr := ln.Accept()
		if aerr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fault.Wrapf(aerr, fault.Internal, op, "accept redirected connection")
		}
		tc, ok := conn.(*net.TCPConn)
		if !ok {
			_ = conn.Close()
			continue
		}
		go g.handle(ctx, tc)
	}
}

func (g *linuxGateway) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ln != nil {
		return g.ln.Close()
	}
	return nil
}

// handle enforces one redirected connection.
func (g *linuxGateway) handle(ctx context.Context, c *net.TCPConn) {
	defer func() { _ = c.Close() }()

	dst, err := originalDst(c)
	if err != nil {
		slog.Warn("egress: cannot recover original destination", "err", err)
		return
	}
	src, ok := srcAddr(c)
	if !ok {
		return
	}
	asserted, replayed := peek(c)

	dec, rec, derr := decideConnect(ctx, g.deps, src, dst, asserted)
	if derr != nil {
		slog.Warn("egress: authorization error", "err", derr, "dst", dst.String())
		return
	}
	if g.deps.Audit != nil {
		g.deps.Audit.Egress(ctx, rec)
	}
	if !dec.Allowed {
		writeRefusal(replayed, dst.Port())
		return
	}

	up, derr := net.Dial("tcp", dst.String())
	if derr != nil {
		slog.Warn("egress: dial allowed destination failed", "err", derr, "dst", dst.String())
		return
	}
	defer func() { _ = up.Close() }()
	if serr := splice(replayed, up); serr != nil {
		slog.Debug("egress: splice ended", "err", serr, "dst", dst.String())
	}
}

// originalDst recovers the pre-DNAT destination of a REDIRECTed connection via getsockopt(SO_ORIGINAL_DST).
func originalDst(c *net.TCPConn) (netip.AddrPort, error) {
	const op = "egress.originalDst"
	raw, err := c.SyscallConn()
	if err != nil {
		return netip.AddrPort{}, fault.Wrapf(err, fault.Internal, op, "raw conn")
	}
	var mreq *unix.IPv6Mreq
	var optErr error
	if cerr := raw.Control(func(fd uintptr) {
		mreq, optErr = unix.GetsockoptIPv6Mreq(int(fd), unix.IPPROTO_IP, unix.SO_ORIGINAL_DST)
	}); cerr != nil {
		return netip.AddrPort{}, fault.Wrapf(cerr, fault.Internal, op, "control raw conn")
	}
	if optErr != nil {
		return netip.AddrPort{}, fault.Wrapf(optErr, fault.Internal, op, "getsockopt SO_ORIGINAL_DST")
	}
	// mreq.Multiaddr holds a sockaddr_in: family(2), port(2 BE), addr(4).
	port := uint16(mreq.Multiaddr[2])<<8 | uint16(mreq.Multiaddr[3])
	ip := netip.AddrFrom4([4]byte{mreq.Multiaddr[4], mreq.Multiaddr[5], mreq.Multiaddr[6], mreq.Multiaddr[7]})
	return netip.AddrPortFrom(ip, port), nil
}

// srcAddr extracts the worker source IP from the connection's remote address.
func srcAddr(c *net.TCPConn) (netip.Addr, bool) {
	ta, ok := c.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return netip.Addr{}, false
	}
	a, ok := netip.AddrFromSlice(ta.IP)
	return a.Unmap(), ok
}

// peek reads the connection preamble (with a short deadline) to extract an SNI/Host confirmation hint,
// returning a replayed conn that re-serves the peeked bytes. A timeout / raw-TCP connect yields ("", conn).
func peek(c *net.TCPConn) (string, net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 4096)
	n, _ := c.Read(buf)
	_ = c.SetReadDeadline(time.Time{})
	head := buf[:n]
	replayed := &peekedConn{Conn: c, prefix: append([]byte(nil), head...)}
	if name, ok := parseClientHelloSNI(head); ok {
		return name, replayed
	}
	if host, ok := parseHTTPHost(head); ok {
		return host, replayed
	}
	return "", replayed
}

// peekedConn re-serves the peeked prefix before reading the underlying conn (transparent replay).
type peekedConn struct {
	net.Conn
	prefix []byte
}

func (p *peekedConn) Read(b []byte) (int, error) {
	if len(p.prefix) > 0 {
		n := copy(b, p.prefix)
		p.prefix = p.prefix[n:]
		return n, nil
	}
	return p.Conn.Read(b)
}

// splice copies bytes bidirectionally between the worker and the upstream until either side closes.
func splice(a, b net.Conn) error {
	errc := make(chan error, 2)
	cp := func(dst, src net.Conn) {
		_, err := io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		errc <- err
	}
	go cp(a, b)
	go cp(b, a)
	err1 := <-errc
	err2 := <-errc
	if err1 != nil {
		return err1
	}
	return err2
}

// writeRefusal returns a descriptive refusal on a DENY: a 403 (RFC 9457) for the HTTP ports, else a plain
// close (RST/close) for raw TCP/TLS.
func writeRefusal(c net.Conn, port uint16) {
	if port == 80 || port == 8080 {
		const body = `{"type":"about:blank","title":"Forbidden","status":403,"detail":"egress denied by funcd EgressPolicy"}`
		_, _ = io.WriteString(c, "HTTP/1.1 403 Forbidden\r\nContent-Type: application/problem+json\r\nContent-Length: "+itoa(len(body))+"\r\nConnection: close\r\n\r\n"+body)
	}
	// otherwise: return (defer close ⇒ RST/close for raw TCP/TLS)
}

// itoa avoids a strconv import for the single small length value.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
