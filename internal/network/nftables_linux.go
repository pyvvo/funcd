//go:build linux

package network

import (
	"context"
	"encoding/binary"
	"log/slog"
	"net/netip"
	"strings"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"

	"github.com/pyvvo/funcd/api/fault"
)

// Table names funcd owns. Programmed additively over the CNI bridge (ADR-0056 never-clobber); Remove
// deletes exactly these, leaving the operator's/CNI's own tables untouched.
const (
	lateralTableName = "funcd_lateral" // bridge family — L2 inter-worker deny
	egressTableName  = "funcd_egress"  // inet family   — L3 accept/redirect/default-deny
)

func newDriver(*slog.Logger) Manager { return &nftDriver{} }

// nftDriver programs the F80 isolation substrate via google/nftables (pure-Go netlink — no nft binary,
// no cgo). It assembles two objects (see planTables) and applies them in one atomic Flush.
type nftDriver struct{}

// chainPlan is the correct-by-construction structure of a chain funcd programs — asserted directly by the
// Linux unit test (ruleset-programmed) and used by program() to scaffold the real nftables objects. The
// expr-level rules are attached in program(); their runtime effect is verified by the Lima e2e lane.
type chainPlan struct {
	name    string
	hook    *nftables.ChainHook
	prio    *nftables.ChainPriority
	typ     nftables.ChainType
	dropPol bool // true ⇒ chain policy is drop (default-deny); false ⇒ accept
}

type tablePlan struct {
	name   string
	family nftables.TableFamily
	chains []chainPlan
}

// planTables is the pure plan F80 programs for a Policy: a bridge-family lateral-deny and an inet
// egress-policy table whose forward-filter chain defaults to drop (accepts precede it — see program).
func planTables(_ Policy) []tablePlan {
	return []tablePlan{
		{
			name:   lateralTableName,
			family: nftables.TableFamilyBridge,
			chains: []chainPlan{{
				name: "forward", hook: nftables.ChainHookForward, prio: nftables.ChainPriorityFilter,
				typ: nftables.ChainTypeFilter, dropPol: false, // per-frame drop is an explicit rule (inter-worker only)
			}},
		},
		{
			name:   egressTableName,
			family: nftables.TableFamilyINet,
			chains: []chainPlan{
				{name: "prerouting", hook: nftables.ChainHookPrerouting, prio: nftables.ChainPriorityNATDest, typ: nftables.ChainTypeNAT, dropPol: false},
				{name: "forward", hook: nftables.ChainHookForward, prio: nftables.ChainPriorityFilter, typ: nftables.ChainTypeFilter, dropPol: true},
				{name: "input", hook: nftables.ChainHookInput, prio: nftables.ChainPriorityFilter, typ: nftables.ChainTypeFilter, dropPol: false},
			},
		},
	}
}

func (d *nftDriver) Apply(ctx context.Context, p Policy) error {
	const op = "network.nftDriver.Apply"
	if err := p.Validate(); err != nil {
		return err
	}
	c, err := nftables.New()
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "open nftables netlink conn")
	}
	if err := delFuncdTables(c); err != nil { // idempotent reconcile: drop any prior funcd tables before re-adding
		return fault.Wrapf(err, fault.Internal, op, "reconcile prior funcd tables")
	}
	if err := program(c, p); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build funcd egress ruleset")
	}
	if err := c.Flush(); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "program funcd nftables ruleset")
	}
	return nil
}

func (d *nftDriver) Remove(ctx context.Context) error {
	const op = "network.nftDriver.Remove"
	c, err := nftables.New()
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "open nftables netlink conn")
	}
	if err := delFuncdTables(c); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "list funcd tables")
	}
	if err := c.Flush(); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "remove funcd nftables ruleset")
	}
	return nil
}

// delFuncdTables queues deletion of any pre-existing funcd tables — LISTING first so it deletes ONLY the
// tables that actually exist. A batched DelTable of a *nonexistent* table is NOT a no-op: the kernel
// returns ENOENT ("no such file or directory") and fails the whole atomic Flush — the first-run crash the
// egress e2e caught (funcd exits before serving because egress isolation is fail-closed-fatal). Listing
// first makes the reconcile genuinely idempotent on a clean host.
func delFuncdTables(c *nftables.Conn) error {
	tables, err := c.ListTables()
	if err != nil {
		return err
	}
	for _, t := range tables {
		switch {
		case t.Family == nftables.TableFamilyBridge && t.Name == lateralTableName:
			c.DelTable(t)
		case t.Family == nftables.TableFamilyINet && t.Name == egressTableName:
			c.DelTable(t)
		}
	}
	return nil
}

