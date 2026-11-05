package egress

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	cedarauth "github.com/pyvvo/funcd/internal/auth/cedar"
)

// loopback is the source of every test query.
func loopback() netip.Addr { return netip.MustParseAddr("127.0.0.1") }

// noUpstream is an upstream no test reaches.
func noUpstream() netip.AddrPort { return netip.MustParseAddrPort("127.0.0.1:53") }

// fakeWorkers is a static WorkerIndex.
type fakeWorkers map[netip.Addr]auth.EntityRef

func (f fakeWorkers) Lookup(ip netip.Addr) (auth.EntityRef, bool) {
	ref, ok := f[ip]
	return ref, ok
}

// worker is a WorkerIndex holding the loopback source as a principal.
func worker(kind v1.Kind, name v1.ObjectName) fakeWorkers {
	return fakeWorkers{loopback(): {Type: kind, Namespace: "acme", Name: name}}
}

// fakeRules is a static DomainRules.
type fakeRules struct {
	rules []string
	err   error
}

func (f fakeRules) For(context.Context, auth.EntityRef) ([]string, error) { return f.rules, f.err }

func rules(r ...string) fakeRules { return fakeRules{rules: r} }

// policyRules reads EgressDomains over fixed policies, as the daemon's adapter does over the store.
type policyRules []*v1.EgressPolicy

