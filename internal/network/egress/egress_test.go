package egress

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
)

// captureClientHello drives a real crypto/tls client over a pipe and returns its first flight (a genuine
// ClientHello record) so the SNI parser is tested against real bytes.
func captureClientHello(t *testing.T, serverName string) []byte {
	t.Helper()
	c1, c2 := net.Pipe()
	got := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 8192)
		n, _ := c2.Read(buf)
		got <- append([]byte(nil), buf[:n]...)
		_ = c2.Close()
	}()
	client := tls.Client(c1, &tls.Config{ServerName: serverName, InsecureSkipVerify: true}) //nolint:gosec // test-only handshake capture
	go func() { _ = client.Handshake() }()
	select {
	case b := <-got:
		_ = c1.Close()
		return b
	case <-time.After(5 * time.Second):
		_ = c1.Close()
		t.Fatal("timed out capturing ClientHello")
		return nil
	}
}

// scenario: sni-parse — the SNI is extracted from a real TLS ClientHello; non-TLS bytes yield ("", false).
func TestScenarioSNIParse(t *testing.T) {
	t.Parallel()
	hello := captureClientHello(t, "api.example.com")
	name, ok := parseClientHelloSNI(hello)
	require.True(t, ok, "SNI is parsed from a real ClientHello")
	require.Equal(t, "api.example.com", name)

	_, ok = parseClientHelloSNI([]byte("not a tls record"))
	require.False(t, ok, "non-TLS bytes have no SNI")
	_, ok = parseClientHelloSNI(nil)
	require.False(t, ok)
}

// scenario: http-host-parse — the Host header is extracted from an HTTP/1.x preamble (port stripped).
func TestScenarioHTTPHostParse(t *testing.T) {
	t.Parallel()
	req := []byte("GET /x HTTP/1.1\r\nHost: db.internal.example.com:8080\r\nUser-Agent: probe\r\n\r\n")
	host, ok := parseHTTPHost(req)
	require.True(t, ok)
	require.Equal(t, "db.internal.example.com", host)

	_, ok = parseHTTPHost([]byte("GET / HTTP/1.1\r\n\r\n"))
	require.False(t, ok, "no Host header ⇒ no host")
}

// scenario: domainsfor-correlation-and-wildcard — DomainsFor returns only the domains THIS worker
// resolved to dst (per-worker keying), expires on TTL, and injects a matched namespace wildcard token.
func TestScenarioDomainsForCorrelationAndWildcard(t *testing.T) {
	t.Parallel()
	now := time.Unix(1000, 0)
	clock := func() time.Time { return now }
	wild := func(netip.Addr) []string { return []string{"*.x.com"} }
	c := newCorrelator(clock, wild)

	worker := netip.MustParseAddr("10.63.0.5")
	other := netip.MustParseAddr("10.63.0.6")
	dst := netip.MustParseAddr("93.184.216.34")

	c.record(worker, "api.x.com", []netip.Addr{dst}, 30*time.Second)

	got := c.DomainsFor(worker, dst)
	require.Equal(t, []string{"*.x.com", "api.x.com"}, got, "the FQDN + its matched wildcard token, sorted")

	require.Empty(t, c.DomainsFor(other, dst), "another worker never resolved it ⇒ no attestation (per-worker keying)")

	// TTL expiry: advance past the record's expiry.
	now = time.Unix(1031, 0)
	require.Empty(t, c.DomainsFor(worker, dst), "an expired record is not attested")
}

// scenario: forwarder-attested-netdestination-build (B1) — buildDestination trusts only the
// forwarder-attested set; a spoofed SNI to an unattested IP contributes NO domain (so the compiled
// policy denies), while an attested name is confirmed.
func TestScenarioForwarderAttestedBuild(t *testing.T) {
	t.Parallel()
	dst := netip.MustParseAddrPort("93.184.216.34:443")

	nd, confirmed := buildDestination(dst, []string{"api.x.com"}, "api.x.com")
	require.Equal(t, []string{"api.x.com"}, nd.Domains)
	require.Equal(t, "api.x.com", confirmed, "an attested asserted name is confirmed")

	spoofDst := netip.MustParseAddrPort("8.8.8.8:443")
	nd, confirmed = buildDestination(spoofDst, nil, "api.x.com")
	require.Empty(t, nd.Domains, "a spoofed SNI to an unattested IP contributes no domain")
	require.Equal(t, "", confirmed, "an unattested asserted name is dropped")
}

