package egress

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/miekg/dns"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/auth"
)

const (
	dnsTimeout   = 5 * time.Second
	minRecordTTL = time.Second
	// bindRetry paces the bind while the forwarder's address does not exist yet: the bridge appears with the first worker
	// (ADR-0238 Decision 3).
	bindRetry = time.Second
	// refusedLookupLogInterval limits the refused-lookup lines to one per source per interval (ADR-0315): lookups repeat far
	// faster than connections, so a line per refusal would let one worker fill the node's log.
	refusedLookupLogInterval = 10 * time.Second
)

// Forwarder is the ONLY resolver a worker can reach (F80 redirects worker :53 into it). It answers known workers only and
// forwards to the node resolver only the names a domain rule of an EgressPolicy that applies to them allows; anything else
// gets REFUSED and nothing goes upstream (ADR-0315). It records (src, question's name)→resolved-IP set, exposing the
// reverse DomainsFor the gateway uses to authorize domain rules, with a matched wildcard token. It satisfies
// DomainResolver. Serve blocks until ctx is cancelled.
type Forwarder interface {
	Serve(ctx context.Context) error
	DomainResolver
}

// DomainRules supplies the domain rules (exact names and "*." wildcards) of the EgressPolicies that apply to a worker's
// principal: who they cover is what the PDP grants (ADR-0315). An error means they could not be read, and the lookup is
// refused.
type DomainRules interface {
	For(ctx context.Context, principal auth.EntityRef) ([]string, error)
}

// NewForwarder builds the forwarder over the node resolver (github.com/miekg/dns; BSD-3-Clause), listening on the bridge
// address. The correlation map keys on the DNS client's source IP so a worker only inherits the domains IT resolved.
// workers maps that source IP → its principal and rules supplies that principal's domain rules, so a resolved FQDN also
// records any "*.x.com" it matched (keeping wildcard authorization an exact set-membership check over a forwarder-derived
// token). A nil workers or rules knows no source or no rule, so every query is refused; a nil logger means slog.Default().
func NewForwarder(listen, upstream netip.AddrPort, workers WorkerIndex, rules DomainRules, logger *slog.Logger) Forwarder {
	if logger == nil {
		logger = slog.Default()
	}
	f := &forwarder{
		listen:   listen,
		upstream: upstream,
		workers:  workers,
		rules:    rules,
		logger:   logger,
		refused:  refusalLog{now: time.Now, open: map[netip.Addr]*refusalWindow{}},
	}
	f.corr = newCorrelator(time.Now, func(src netip.Addr) []string {
		ref, ok := f.principal(src)
		if !ok || rules == nil {
			return nil
		}
		patterns, err := rules.For(context.Background(), ref)
		if err != nil {
			return nil // no token: a failed read grants nothing
		}
		return patterns
	})
	return f
}

// forwarder is the miekg/dns-backed Forwarder (the :53 serving path; exercised on Linux/Lima). The
// correlation map + DomainsFor + wildcard logic (correlator) are pure and unit-tested cross-platform.
type forwarder struct {
	listen   netip.AddrPort
	upstream netip.AddrPort
	workers  WorkerIndex
	rules    DomainRules
	logger   *slog.Logger
	refused  refusalLog
	corr     *correlator
}

// principal resolves a query's source to its worker's principal.
func (f *forwarder) principal(src netip.Addr) (auth.EntityRef, bool) {
	if f.workers == nil {
		return auth.EntityRef{}, false
	}
	return f.workers.Lookup(src)
}

// Serve runs the forwarder until ctx is cancelled, retrying each bindRetry while its address does not exist yet (ADR-0238
// Decision 3): before the bridge appears a lookup fails, and any other bind error ends Serve.
func (f *forwarder) Serve(ctx context.Context) error {
	for warned := false; ; {
		err := f.serve(ctx)
		if !errors.Is(err, syscall.EADDRNOTAVAIL) {
			return err
		}
		if !warned {
			f.logger.WarnContext(ctx, "the egress dns forwarder cannot listen yet; retrying each second", "addr", f.listen.String(), "error", err)
			warned = true
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(bindRetry):
		}
	}
}

