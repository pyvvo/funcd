package egress

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	cedarauth "github.com/pyvvo/funcd/internal/auth/cedar"
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

// fnMeta holds Function etl of namespace acme for the real PDP.
type fnMeta struct{}

func (fnMeta) Get(_ context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	if gvk.Kind == v1.KindFunction && ns == "acme" && name == "etl" {
		return &v1.Function{ObjectMeta: v1.ObjectMeta{Name: "etl", Namespace: "acme", ResourceGroup: "rg1"}}, nil
	}
	return nil, fault.NotFoundf("fnMeta.Get", "%s %s/%s not found", gvk.Kind, ns, name)
}

// compiledPolicies is a PolicySource over one compiled EgressPolicy.
type compiledPolicies []v1.Policy

func (c compiledPolicies) Policies(context.Context) ([]v1.Policy, string, error) { return c, "1", nil }

// realPDP is the real compiler and Cedar PDP over ep.
func realPDP(t *testing.T, ep *v1.EgressPolicy) auth.Authorizer {
	t.Helper()
	compiled, err := cedarauth.CompileEgressPolicy("acme", ep, nil)
	require.NoError(t, err)
	ents, err := cedarauth.NewEntityProvider(fnMeta{})
	require.NoError(t, err)
	pdp, err := cedarauth.New(cedarauth.Deps{Entities: ents, Policies: compiledPolicies(compiled)})
	require.NoError(t, err)
	return pdp
}

// egressRig is a Function's worker under one EgressPolicy rule, with the real compiler and PDP.
type egressRig struct {
	source netip.Addr
	deps   Deps
}

func newEgressRig(t *testing.T, rule v1.EgressRule) egressRig {
	t.Helper()
	source := netip.MustParseAddr("10.63.0.5")
	idx := NewMemoryWorkerIndex()
	idx.Add(source, auth.EntityRef{Type: v1.KindFunction, Namespace: "acme", Name: "etl"})
	pdp := realPDP(t, &v1.EgressPolicy{ObjectMeta: v1.ObjectMeta{Name: "egress", Namespace: "acme", ResourceGroup: "rg1"}, Spec: v1.EgressPolicySpec{Rules: []v1.EgressRule{rule}}})
	return egressRig{source: source, deps: Deps{Workers: idx, Authz: pdp}}
}

// newDomainRig holds the domain rules on 443 the domain scenarios share.
func newDomainRig(t *testing.T) egressRig {
	t.Helper()
	return newEgressRig(t, v1.EgressRule{To: v1.EgressTo{Domains: []string{"api.x.example", "*.y.example"}}, Ports: []int{443}})
}

// decideDomain decides a connection to addr whose lookup answered with that address, on a node holding node's addresses.
func (r egressRig) decideDomain(t *testing.T, addr netip.Addr, port uint16, node func() ([]netip.Addr, error)) (auth.Decision, AuditRecord) {
	t.Helper()
	d := r.deps
	d.NodeAddrs = node
	d.DNS = fakeDNS{addr: {"a.y.example", "api.x.example", "*.y.example"}}
	dec, rec, err := decideConnect(context.Background(), d, r.source, netip.AddrPortFrom(addr, port), "api.x.example")
	require.NoError(t, err)
	return dec, rec
}