func (p policyRules) For(_ context.Context, principal auth.EntityRef) ([]string, error) {
	var out []string
	for _, ep := range p {
		out = append(out, cedarauth.EgressDomains(ep, principal)...)
	}
	return out, nil
}

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
// listeners instead of sleeping. The client sends no EDNS0 OPT, so it reads at most 512 bytes over UDP.
func exchangeUntilServed(t *testing.T, network string, addr netip.AddrPort, q *dns.Msg) *dns.Msg {
	t.Helper()
	client := &dns.Client{Net: network, Timeout: time.Second}
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

// startUpstream serves answer from a loopback upstream that, over UDP, truncates it to 512 bytes (compressed, as a real
// resolver does), and counts the queries it receives.
func startUpstream(t *testing.T, answer func(req *dns.Msg) []dns.RR) (netip.AddrPort, *atomic.Int64) {
	t.Helper()
	upPC, upLn := bindUDPTCP(t)
	queries := new(atomic.Int64)
	mux := dns.NewServeMux()
	mux.HandleFunc(".", func(w dns.ResponseWriter, req *dns.Msg) {
		queries.Add(1)
		m := new(dns.Msg)
		m.SetReply(req)
		m.Answer = answer(req)
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
	return netip.MustParseAddrPort(upLn.Addr().String()), queries
}

// serveForwarder runs a forwarder in front of upstream, on a free loopback port.
func serveForwarder(t *testing.T, upstream netip.AddrPort, workers WorkerIndex, r DomainRules, logger *slog.Logger) (Forwarder, netip.AddrPort) {
	t.Helper()
	pc, ln := bindUDPTCP(t)
	listen := netip.MustParseAddrPort(ln.Addr().String())
	require.NoError(t, pc.Close())
	require.NoError(t, ln.Close())

	fwd := NewForwarder(listen, upstream, workers, r, logger)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- fwd.Serve(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return fwd, listen
}

// aRecord is an A record of name at ip.
func aRecord(name string, ip net.IP) *dns.A {
	return &dns.A{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: ip}
}

// startForwarder serves name → ips to a Function's worker a domain rule allows it.
func startForwarder(t *testing.T, name string, ips []net.IP) (Forwarder, netip.AddrPort) {
	t.Helper()
	upstream, _ := startUpstream(t, func(*dns.Msg) []dns.RR {
		answer := make([]dns.RR, 0, len(ips))
		for _, ip := range ips {
			answer = append(answer, aRecord(name, ip))
		}
		return answer
	})
	return serveForwarder(t, upstream, worker(v1.KindFunction, "etl"), rules(strings.TrimSuffix(name, ".")), nil)
}

// lookupRcode looks name up through the forwarder and returns the answer's rcode.
func lookupRcode(t *testing.T, network string, listen netip.AddrPort, name string) int {
	t.Helper()
	return exchangeUntilServed(t, network, listen, new(dns.Msg).SetQuestion(name, dns.TypeA)).Rcode
}

// answerA answers any question with one A record of its own name.
func answerA(req *dns.Msg) []dns.RR {
	return []dns.RR{aRecord(req.Question[0].Name, net.IPv4(203, 0, 113, 7))}
}

// scenario: lookup-allowed-name — an exact name and a name under a wildcard are answered, over UDP and TCP; the wildcard's
// bare suffix is not.
func TestScenarioLookupAllowedName(t *testing.T) {
	t.Parallel()
	for _, network := range []string{"udp", "tcp"} {
		t.Run(network, func(t *testing.T) {
			t.Parallel()
			upstream, queries := startUpstream(t, answerA)
			_, listen := serveForwarder(t, upstream, worker(v1.KindFunction, "etl"), rules("api.x.example", "*.y.example"), nil)

			require.Equal(t, dns.RcodeSuccess, lookupRcode(t, network, listen, "api.x.example."))
			require.Equal(t, dns.RcodeSuccess, lookupRcode(t, network, listen, "a.b.y.example."))
			require.EqualValues(t, 2, queries.Load())
			require.Equal(t, dns.RcodeRefused, lookupRcode(t, network, listen, "y.example."))
			require.EqualValues(t, 2, queries.Load(), "the bare suffix goes no further")
		})
	}
}

// scenario: lookup-case-and-trailing-dot — a name matches and is recorded lower-cased and with no trailing dot.
func TestScenarioLookupCaseAndTrailingDot(t *testing.T) {
	t.Parallel()
	upstream, _ := startUpstream(t, answerA)
	fwd, listen := serveForwarder(t, upstream, worker(v1.KindFunction, "etl"), rules("api.x.example", "*.y.example"), nil)

	require.Equal(t, dns.RcodeSuccess, lookupRcode(t, "udp", listen, "API.X.Example."))
	require.Equal(t, dns.RcodeSuccess, lookupRcode(t, "udp", listen, "A.Y.EXAMPLE."))
	dst := netip.MustParseAddr("203.0.113.7")
	require.Equal(t, []string{"*.y.example", "a.y.example", "api.x.example"}, fwd.DomainsFor(loopback(), dst))
}

// scenario: lookup-refusals — another name, no policy, an unknown source and a failed read each get REFUSED over UDP and TCP,
// and the upstream receives no query. A nil index or nil rules refuse too.
func TestScenarioLookupRefusals(t *testing.T) {
	t.Parallel()
	fn := worker(v1.KindFunction, "etl")
	for name, tc := range map[string]struct {
		workers WorkerIndex
		rules   DomainRules
	}{
		"another name":       {fn, rules("api.x.example", "*.y.example")},
		"no policy":          {fn, rules()},
		"unknown source":     {fakeWorkers{}, rules("other.example")},
		"failed policy read": {fn, fakeRules{rules: []string{"other.example"}, err: errors.New("store down")}},
		"nil index":          {nil, rules("other.example")},
		"nil rules":          {fn, nil},
	} {
		for _, network := range []string{"udp", "tcp"} {
			t.Run(name+"/"+network, func(t *testing.T) {
				t.Parallel()
				upstream, queries := startUpstream(t, answerA)
				fwd, listen := serveForwarder(t, upstream, tc.workers, tc.rules, nil)

				require.Equal(t, dns.RcodeRefused, lookupRcode(t, network, listen, "other.example."))
				require.Zero(t, queries.Load(), "nothing goes upstream")
				require.Empty(t, fwd.DomainsFor(loopback(), netip.MustParseAddr("203.0.113.7")))
			})
		}
	}
}

// scenario: lookup-follows-who-a-rule-covers — the rules a worker's lookup reads are the ones the PDP grants it.
func TestScenarioLookupFollowsWhoARuleCovers(t *testing.T) {
	t.Parallel()
	whole := &v1.EgressPolicy{ObjectMeta: v1.ObjectMeta{Name: "whole", Namespace: "acme"}, Spec: v1.EgressPolicySpec{
		Rules: []v1.EgressRule{{To: v1.EgressTo{Domains: []string{"whole.example"}}}},
	}}
	named := &v1.EgressPolicy{ObjectMeta: v1.ObjectMeta{Name: "named", Namespace: "acme"}, Spec: v1.EgressPolicySpec{
		AppliesTo: []v1.ObjectName{"etl"},
		Rules:     []v1.EgressRule{{To: v1.EgressTo{Domains: []string{"named.example"}}}},
	}}
	for _, tc := range []struct {
		name          string
		workers       fakeWorkers
		whole, namedR int
	}{
		{"named Function", worker(v1.KindFunction, "etl"), dns.RcodeSuccess, dns.RcodeSuccess},
		{"another Function", worker(v1.KindFunction, "web"), dns.RcodeSuccess, dns.RcodeRefused},
		{"a Lake's engine", worker(v1.KindLake, "etl"), dns.RcodeRefused, dns.RcodeRefused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			upstream, _ := startUpstream(t, answerA)
			_, listen := serveForwarder(t, upstream, tc.workers, policyRules{whole, named}, nil)

			require.Equal(t, tc.whole, lookupRcode(t, "udp", listen, "whole.example."))
			require.Equal(t, tc.namedR, lookupRcode(t, "udp", listen, "named.example."))
		})
	}
}

// A wildcard token comes from the rules that apply to the worker: Function web holds the name a whole-namespace rule gave it,
// and not the token of a rule that names only etl.
func TestWildcardTokenFollowsWhoARuleCovers(t *testing.T) {
	t.Parallel()
	whole := &v1.EgressPolicy{ObjectMeta: v1.ObjectMeta{Name: "whole", Namespace: "acme"}, Spec: v1.EgressPolicySpec{
		Rules: []v1.EgressRule{{To: v1.EgressTo{Domains: []string{"api.y.example"}}}},
	}}
	named := &v1.EgressPolicy{ObjectMeta: v1.ObjectMeta{Name: "named", Namespace: "acme"}, Spec: v1.EgressPolicySpec{
		AppliesTo: []v1.ObjectName{"etl"},
		Rules:     []v1.EgressRule{{To: v1.EgressTo{Domains: []string{"*.y.example"}}}},
	}}
	dst := netip.MustParseAddr("203.0.113.7")
	for name, want := range map[v1.ObjectName][]string{
		"etl": {"*.y.example", "api.y.example"},
		"web": {"api.y.example"},
	} {
		upstream, _ := startUpstream(t, answerA)
		fwd, listen := serveForwarder(t, upstream, worker(v1.KindFunction, name), policyRules{whole, named}, nil)
		require.Equal(t, dns.RcodeSuccess, lookupRcode(t, "udp", listen, "api.y.example."))
		require.Equal(t, want, fwd.DomainsFor(loopback(), dst), "Function %s", name)
	}
}

// An answer counts for its record's TTL.
func TestRecordKeepsTheAnswersTTL(t *testing.T) {
	t.Parallel()
	f := NewForwarder(noUpstream(), noUpstream(), nil, nil, nil).(*forwarder)
	now := time.Unix(1000, 0)
	f.corr.now = func() time.Time { return now }
	resp := new(dns.Msg)
	resp.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: "x.example.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 30}, A: net.IPv4(203, 0, 113, 7)}}
	f.record(loopback(), "x.example", resp)

	dst := netip.MustParseAddr("203.0.113.7")
	require.Equal(t, []string{"x.example"}, f.DomainsFor(loopback(), dst))
	now = now.Add(31 * time.Second)
	require.Empty(t, f.DomainsFor(loopback(), dst))
}

// scenario: lookup-not-a-query — only a standard query goes upstream.
func TestScenarioLookupNotAQuery(t *testing.T) {
	t.Parallel()
	upstream, queries := startUpstream(t, answerA)
	_, listen := serveForwarder(t, upstream, worker(v1.KindFunction, "etl"), rules("api.x.example"), nil)

	notify := new(dns.Msg).SetNotify("api.x.example.")
	require.Equal(t, dns.RcodeRefused, exchangeUntilServed(t, "udp", listen, notify).Rcode)
	update := new(dns.Msg).SetUpdate("api.x.example.")
	require.Equal(t, dns.RcodeNotImplemented, exchangeUntilServed(t, "udp", listen, update).Rcode, "the DNS server turns it down")
	two := new(dns.Msg).SetQuestion("api.x.example.", dns.TypeA)
	two.Question = append(two.Question, two.Question[0])
	require.Equal(t, dns.RcodeFormatError, exchangeUntilServed(t, "udp", listen, two).Rcode, "the DNS server turns it down")
	require.Zero(t, queries.Load())
}

// scenario: lookup-name-outside-host-characters — a name that does not hold only host-name characters gets REFUSED, under
// a wildcard too.
func TestScenarioLookupNameOutsideHostCharacters(t *testing.T) {
	t.Parallel()
	upstream, queries := startUpstream(t, answerA)
	fwd, listen := serveForwarder(t, upstream, worker(v1.KindFunction, "etl"), rules("*.y.example", "*.example"), nil)

	for _, name := range []string{`a\.y.example.`, `a,b.y.example.`, `a\032b.y.example.`, `a\#b.y.example.`, `a@b.y.example.`, `*.y.example.`} {
		require.Equal(t, dns.RcodeRefused, lookupRcode(t, "udp", listen, name), name)
	}
	require.Zero(t, queries.Load())
	require.Equal(t, dns.RcodeSuccess, lookupRcode(t, "udp", listen, "a_b-c.y.example."))
	require.Equal(t, []string{"*.example", "*.y.example", "a_b-c.y.example"}, fwd.DomainsFor(loopback(), netip.MustParseAddr("203.0.113.7")))
}

// scenario: rule-fires-through-a-cname — an answer reached through a CNAME counts for the name asked.
func TestScenarioRuleFiresThroughACNAME(t *testing.T) {
	t.Parallel()
	dst := net.IPv4(203, 0, 113, 7)
	upstream, _ := startUpstream(t, func(req *dns.Msg) []dns.RR {
		return []dns.RR{
			&dns.CNAME{Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: 60}, Target: "edge.cdn.example."},
			aRecord("edge.cdn.example.", dst),
		}
	})
	fwd, listen := serveForwarder(t, upstream, worker(v1.KindFunction, "etl"), rules("api.x.example", "*.y.example"), nil)

	require.Equal(t, dns.RcodeSuccess, lookupRcode(t, "udp", listen, "api.x.example."))
	require.Equal(t, dns.RcodeSuccess, lookupRcode(t, "udp", listen, "a.y.example."))
	require.Equal(t, []string{"*.y.example", "a.y.example", "api.x.example"}, fwd.DomainsFor(loopback(), netip.MustParseAddr("203.0.113.7")))
}

// scenario: answer-names-not-recorded — a name in the answer section counts for nothing.
func TestScenarioAnswerNamesNotRecorded(t *testing.T) {
	t.Parallel()
	dst := net.IPv4(203, 0, 113, 7)
	upstream, _ := startUpstream(t, func(*dns.Msg) []dns.RR { return []dns.RR{aRecord("b.y.example.", dst)} })
	fwd, listen := serveForwarder(t, upstream, worker(v1.KindFunction, "etl"), rules("a.x.example", "b.y.example"), nil)

	require.Equal(t, dns.RcodeSuccess, lookupRcode(t, "udp", listen, "a.x.example."))
	require.Equal(t, []string{"a.x.example"}, fwd.DomainsFor(loopback(), netip.MustParseAddr("203.0.113.7")))
}

// scenario: cidr-rule-name-refused — a rule with only a CIDR gives no name a lookup.
func TestScenarioCIDRRuleNameRefused(t *testing.T) {
	t.Parallel()
	ep := &v1.EgressPolicy{ObjectMeta: v1.ObjectMeta{Name: "cidr", Namespace: "acme"}, Spec: v1.EgressPolicySpec{
		Rules: []v1.EgressRule{{To: v1.EgressTo{CIDRs: []string{"1.1.1.1/32"}}, Ports: []int{443}}},
	}}
	upstream, queries := startUpstream(t, answerA)
	_, listen := serveForwarder(t, upstream, worker(v1.KindFunction, "etl"), policyRules{ep}, nil)

	require.Equal(t, dns.RcodeRefused, lookupRcode(t, "udp", listen, "one.one.one.one."))
	require.Zero(t, queries.Load())
}

// syncBuffer is a buffer a logger and a test read and write at once.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// scenario: refused-lookup-audited — repeated refusals write one line per source per interval.
func TestScenarioRefusedLookupAudited(t *testing.T) {
	t.Parallel()
	var logs syncBuffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	upstream, _ := startUpstream(t, answerA)
	_, listen := serveForwarder(t, upstream, worker(v1.KindFunction, "etl"), rules("api.x.example"), logger)
	for i := range 5 {
		require.Equal(t, dns.RcodeRefused, lookupRcode(t, "udp", listen, fmt.Sprintf("n%d.other.example.", i)))
	}
	require.Equal(t, 1, strings.Count(logs.String(), "egress lookup refused"), logs.String())
	for _, want := range []string{"namespace=acme", "function=etl", "domain=n0.other.example", "source=127.0.0.1", "more=0", `reason="no domain rule allows it"`} {
		require.Contains(t, logs.String(), want)
	}

	var unknown syncBuffer
	_, listen = serveForwarder(t, upstream, fakeWorkers{}, rules("api.x.example"), slog.New(slog.NewTextHandler(&unknown, nil)))
	require.Equal(t, dns.RcodeRefused, lookupRcode(t, "udp", listen, "other.example."))
	require.Contains(t, unknown.String(), `domain="" source=127.0.0.1 reason="unknown source"`, "a source that is no worker has its name left out")

	t.Run("sources that are no worker share one allowance", func(t *testing.T) {
		t.Parallel()
		var logs syncBuffer
		f := NewForwarder(noUpstream(), noUpstream(), fakeWorkers{}, rules(), slog.New(slog.NewTextHandler(&logs, nil))).(*forwarder)
		req := new(dns.Msg).SetQuestion("a.example.", dns.TypeA)
		for _, src := range []string{"10.63.0.5", "10.63.0.6", "192.0.2.2"} {
			_, ok := f.admit(context.Background(), netip.MustParseAddr(src), req)
			require.False(t, ok)
		}
		require.Equal(t, 1, strings.Count(logs.String(), "egress lookup refused"), logs.String())
		require.Len(t, f.refused.open, 1)
	})

	t.Run("the next line counts what was held back", func(t *testing.T) {
		t.Parallel()
		var logs syncBuffer
		f := NewForwarder(noUpstream(), noUpstream(), worker(v1.KindFunction, "etl"), rules(), slog.New(slog.NewTextHandler(&logs, nil))).(*forwarder)
		now := time.Unix(1000, 0)
		f.refused.now = func() time.Time { return now }
		req := new(dns.Msg).SetQuestion("a.example.", dns.TypeA)
		for range 4 {
			_, _ = f.admit(context.Background(), loopback(), req)
		}
		now = now.Add(refusedLookupLogInterval)
		_, _ = f.admit(context.Background(), loopback(), req)
		require.Equal(t, 2, strings.Count(logs.String(), "egress lookup refused"), logs.String())
		require.Contains(t, logs.String(), "more=3")
	})
}

// The refusal log holds one window per source: a line at its start, and at the next one a count of what it held back.
func TestRefusalLogOneLinePerSourcePerInterval(t *testing.T) {
	t.Parallel()
	now := time.Unix(1000, 0)
	l := refusalLog{now: func() time.Time { return now }, open: map[netip.Addr]*refusalWindow{}}
	a, b := netip.MustParseAddr("10.63.0.5"), netip.MustParseAddr("10.63.0.6")

	more, ok := l.note(a)
	require.True(t, ok, "a source's first refusal writes a line")
	require.Zero(t, more)
	for range 3 {
		_, ok = l.note(a)
		require.False(t, ok, "within the interval it writes none")
	}
	_, ok = l.note(b)
	require.True(t, ok, "another source has its own allowance")

	now = now.Add(refusedLookupLogInterval)
	more, ok = l.note(a)
	require.True(t, ok, "the next interval writes a line")
	require.Equal(t, 3, more, "with how many were held back")

	require.NotContains(t, l.open, b, "a window that ended with nothing held back is forgotten")
	require.Contains(t, l.open, a)
}

// scenario: forwarder-waits-for-the-bridge — an address not on the host yet is retried until the stop, with one warning; a
// taken port ends Serve.
func TestScenarioForwarderWaitsForTheBridge(t *testing.T) {
	t.Parallel()
	t.Run("the address does not exist yet", func(t *testing.T) {
		t.Parallel()
		var logs syncBuffer
		fwd := NewForwarder(netip.MustParseAddrPort("192.0.2.1:15053"), noUpstream(), nil, nil, slog.New(slog.NewTextHandler(&logs, nil)))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- fwd.Serve(ctx) }()

		select {
		case err := <-done:
			require.Fail(t, "Serve returned while the address did not exist", "%v", err)
		case <-time.After(bindRetry + bindRetry/4):
		}
		require.Equal(t, 1, strings.Count(logs.String(), "cannot listen yet"), logs.String())

		stop := time.Now()
		cancel()
		require.ErrorIs(t, <-done, context.Canceled)
		require.Less(t, time.Since(stop), bindRetry/2, "a stop during the wait returns at once")
	})
	t.Run("the port is taken", func(t *testing.T) {
		t.Parallel()
		pc, ln := bindUDPTCP(t)
		defer func() { _ = pc.Close(); _ = ln.Close() }()
		listen := netip.MustParseAddrPort(ln.Addr().String())

		ctx, cancel := context.WithTimeout(context.Background(), 3*bindRetry)
		defer cancel()
		err := NewForwarder(listen, noUpstream(), nil, nil, nil).Serve(ctx)
		require.ErrorIs(t, err, syscall.EADDRINUSE)
	})
}

// testIPs returns n distinct documentation-range IPv4 addresses.
func testIPs(n int) []net.IP {
	ips := make([]net.IP, n)
	for i := range ips {
		ips[i] = net.IPv4(203, 0, 113, byte(i+1))
	}
	return ips
}

// Issue 137: nftables redirects worker TCP :53 into the forwarder (dnsForwarderProtos), so a stub's
// RFC 7766 TCP retry of a truncated UDP answer must be served, and the full answer attested.
func TestIssue137_ForwarderServesDNSOverTCP(t *testing.T) {
	const name = "big.example.com."
	ips := testIPs(40)
	fwd, listen := startForwarder(t, name, ips)

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

// Issue 367: miekg/dns unpacks the upstream reply with Compress false, so relaying it as is re-packs it
// uncompressed. 25 A records fit 512 bytes compressed (433) but not uncompressed (808), so a client
// without EDNS0 must still get the whole answer over UDP.
func TestIssue367_ForwarderFitsReplyToClientUDPSize(t *testing.T) {
	const name = "big.example.com."
	ips := testIPs(25)
	_, listen := startForwarder(t, name, ips)

	resp := exchangeUntilServed(t, "udp", listen, new(dns.Msg).SetQuestion(name, dns.TypeA))
	require.False(t, resp.Truncated, "the answer fits 512 bytes once compressed")
	require.Len(t, resp.Answer, len(ips))
}

// serveGoroutineAlive reports whether any goroutine is still running inside (or was started by) serve, which Serve runs.
func serveGoroutineAlive() bool {
	buf := make([]byte, 1<<20)
	return bytes.Contains(buf[:runtime.Stack(buf, true)], []byte("egress.(*forwarder).serve"))
}

// Issue 368: a Serve cancelled before its servers bind must still shut them down. GOMAXPROCS(1) holds the
// server goroutines until Serve yields, so a Serve that does not wait for them returns before they bind.
func TestIssue368_ServeCancelledEarlyReleasesListeners(t *testing.T) {
	pc, ln := bindUDPTCP(t)
	listen := netip.MustParseAddrPort(ln.Addr().String())
	require.NoError(t, pc.Close())
	require.NoError(t, ln.Close())

	fwd := NewForwarder(listen, noUpstream(), nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	prev := runtime.GOMAXPROCS(1)
	err := fwd.Serve(ctx)
	runtime.GOMAXPROCS(prev)
	require.ErrorIs(t, err, context.Canceled)

	require.Eventually(t, func() bool { return !serveGoroutineAlive() }, 2*time.Second, 10*time.Millisecond,
		"a DNS server goroutine outlived Serve")
	pc, err = net.ListenPacket("udp", listen.String())
	require.NoError(t, err, "the UDP port is released when Serve returns")
	ln, err = net.Listen("tcp", listen.String())
	require.NoError(t, err, "the TCP port is released when Serve returns")
	require.NoError(t, pc.Close())
	require.NoError(t, ln.Close())
}

// Issue 455: Serve owns its DNS servers through a local slice, so the forwarder keeps no server state (and
// no mutex guarding it) that would suggest another path, such as a shutdown, reaches them.
func TestIssue455_ForwarderKeepsNoServerState(t *testing.T) {
	typ := reflect.TypeFor[forwarder]()
	fields := make([]string, 0, typ.NumField())
	for f := range typ.Fields() {
		fields = append(fields, f.Name)
	}
	require.Equal(t, []string{"listen", "upstream", "workers", "rules", "logger", "refused", "corr"}, fields)
}