// serve runs the forwarder on UDP and TCP until ctx is cancelled: F80 redirects worker :53 on both, and a
// truncated UDP answer is retried over TCP (RFC 7766). Each answered query records (src, question's name)→IPs.
func (f *forwarder) serve(ctx context.Context) error {
	const op = "egress.forwarder.Serve"
	udpClient := &dns.Client{Net: "udp", Timeout: dnsTimeout}
	tcpClient := &dns.Client{Net: "tcp", Timeout: dnsTimeout}
	mux := dns.NewServeMux()
	mux.HandleFunc(".", func(w dns.ResponseWriter, req *dns.Msg) {
		client, limit := udpClient, dns.MinMsgSize
		if opt := req.IsEdns0(); opt != nil {
			limit = int(opt.UDPSize())
		}
		if _, overTCP := w.RemoteAddr().(*net.TCPAddr); overTCP {
			client, limit = tcpClient, dns.MaxMsgSize
		}
		f.handle(ctx, client, limit, w, req)
	})
	servers := []*dns.Server{
		{Addr: f.listen.String(), Net: "udp", Handler: mux},
		{Addr: f.listen.String(), Net: "tcp", Handler: mux},
	}
	errCh := make(chan error, len(servers))
	started := make([]chan struct{}, len(servers))
	exited := make([]chan struct{}, len(servers))
	for i, srv := range servers {
		started[i], exited[i] = make(chan struct{}), make(chan struct{})
		srv.NotifyStartedFunc = func() { close(started[i]) }
		go func() {
			defer close(exited[i])
			errCh <- srv.ListenAndServe()
		}()
	}
	// ShutdownContext is a no-op on a server ListenAndServe has not marked started, so each server is shut
	// down only once it has started (or has already failed), and Serve returns after its goroutine has.
	shutdown := func() {
		for i, srv := range servers {
			select {
			case <-started[i]:
				_ = srv.ShutdownContext(context.Background())
			case <-exited[i]:
			}
			<-exited[i]
		}
	}
	select {
	case <-ctx.Done():
		shutdown()
		return ctx.Err()
	case err := <-errCh:
		shutdown()
		if err != nil {
			return fault.Wrapf(err, fault.Internal, op, "dns forwarder listen %s", f.listen)
		}
		return nil
	}
}

// handle answers one query: REFUSED unless admit lets it go upstream. It forwards over the transport the query arrived on
// (so a TCP retry gets the whole answer), records the resolved A/AAAA answers per (src, question's name), and relays the
// response fitted to the client's limit: 512 bytes over UDP without EDNS0 (RFC 1035), else the OPT UDP size, 65535 over
// TCP. The unpacked reply has Compress false, so Truncate re-compresses it and sets TC on what still overflows.
func (f *forwarder) handle(ctx context.Context, client *dns.Client, limit int, w dns.ResponseWriter, req *dns.Msg) {
	src, _ := addrFromNet(w.RemoteAddr())
	name, ok := f.admit(ctx, src, req)
	if !ok {
		fail := new(dns.Msg)
		fail.SetRcode(req, dns.RcodeRefused)
		_ = w.WriteMsg(fail)
		return
	}
	resp, _, err := client.Exchange(req, f.upstream.String())
	if err != nil || resp == nil {
		fail := new(dns.Msg)
		fail.SetRcode(req, dns.RcodeServerFailure)
		_ = w.WriteMsg(fail)
		return
	}
	f.record(src, name, resp)
	resp.Truncate(limit)
	_ = w.WriteMsg(resp)
}