// scenario: domain-rule-public-only — a domain rule reaches public addresses only: at any other address the destination
// carries no domains, so the rule does not fire, whatever the lookup answered.
func TestScenarioDomainRulePublicOnly(t *testing.T) {
	t.Parallel()
	rig := newDomainRig(t)
	node := func() ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("203.0.114.9"), netip.MustParseAddr("2a00:1450:1::9")}, nil
	}

	for _, ip := range []string{
		"10.0.0.5", "172.16.0.5", "192.168.1.5", // private
		"169.254.169.254",               // link-local
		"127.0.0.1",                     // loopback
		"0.0.0.0",                       // unspecified
		"100.64.0.5", "100.127.255.254", // shared
		"224.0.0.1", "239.255.255.250", // multicast
		"255.255.255.255",    // broadcast
		"203.0.114.9",        // on the node
		"fd00::5", "fc00::5", // private
		"fe80::5",        // link-local
		"::1",            // loopback
		"::",             // unspecified
		"ff02::1",        // multicast
		"2a00:1450:1::9", // on the node
	} {
		t.Run(ip, func(t *testing.T) {
			t.Parallel()
			dec, rec := rig.decideDomain(t, netip.MustParseAddr(ip), 443, node)
			require.False(t, dec.Allowed, "%s takes a cidrs rule that names it", ip)
			require.False(t, rec.Allowed)
			require.Empty(t, rec.Domain, "the audit record names no domain there")
		})
	}

	for _, ip := range []string{"::ffff:10.0.0.5", "::ffff:0.0.0.0", "::ffff:100.64.0.5", "::ffff:203.0.114.9"} {
		restricted, err := Deps{NodeAddrs: node}.restricted(netip.MustParseAddr(ip))
		require.NoError(t, err)
		require.True(t, restricted, "%s reads as its IPv4 address", ip)
	}

	for _, ip := range []string{"93.184.216.34", "100.128.0.1", "100.63.255.254", "172.32.0.1", "2606:2800:220:1::1"} {
		t.Run(ip, func(t *testing.T) {
			t.Parallel()
			addr := netip.MustParseAddr(ip)
			dec, rec := rig.decideDomain(t, addr, 443, node)
			require.True(t, dec.Allowed, "a public address behaves as before")
			require.Equal(t, "api.x.example", rec.Domain)

			dec, _ = rig.decideDomain(t, addr, 80, node)
			require.False(t, dec.Allowed, "the rule's port holds")
		})
	}

	t.Run("the host's own addresses are read when no reader is given", func(t *testing.T) {
		t.Parallel()
		addrs, err := interfaceAddrs()
		require.NoError(t, err)
		require.Contains(t, addrs, netip.MustParseAddr("127.0.0.1"))
		restricted, err := Deps{}.restricted(netip.MustParseAddr("93.184.216.34"))
		require.NoError(t, err)
		require.False(t, restricted)
	})

	t.Run("a failed read of the node's addresses permits nothing", func(t *testing.T) {
		t.Parallel()
		d := rig.deps
		d.DNS = fakeDNS{netip.MustParseAddr("93.184.216.34"): {"api.x.example"}}
		d.NodeAddrs = func() ([]netip.Addr, error) { return nil, errors.New("netlink down") }
		dec, _, err := decideConnect(context.Background(), d, rig.source, netip.MustParseAddrPort("93.184.216.34:443"), "")
		require.Error(t, err)
		require.False(t, dec.Allowed)
	})
}

// scenario: cidr-rule-names-the-address — a rule that lists the name and the address reaches it, on its ports. A connection
// with no attested name needs no read of the node's addresses.
func TestScenarioCIDRRuleNamesTheAddress(t *testing.T) {
	t.Parallel()
	rig := newEgressRig(t, v1.EgressRule{To: v1.EgressTo{Domains: []string{"db.internal.example"}, CIDRs: []string{"10.0.0.5/32", "203.0.114.9/32"}}, Ports: []int{5432}})
	d := rig.deps
	d.NodeAddrs = func() ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("203.0.114.9")}, nil
	}
	attested := func(addr string) fakeDNS { return fakeDNS{netip.MustParseAddr(addr): {"db.internal.example"}} }
	for _, tc := range []struct {
		dst     string
		allowed bool
	}{
		{"10.0.0.5:5432", true},
		{"10.0.0.5:5433", false},
		{"10.0.0.6:5432", false},
		{"203.0.114.9:5432", true},
	} {
		d := d
		d.DNS = attested(netip.MustParseAddrPort(tc.dst).Addr().String())
		dec, _, err := decideConnect(context.Background(), d, rig.source, netip.MustParseAddrPort(tc.dst), "")
		require.NoError(t, err)
		require.Equal(t, tc.allowed, dec.Allowed, tc.dst)
	}

	d.DNS = fakeDNS{}
	d.NodeAddrs = func() ([]netip.Addr, error) { return nil, errors.New("not read") }
	dec, _, err := decideConnect(context.Background(), d, rig.source, netip.MustParseAddrPort("10.0.0.5:5432"), "")
	require.NoError(t, err)
	require.True(t, dec.Allowed, "by address, with no name")
}