// fakeDNS is a static DomainResolver.
type fakeDNS map[netip.Addr][]string

func (f fakeDNS) DomainsFor(_ netip.Addr, dst netip.Addr) []string { return f[dst] }

// fakeAudit records the last audit record.
type fakeAudit struct{ last *AuditRecord }

func (a *fakeAudit) Egress(_ context.Context, rec AuditRecord) { r := rec; a.last = &r }

// fakeAuthz mirrors the compiled EgressPolicy at the decision level for the unit test: it ALLOWS an
// egress::connect whose NetDestination carries the attested domain "api.x.com" (else default-deny).
type fakeAuthz struct{}

func (fakeAuthz) Authorize(_ context.Context, req auth.Request) (auth.Decision, error) {
	if req.Action != auth.ActionEgressConnect || req.Resource == nil {
		return auth.Decision{Allowed: false, Reason: "not an egress request"}, nil
	}
	nd, err := auth.ParseNetDestPath(req.Resource.Path)
	if err != nil {
		return auth.Decision{}, err
	}
	for _, d := range nd.Domains {
		if d == "api.x.com" {
			return auth.Decision{Allowed: true, Reason: "permitted"}, nil
		}
	}
	return auth.Decision{Allowed: false, Reason: "default-deny"}, nil
}

// scenario: decide-flow-attested-vs-spoof — a known worker to an attested destination is allowed+audited;
// the SAME asserted SNI to an unattested IP (sni-spoof) is denied+audited; an unknown source is denied.
func TestScenarioDecideFlow(t *testing.T) {
	t.Parallel()
	worker := netip.MustParseAddr("10.63.0.5")
	attestedIP := netip.MustParseAddr("93.184.216.34")
	spoofIP := netip.MustParseAddr("8.8.8.8")

	idx := NewMemoryWorkerIndex()
	idx.Add(worker, auth.EntityRef{Type: v1.KindFunction, Namespace: "acme", Name: "etl"})

	audit := &fakeAudit{}
	d := Deps{
		Workers: idx,
		DNS:     fakeDNS{attestedIP: {"api.x.com"}}, // spoofIP is NOT attested
		Authz:   fakeAuthz{},
		Audit:   audit,
	}
	ctx := context.Background()

	// attested → allowed.
	dec, rec, err := decideConnect(ctx, d, worker, netip.AddrPortFrom(attestedIP, 443), "api.x.com")
	require.NoError(t, err)
	require.True(t, dec.Allowed, "an attested declared domain is allowed")
	require.Equal(t, "acme", rec.Namespace)
	require.Equal(t, "api.x.com", rec.Domain)

	// spoof: same asserted SNI, unattested IP → denied.
	dec, rec, err = decideConnect(ctx, d, worker, netip.AddrPortFrom(spoofIP, 443), "api.x.com")
	require.NoError(t, err)
	require.False(t, dec.Allowed, "a spoofed SNI to an unattested IP is denied (B1)")
	require.False(t, rec.Allowed)
	require.Equal(t, "", rec.Domain, "no attested domain on the audit record")

	// unknown source → denied.
	dec, _, err = decideConnect(ctx, d, netip.MustParseAddr("10.63.0.99"), netip.AddrPortFrom(attestedIP, 443), "")
	require.NoError(t, err)
	require.False(t, dec.Allowed, "an unknown source worker is denied+audited")
}

// scenario: disabled-passthrough — New(false) is the no-op Gateway: Serve returns on ctx cancel and
// nothing external is enforced (egress open as today). On any non-Linux host New(true) is also a no-op.
func TestScenarioDisabledPassthrough(t *testing.T) {
	t.Parallel()
	g := New(false, Deps{})
	require.IsType(t, noopGateway{}, g, "disabled ⇒ the no-op Gateway")
	require.NoError(t, g.Close())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, g.Serve(ctx), context.Canceled)
}