// admit decides whether req goes upstream (ADR-0315 Decision 2): a known worker's standard query for a host name that a
// domain rule applying to it allows. It returns the name, lower-cased and with no trailing dot. Anything else, a failed
// read of the rules included, is refused and logged.
func (f *forwarder) admit(ctx context.Context, src netip.Addr, req *dns.Msg) (string, bool) {
	ref, known := f.principal(src)
	if !known {
		f.refuse(ctx, src, auth.EntityRef{}, "", "unknown source")
		return "", false
	}
	if req.Opcode != dns.OpcodeQuery || len(req.Question) != 1 {
		f.refuse(ctx, src, ref, "", "not a standard query")
		return "", false
	}
	name := strings.TrimSuffix(strings.ToLower(req.Question[0].Name), ".")
	if !hostName(name) {
		f.refuse(ctx, src, ref, name, "a name outside host-name characters")
		return "", false
	}
	if f.rules == nil {
		f.refuse(ctx, src, ref, name, "no domain rule allows it")
		return "", false
	}
	rules, err := f.rules.For(ctx, ref)
	if err != nil {
		f.refuse(ctx, src, ref, name, "the EgressPolicies could not be read: "+err.Error())
		return "", false
	}
	if !slices.ContainsFunc(rules, func(rule string) bool { return rule == name || matchWildcard(rule, name) }) {
		f.refuse(ctx, src, ref, name, "no domain rule allows it")
		return "", false
	}
	return name, true
}

// refuse writes the refused lookup's line, unless its source had one within refusedLookupLogInterval. A source that is no
// known worker shares one allowance, and its line names no domain.
func (f *forwarder) refuse(ctx context.Context, src netip.Addr, ref auth.EntityRef, name, reason string) {
	key := src
	if ref.Type == "" {
		key = netip.Addr{}
	}
	if more, ok := f.refused.note(key); ok {
		f.logger.InfoContext(ctx, "egress lookup refused",
			"namespace", ref.Namespace, "function", ref.Name, "domain", name, "source", src.String(), "reason", reason, "more", more)
	}
}

// refusalLog holds, per source, when its next refused-lookup line may be written and how many refusals it has held back. A
// window that ended with nothing held back is forgotten at the next line; one with a count waits for its source's next
// refusal. So it holds at most one window per worker address, and one for every other source together.
type refusalLog struct {
	now  func() time.Time
	mu   sync.Mutex
	open map[netip.Addr]*refusalWindow
}

type refusalWindow struct {
	until time.Time
	more  int
}

// note counts one refusal for key. It reports whether a line is due, with how many were refused since the last line.
func (l *refusalLog) note(key netip.Addr) (int, bool) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.open[key]
	if w != nil && now.Before(w.until) {
		w.more++
		return 0, false
	}
	more := 0
	if w != nil {
		more = w.more
	}
	for k, old := range l.open {
		if !now.Before(old.until) && old.more == 0 {
			delete(l.open, k)
		}
	}
	l.open[key] = &refusalWindow{until: now.Add(refusedLookupLogInterval)}
	return more, true
}

// record binds each A/AAAA answer to name, the question's name a rule allowed, with that record's TTL. A name in the answer
// section is not read: an answer reached through a CNAME counts for the name asked (ADR-0315).
func (f *forwarder) record(src netip.Addr, name string, resp *dns.Msg) {
	for _, rr := range resp.Answer {
		var ip netip.Addr
		switch a := rr.(type) {
		case *dns.A:
			ip, _ = netip.AddrFromSlice(a.A.To4())
		case *dns.AAAA:
			ip, _ = netip.AddrFromSlice(a.AAAA.To16())
		default:
			continue
		}
		if !ip.IsValid() {
			continue
		}
		ttl := time.Duration(rr.Header().Ttl) * time.Second
		f.corr.record(src, name, []netip.Addr{ip}, ttl)
	}
}

// DomainsFor implements DomainResolver over the correlation map.
func (f *forwarder) DomainsFor(src netip.Addr, dst netip.Addr) []string {
	return f.corr.DomainsFor(src, dst)
}