// adr0317Table is ADR-0317 Decision 1's table.
func adr0317Table() []string {
	return []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24",
		"192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"::/128", "::1/128", "::ffff:0:0/96", "64:ff9b:1::/48", "100::/64", "100:0:0:1::/64", "2001::/23", "2001:db8::/32",
		"3fff::/20", "5f00::/16", "fc00::/7", "fe80::/10",
		"192.88.99.0/24", "2002::/16", "64:ff9b::/96",
		"224.0.0.0/4", "ff00::/8", "fec0::/10", "168.63.129.16/32",
	}
}

// offTheNode is a node that holds none of the addresses a test names.
func offTheNode() ([]netip.Addr, error) { return nil, nil }

// lastAddr is the last address of p.
func lastAddr(p netip.Prefix) netip.Addr {
	b := p.Addr().AsSlice()
	for i := p.Bits(); i < len(b)*8; i++ {
		b[i/8] |= 1 << (7 - i%8)
	}
	last, _ := netip.AddrFromSlice(b)
	return last
}

// scenario: domain-rule-globally-reachable-only — a domain rule permits nothing in a prefix of the table, from its first
// address to its last, whatever the lookup answered; it reaches what lies just outside one, on its port.
func TestScenarioDomainRuleGloballyReachableOnly(t *testing.T) {
	t.Parallel()
	rig := newDomainRig(t)

	var refused []netip.Addr
	for _, s := range adr0317Table() {
		p := netip.MustParsePrefix(s)
		if p.Addr().Is4In6() {
			continue // no address reaches this row: the mapping comes off first, and the PDP takes no mapped form
		}
		refused = append(refused, p.Addr(), lastAddr(p))
	}
	// The rows the registries mark globally reachable inside an entry count as the entry does.
	for _, ip := range []string{"192.0.0.9", "192.0.0.10", "2001:1::1", "2001:1::2", "2001:1::3", "2001:3::1", "2001:4:112::1", "2001:20::1", "2001:30::1"} {
		refused = append(refused, netip.MustParseAddr(ip))
	}
	for _, addr := range refused {
		t.Run(addr.String(), func(t *testing.T) {
			t.Parallel()
			dec, rec := rig.decideDomain(t, addr, 443, offTheNode)
			require.False(t, dec.Allowed, "%s takes a cidrs rule that names it", addr)
			require.False(t, rec.Allowed)
			require.Empty(t, rec.Domain, "the audit record names no domain there")
		})
	}

	for _, ip := range []string{
		"1.0.0.0", "9.255.255.255", "11.0.0.0", "100.63.255.255", "100.128.0.0", "126.255.255.255", "128.0.0.0",
		"168.63.129.15", "168.63.129.17", "169.253.255.255", "169.255.0.0", "172.15.255.255", "172.32.0.0", "192.0.1.255",
		"192.0.3.0", "192.88.98.255", "192.88.100.0", "192.167.255.255", "192.169.0.0", "198.17.255.255", "198.20.0.0",
		"198.51.99.255", "198.51.101.0", "203.0.112.255", "203.0.114.0", "223.255.255.255",
		"::2", "64:ff9a::", "64:ff9b:0:0:0:1::", "64:ff9b:2::", "100:0:0:2::", "2000:ffff:ffff:ffff:ffff:ffff:ffff:ffff",
		"2001:200::", "2001:db9::", "2003::", "3ffe:ffff::", "3fff:1000::", "5eff:ffff::", "5f01::", "fbff:ffff::", "fe00::",
	} {
		t.Run("outside "+ip, func(t *testing.T) {
			t.Parallel()
			dec, rec := rig.decideDomain(t, netip.MustParseAddr(ip), 443, offTheNode)
			require.True(t, dec.Allowed, "%s lies outside the table", ip)
			require.Equal(t, "api.x.example", rec.Domain)
			dec, _ = rig.decideDomain(t, netip.MustParseAddr(ip), 80, offTheNode)
			require.False(t, dec.Allowed, "the rule's port holds")
		})
	}

	t.Run("an address reads with its mapping and zone removed, and an invalid one counts as restricted", func(t *testing.T) {
		t.Parallel()
		for ip, want := range map[netip.Addr]bool{
			netip.MustParseAddr("::ffff:198.18.0.5"):    true,
			netip.MustParseAddr("::ffff:93.184.216.34"): false,
			netip.MustParseAddr("fe80::1%eth0"):         true,
			{}:                                          true,
		} {
			restricted, err := Deps{NodeAddrs: offTheNode}.restricted(ip)
			require.NoError(t, err)
			require.Equal(t, want, restricted, "%s", ip)
		}
	})
}