// chain keys (table/chain) the rule plan references.
const (
	lateralFwd = lateralTableName + "/forward"
	egressPre  = egressTableName + "/prerouting"
	egressFwd  = egressTableName + "/forward"
	egressIn   = egressTableName + "/input"
)

// ruleKind classifies a planned rule so both program() (which builds it) and the Linux unit test (which
// asserts the plan, including the redirect-exclusion ordering) share one source of truth.
type ruleKind int

const (
	kindLateralDrop    ruleKind = iota // bridge/forward: src AND dst in the worker subnet → drop (L2 lateral)
	kindRedirectSkip                   // inet/prerouting: src∈subnet, dst = internal/DNS → return (NOT redirected)
	kindRedirect                       // inet/prerouting: src∈subnet, TCP → redirect to GatewayPort
	kindForwardAccept                  // inet/forward: src∈subnet, dst = internal/DNS → accept (precedes the drop policy)
	kindInputAccept                    // inet/input: TCP dport = GatewayPort → accept the redirected packets
	kindDNSRedirect                    // inet/prerouting: src∈subnet, {UDP,TCP} dport 53 → redirect to DNSForwarderPort (F81)
	kindDNSInputAccept                 // inet/input: {UDP,TCP} dport = DNSForwarderPort → accept the redirected DNS (F81)
)

// rulePlan is one planned rule. dst/port/proto are set only for the dst-scoped kinds (skip/forward-accept).
type rulePlan struct {
	chain string
	kind  ruleKind
	dst   netip.Addr
	port  uint16
	proto byte
}

// planRules is the pure, ordered rule plan F80 programs — the correct-by-construction core the Linux unit
// test asserts. Ordering is load-bearing: in prerouting the internal-allow + DNS **return** (skip) rules
// MUST precede the redirect, or a NAT redirect (which rewrites dst before routing) would hijack internal
// and DNS-over-TCP traffic to the gateway. In forward the accepts precede the chain's drop policy.
func planRules(p Policy) []rulePlan {
	rs := []rulePlan{{chain: lateralFwd, kind: kindLateralDrop}}

	// prerouting: (F81) redirect worker :53 into the DNS forwarder FIRST, then skip (return, no DNAT)
	// internal-service TCP + (legacy) DNS-over-TCP, THEN redirect the rest. The general redirect MUST
	// stay LAST — a NAT redirect rewrites dst before routing, so it would hijack the earlier destinations.
	for _, proto := range dnsForwarderProtos(p) {
		rs = append(rs, rulePlan{chain: egressPre, kind: kindDNSRedirect, port: 53, proto: proto})
	}
	for _, ep := range p.InternalAllow {
		rs = append(rs, rulePlan{chain: egressPre, kind: kindRedirectSkip, dst: ep.Addr(), port: ep.Port(), proto: unix.IPPROTO_TCP})
	}
	if p.DNSForwarderPort == 0 {
		// legacy F80: DNS-over-TCP to the node resolver is skipped (returned), not redirected.
		rs = append(rs, rulePlan{chain: egressPre, kind: kindRedirectSkip, dst: p.DNSResolver.Addr(), port: p.DNSResolver.Port(), proto: unix.IPPROTO_TCP})
	}
	rs = append(rs, rulePlan{chain: egressPre, kind: kindRedirect})

	// forward (policy drop): accept the skipped internal-service TCP; and (legacy) DNS to the node
	// resolver (UDP+TCP). With the forwarder, worker :53 is DNAT'd to the LOCAL forwarder and never
	// forwarded, so no forward-accept for DNS is needed (or wanted — the forwarder is the only resolver).
	for _, ep := range p.InternalAllow {
		rs = append(rs, rulePlan{chain: egressFwd, kind: kindForwardAccept, dst: ep.Addr(), port: ep.Port(), proto: unix.IPPROTO_TCP})
	}
	if p.DNSForwarderPort == 0 {
		for _, proto := range []byte{unix.IPPROTO_UDP, unix.IPPROTO_TCP} {
			rs = append(rs, rulePlan{chain: egressFwd, kind: kindForwardAccept, dst: p.DNSResolver.Addr(), port: p.DNSResolver.Port(), proto: proto})
		}
	}

	// input: accept the redirected packets destined to the local gateway port; and (F81) the DNS now
	// redirected to the local forwarder port (UDP+TCP).
	rs = append(rs, rulePlan{chain: egressIn, kind: kindInputAccept})
	for _, proto := range dnsForwarderProtos(p) {
		rs = append(rs, rulePlan{chain: egressIn, kind: kindDNSInputAccept, port: p.DNSForwarderPort, proto: proto})
	}
	return rs
}