// addrFromNet extracts a netip.Addr from a net.Addr (UDP/TCP), for keying the correlation map.
func addrFromNet(a net.Addr) (netip.Addr, bool) {
	switch v := a.(type) {
	case *net.UDPAddr:
		ip, ok := netip.AddrFromSlice(v.IP)
		return ip.Unmap(), ok
	case *net.TCPAddr:
		ip, ok := netip.AddrFromSlice(v.IP)
		return ip.Unmap(), ok
	default:
		return netip.Addr{}, false
	}
}

// --- the pure correlation map (unit-tested cross-platform) ---------------------------------------------

// corrKey is a (worker source IP, resolved destination IP) pair — the reverse index the gateway queries.
type corrKey struct{ src, dst netip.Addr }

// minSweep is the record count below which the correlator never sweeps, so a small map is not rescanned.
const minSweep = 1024

// maxPairsPerSource caps the live (dst, domain) pairs one worker source holds. The worker picks the names
// it resolves and, through a zone it controls, their TTLs, so only a cap bounds the correlator's memory
// (to maxPairsPerSource per worker on the subnet, ADR-0117 §4a); a pair shed at the cap fails closed.
const maxPairsPerSource = 1024

// correlator records (src, domain)→resolved-IP with a TTL and exposes the reverse DomainsFor(src, dst).
// It is the trust anchor's data structure: only domains funcd's OWN forwarder resolved for a worker are
// ever returned. wildcardsFor supplies the domain rules that apply to the worker, so a matched FQDN
// also injects its `*.x.com` pattern token (keeping wildcard authorization an exact set-membership check).
type correlator struct {
	now          func() time.Time
	wildcardsFor func(src netip.Addr) []string

	mu      sync.Mutex
	entries map[corrKey]map[string]time.Time // domain → expiry
	bySrc   map[netip.Addr]*srcPairs         // per-source view of entries, for the per-source cap
	records int                              // (key, domain) pairs held in entries
	sweepAt int                              // records count that triggers the next sweep
}

// srcPairs is one source's dst keys in entries and its (key, domain) pair count.
type srcPairs struct {
	dsts  map[netip.Addr]struct{}
	pairs int
}

func newCorrelator(now func() time.Time, wildcardsFor func(netip.Addr) []string) *correlator {
	return &correlator{
		now:          now,
		wildcardsFor: wildcardsFor,
		entries:      map[corrKey]map[string]time.Time{},
		bySrc:        map[netip.Addr]*srcPairs{},
		sweepAt:      minSweep,
	}
}

// record binds (src, domain) to each resolved ip until now+ttl (a zero/negative ttl is clamped to a
// short floor so a 0-TTL answer is still briefly usable for the connection it was resolved for).
func (c *correlator) record(src netip.Addr, domain string, ips []netip.Addr, ttl time.Duration) {
	if ttl < minRecordTTL {
		ttl = minRecordTTL
	}
	now := c.now()
	exp := now.Add(ttl)
	domain = strings.TrimSuffix(strings.ToLower(domain), ".")
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ip := range ips {
		k := corrKey{src: src, dst: ip}
		if _, ok := c.entries[k][domain]; !ok {
			sp := c.bySrc[src]
			if sp == nil {
				sp = &srcPairs{dsts: map[netip.Addr]struct{}{}}
				c.bySrc[src] = sp
			}
			if sp.pairs >= maxPairsPerSource {
				c.shed(src, now)
			}
			sp.dsts[ip] = struct{}{}
			sp.pairs++
			c.records++
		}
		m := c.entries[k]
		if m == nil {
			m = map[string]time.Time{}
			c.entries[k] = m
		}
		m[domain] = exp
	}
	if c.records >= c.sweepAt {
		c.sweep(now)
	}
}

// hostName reports whether a lower-cased name holds only host-name characters: letters, digits, "-", "_" and the dots
// between labels. Nothing else is forwarded or recorded (ADR-0315).
func hostName(name string) bool {
	return name != "" && !strings.ContainsFunc(name, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' && r != '.'
	})
}