// scenario: globally-reachable-block-allowed — an entry the registries mark globally reachable, which no prefix of the
// table holds, stays in a domain rule's reach.
func TestScenarioGloballyReachableBlockAllowed(t *testing.T) {
	t.Parallel()
	rig := newDomainRig(t)
	for _, ip := range []string{"192.31.196.1", "192.52.193.1", "192.175.48.1", "2620:4f:8000::1"} {
		dec, rec := rig.decideDomain(t, netip.MustParseAddr(ip), 443, offTheNode)
		require.True(t, dec.Allowed, ip)
		require.Equal(t, "api.x.example", rec.Domain, ip)
	}
}

// scenario: cidr-rule-names-a-table-address — a rule that names an address of the table reaches it, on its ports, by address
// or with a name attested; the name alone does not.
func TestScenarioCIDRRuleNamesATableAddress(t *testing.T) {
	t.Parallel()
	rig := newEgressRig(t, v1.EgressRule{To: v1.EgressTo{Domains: []string{"lab.example"}, CIDRs: []string{"198.18.0.5/32", "168.63.129.16/32"}}, Ports: []int{443}})
	for _, tc := range []struct {
		dst      string
		attested bool
		allowed  bool
	}{
		{"198.18.0.5:443", false, true},
		{"198.18.0.5:443", true, true},
		{"168.63.129.16:443", true, true},
		{"198.18.0.5:80", true, false},
		{"198.18.0.6:443", true, false},
	} {
		dst := netip.MustParseAddrPort(tc.dst)
		d := rig.deps
		d.NodeAddrs = offTheNode
		d.DNS = fakeDNS{}
		if tc.attested {
			d.DNS = fakeDNS{dst.Addr(): {"lab.example"}}
		}
		dec, _, err := decideConnect(context.Background(), d, rig.source, dst, "")
		require.NoError(t, err)
		require.Equal(t, tc.allowed, dec.Allowed, "%s attested=%v", tc.dst, tc.attested)
	}
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

// notGlobal holds exactly ADR-0317 Decision 1's table, each prefix masked.
func TestNotGlobalIsTheDecidedTable(t *testing.T) {
	t.Parallel()
	var want []netip.Prefix
	for _, s := range adr0317Table() {
		p := netip.MustParsePrefix(s)
		require.Equal(t, p.Masked(), p, s)
		want = append(want, p)
	}
	require.ElementsMatch(t, want, notGlobal)
}
