package egress

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/require"
)

// bindUDPTCP binds one loopback port on both UDP and TCP, as the forwarder (and its upstream) serve.
func bindUDPTCP(t *testing.T) (net.PacketConn, net.Listener) {
	t.Helper()
	for range 20 {
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		require.NoError(t, err)
		ln, err := net.Listen("tcp", pc.LocalAddr().String())
		if err == nil {
			return pc, ln
		}
		_ = pc.Close()
	}
	t.Fatal("no loopback port free on both UDP and TCP")
	return nil, nil
}

// exchangeUntilServed retries q until the forwarder answers on network, so the test waits for Serve's
// listeners instead of sleeping. The receive buffer is large because the forwarder relays a truncated
// answer re-packed without compression, which can exceed 512 bytes.
func exchangeUntilServed(t *testing.T, network string, addr netip.AddrPort, q *dns.Msg) *dns.Msg {
	t.Helper()
	client := &dns.Client{Net: network, Timeout: time.Second, UDPSize: dns.DefaultMsgSize}
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, _, err := client.Exchange(q, addr.String())
		if err == nil {
			return resp
		}
		if time.Now().After(deadline) {
			require.NoError(t, err, "DNS over %s to the forwarder", network)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Issue 137: nftables redirects worker TCP :53 into the forwarder (dnsForwarderProtos), so a stub's
// RFC 7766 TCP retry of a truncated UDP answer must be served, and the full answer attested.
func TestIssue137_ForwarderServesDNSOverTCP(t *testing.T) {
	const name = "big.example.com."
	ips := make([]net.IP, 40)
	for i := range ips {
		ips[i] = net.IPv4(203, 0, 113, byte(i+1))
	}

	upPC, upLn := bindUDPTCP(t)
	mux := dns.NewServeMux()
	mux.HandleFunc(".", func(w dns.ResponseWriter, req *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(req)
		for _, ip := range ips {
			m.Answer = append(m.Answer, &dns.A{
				Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
				A:   ip,
			})
		}
		if _, udp := w.RemoteAddr().(*net.UDPAddr); udp {
			m.Truncate(dns.MinMsgSize)
		}
		_ = w.WriteMsg(m)
	})
	for _, srv := range []*dns.Server{{PacketConn: upPC, Handler: mux}, {Listener: upLn, Handler: mux}} {
		go func() { _ = srv.ActivateAndServe() }()
		t.Cleanup(func() { _ = srv.Shutdown() })
	}
	t.Cleanup(func() { _ = upPC.Close(); _ = upLn.Close() })

	pc, ln := bindUDPTCP(t)
	listen := netip.MustParseAddrPort(ln.Addr().String())
	require.NoError(t, pc.Close())
	require.NoError(t, ln.Close())

	fwd := NewForwarder(listen, netip.MustParseAddrPort(upLn.Addr().String()), nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- fwd.Serve(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	q := new(dns.Msg).SetQuestion(name, dns.TypeA)
	udpResp := exchangeUntilServed(t, "udp", listen, q)
	require.True(t, udpResp.Truncated, "the large answer arrives truncated over UDP, forcing a TCP retry")

	tcpResp := exchangeUntilServed(t, "tcp", listen, q)
	require.False(t, tcpResp.Truncated, "the TCP retry carries the whole answer")
	require.Len(t, tcpResp.Answer, len(ips))

	last, ok := netip.AddrFromSlice(ips[len(ips)-1].To4())
	require.True(t, ok)
	require.Equal(t, []string{"big.example.com"}, fwd.DomainsFor(netip.MustParseAddr("127.0.0.1"), last),
		"an IP only the TCP answer carries is attested")
}