// dnsForwarderProtos returns the L4 protos whose worker :53 is redirected into the funcd DNS forwarder —
// UDP+TCP when a forwarder is configured (F81), else none (legacy F80 sends DNS straight to the resolver).
func dnsForwarderProtos(p Policy) []byte {
	if p.DNSForwarderPort == 0 {
		return nil
	}
	return []byte{unix.IPPROTO_UDP, unix.IPPROTO_TCP}
}

// program builds the two funcd tables (planTables) and attaches the F80 rules (planRules). IPv4 only in
// V1 (the funcd0 subnet is IPv4); IPv6 worker egress is a later refinement.
func program(c *nftables.Conn, p Policy) error {
	tables := map[string]*nftables.Table{}
	chains := map[string]*nftables.Chain{}
	for _, tp := range planTables(p) {
		t := c.AddTable(&nftables.Table{Family: tp.family, Name: tp.name})
		tables[tp.name] = t
		for _, ch := range tp.chains {
			nc := &nftables.Chain{Name: ch.name, Table: t, Type: ch.typ, Hooknum: ch.hook, Priority: ch.prio}
			if ch.dropPol {
				pol := nftables.ChainPolicyDrop
				nc.Policy = &pol
			}
			chains[tp.name+"/"+ch.name] = c.AddChain(nc)
		}
	}

	sub := p.WorkerSubnet.Masked()
	netAddr := sub.Addr().As4()
	mask := net4Mask(sub.Bits())
	tableOf := func(chainKey string) *nftables.Table {
		if strings.HasPrefix(chainKey, lateralTableName+"/") {
			return tables[lateralTableName]
		}
		return tables[egressTableName]
	}
	for _, r := range planRules(p) {
		c.AddRule(&nftables.Rule{Table: tableOf(r.chain), Chain: chains[r.chain], Exprs: exprsFor(r, p, netAddr, mask)})
	}
	return nil
}

// exprsFor builds the nftables expressions for one planned rule.
func exprsFor(r rulePlan, p Policy, netAddr, mask [4]byte) []expr.Any {
	src := matchIPv4Field(srcOffset, netAddr, mask)
	switch r.kind {
	case kindLateralDrop:
		return concat(src, matchIPv4Field(dstOffset, netAddr, mask), verdict(expr.VerdictDrop))
	case kindRedirectSkip:
		return concat(src, matchDstAddr(r.dst.As4()), matchL4Proto(r.proto), matchDport(r.port), verdict(expr.VerdictReturn))
	case kindRedirect:
		return concat(src, matchL4Proto(unix.IPPROTO_TCP), redirectTo(p.GatewayPort))
	case kindForwardAccept:
		return concat(src, matchDstAddr(r.dst.As4()), matchL4Proto(r.proto), matchDport(r.port), verdict(expr.VerdictAccept))
	case kindInputAccept:
		return concat(matchL4Proto(unix.IPPROTO_TCP), matchDport(p.GatewayPort), verdict(expr.VerdictAccept))
	case kindDNSRedirect:
		return concat(src, matchL4Proto(r.proto), matchDport(r.port), redirectTo(p.DNSForwarderPort))
	case kindDNSInputAccept:
		return concat(matchL4Proto(r.proto), matchDport(r.port), verdict(expr.VerdictAccept))
	}
	return nil
}

// --- expr helpers (IPv4) -------------------------------------------------------------------------------

const (
	srcOffset = 12 // IPv4 source address offset in the network header
	dstOffset = 16 // IPv4 destination address offset
)

// matchIPv4Field matches a masked IPv4 address field (src or dst) against a network address.
func matchIPv4Field(offset uint32, addr, mask [4]byte) []expr.Any {
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: 4},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: mask[:], Xor: []byte{0, 0, 0, 0}},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: maskedBytes(addr, mask)},
	}
}

func matchDstAddr(addr [4]byte) []expr.Any {
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: dstOffset, Len: 4},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: addr[:]},
	}
}

func matchL4Proto(proto byte) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{proto}},
	}
}

func matchDport(port uint16) []expr.Any {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, port)
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: b},
	}
}

func redirectTo(port uint16) []expr.Any {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, port)
	return []expr.Any{
		&expr.Immediate{Register: 1, Data: b},
		&expr.Redir{RegisterProtoMin: 1, RegisterProtoMax: 1},
	}
}

func verdict(kind expr.VerdictKind) []expr.Any {
	return []expr.Any{&expr.Verdict{Kind: kind}}
}

func concat(parts ...[]expr.Any) []expr.Any {
	var out []expr.Any
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func net4Mask(bits int) [4]byte {
	var m uint32 = 0xffffffff << (32 - bits)
	var out [4]byte
	binary.BigEndian.PutUint32(out[:], m)
	return out
}

func maskedBytes(addr, mask [4]byte) []byte {
	out := make([]byte, 4)
	for i := range out {
		out[i] = addr[i] & mask[i]
	}
	return out
}