// shed makes room under src's cap before a new pair is added: it drops src's expired pairs and then, while
// more than three quarters of the cap are live, its earliest-expiring ones. Shedding a batch keeps a flood
// at amortized O(log cap) per record, and a flood displaces only the flooding worker's own records. Caller
// holds mu.
func (c *correlator) shed(src netip.Addr, now time.Time) {
	type pair struct {
		k   corrKey
		d   string
		exp time.Time
	}
	var live []pair
	for dst := range c.bySrc[src].dsts {
		k := corrKey{src: src, dst: dst}
		for d, exp := range c.entries[k] {
			if now.Before(exp) {
				live = append(live, pair{k: k, d: d, exp: exp})
			} else {
				c.drop(k, d)
			}
		}
	}
	keep := maxPairsPerSource - maxPairsPerSource/4
	if len(live) <= keep {
		return
	}
	slices.SortFunc(live, func(a, b pair) int { return a.exp.Compare(b.exp) })
	for _, p := range live[:len(live)-keep] {
		c.drop(p.k, p.d)
	}
}

// drop removes one (key, domain) pair and any key it empties; a source's view stays while the caller may
// still add to it. Caller holds mu.
func (c *correlator) drop(k corrKey, domain string) {
	m := c.entries[k]
	delete(m, domain)
	c.records--
	sp := c.bySrc[k.src]
	sp.pairs--
	if len(m) == 0 {
		delete(c.entries, k)
		delete(sp.dsts, k.dst)
	}
}

// sweep drops every expired (key, domain) pair, every emptied key and every emptied source, then schedules
// the next sweep at twice the live count. The forwarder lives as long as the daemon, so the TTL must bound
// retention and not only visibility: memory stays within twice the live count at the last sweep (floor
// minSweep), at amortized O(1) per record. Caller holds mu.
func (c *correlator) sweep(now time.Time) {
	for k, m := range c.entries {
		for d, exp := range m {
			if !now.Before(exp) {
				c.drop(k, d)
			}
		}
	}
	for src, sp := range c.bySrc {
		if sp.pairs == 0 {
			delete(c.bySrc, src)
		}
	}
	c.sweepAt = max(2*c.records, minSweep)
}

// DomainsFor returns the non-expired domains this worker resolved to dst, plus any matched namespace
// wildcard pattern token. Empty ⇒ the worker never resolved a policy domain to dst (so only a CIDR rule
// can permit it — the B1 default-deny for domains). The result is sorted for determinism.
func (c *correlator) DomainsFor(src netip.Addr, dst netip.Addr) []string {
	now := c.now()
	c.mu.Lock()
	m := c.entries[corrKey{src: src, dst: dst}]
	fqdns := make([]string, 0, len(m))
	for d, exp := range m {
		if now.Before(exp) {
			fqdns = append(fqdns, d)
		}
	}
	c.mu.Unlock()

	set := map[string]struct{}{}
	for _, d := range fqdns {
		set[d] = struct{}{}
	}
	if c.wildcardsFor != nil {
		patterns := c.wildcardsFor(src)
		for _, d := range fqdns {
			for _, p := range patterns {
				if matchWildcard(p, d) {
					set[p] = struct{}{}
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// matchWildcard reports whether fqdn matches a "*.x.com" pattern: the pattern must be "*."-prefixed and
// fqdn must end in the pattern's suffix with at least one extra leading label (so "*.x.com" matches
// "api.x.com" and "a.b.x.com" but not the bare "x.com").
func matchWildcard(pattern, fqdn string) bool {
	rest, ok := strings.CutPrefix(pattern, "*.")
	if !ok || rest == "" {
		return false
	}
	suffix := "." + rest
	return strings.HasSuffix(fqdn, suffix) && len(fqdn) > len(suffix)
}