// The correlator lives as long as the daemon, so expired records must be freed, not only hidden: both a
// stream of distinct resolved IPs and many names resolving to one IP stay bounded by the live set.
func TestIssue138_CorrelatorEvictsExpiredRecords(t *testing.T) {
	t.Parallel()
	worker := netip.MustParseAddr("10.63.0.5")

	t.Run("distinct-ips", func(t *testing.T) {
		t.Parallel()
		now := time.Unix(1000, 0)
		c := newCorrelator(func() time.Time { return now }, nil)
		pinned := netip.MustParseAddr("198.51.100.7")
		c.record(worker, "api.example.com", []netip.Addr{pinned}, time.Hour)
		const perRound = 2000
		var last netip.Addr
		for round := range 5 {
			if round > 0 {
				now = now.Add(31 * time.Second)
			}
			for i := range perRound {
				last = netip.AddrFrom4([4]byte{100, byte(round), byte(i >> 8), byte(i)})
				c.record(worker, "svc.example.com", []netip.Addr{last}, 30*time.Second)
			}
		}
		require.LessOrEqual(t, len(c.entries), 2*perRound, "expired keys from past rounds are evicted")
		require.Equal(t, []string{"api.example.com"}, c.DomainsFor(worker, pinned), "a record live through the sweeps survives them")
		require.Equal(t, []string{"svc.example.com"}, c.DomainsFor(worker, last), "the newest record is attested")
	})

	t.Run("one-ip-many-names", func(t *testing.T) {
		t.Parallel()
		now := time.Unix(1000, 0)
		c := newCorrelator(func() time.Time { return now }, nil)
		dst := netip.MustParseAddr("93.184.216.34")
		c.record(worker, "pinned.example.com", []netip.Addr{dst}, 24*time.Hour)
		for i := range 50000 {
			now = now.Add(time.Second)
			c.record(worker, fmt.Sprintf("n%d.example.com", i), []netip.Addr{dst}, time.Second)
		}
		require.LessOrEqual(t, len(c.entries[corrKey{src: worker, dst: dst}]), 2000, "expired domains under one key are evicted")
		require.Equal(t, []string{"n49999.example.com", "pinned.example.com"}, c.DomainsFor(worker, dst), "only the live names are attested")
	})
}

// A worker controls which names it resolves and, through a zone it owns, their TTLs, so the live pairs one
// source can pin in the daemon must be capped: the cap sheds that worker's earliest-expiring pair, never its
// newest record and never another worker's.
func TestIssue369_CorrelatorCapsLivePairsPerSource(t *testing.T) {
	t.Parallel()
	now := time.Unix(1000, 0)
	c := newCorrelator(func() time.Time { return now }, nil)
	flooder := netip.MustParseAddr("10.63.0.5")
	other := netip.MustParseAddr("10.63.0.6")
	pinned := netip.MustParseAddr("198.51.100.7")
	c.record(flooder, "pinned.example.com", []netip.Addr{pinned}, 48*time.Hour)
	c.record(other, "api.example.com", []netip.Addr{pinned}, time.Hour)

	var last netip.Addr
	for i := range 20000 {
		last = netip.AddrFrom4([4]byte{100, 64, byte(i >> 8), byte(i)})
		c.record(flooder, fmt.Sprintf("r%d.wild.example", i), []netip.Addr{last}, 24*time.Hour)
	}

	held, total := 0, 0
	for k, m := range c.entries {
		total += len(m)
		if k.src == flooder {
			held += len(m)
		}
	}
	require.LessOrEqual(t, held, 1024, "one worker's live pairs are capped, however long their TTLs")
	require.Equal(t, total, c.records, "the pair count matches the map")
	require.Equal(t, []string{"r19999.wild.example"}, c.DomainsFor(flooder, last), "the newest record is attested")
	require.Equal(t, []string{"pinned.example.com"}, c.DomainsFor(flooder, pinned), "the cap sheds the earliest-expiring pair first")
	require.Equal(t, []string{"api.example.com"}, c.DomainsFor(other, pinned), "a flood never displaces another worker's records")
}
