package nats_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/multinode"
	"github.com/pyvvo/funcd/internal/nodeapi"
	natsdriver "github.com/pyvvo/funcd/internal/nodeapi/nats"
	"github.com/pyvvo/funcd/internal/nodeapi/nodeapicontract"
	"github.com/pyvvo/funcd/internal/store"
)

// listen is the driver's control-plane side on loopback, under a CA made for the test, and that CA.
func listen(t *testing.T, opts ...func(natsdriver.Config) natsdriver.Config) (nodeapi.Server, *multinode.CA) {
	t.Helper()
	ca, err := multinode.LoadOrCreateCA(t.TempDir(), time.Now())
	require.NoError(t, err)
	tlsCfg, err := ca.ServerTLS(nil, time.Now)
	require.NoError(t, err)
	cfg := natsdriver.Config{Addr: "127.0.0.1:0", TLS: tlsCfg}
	for _, o := range opts {
		cfg = o(cfg)
	}
	srv, err := natsdriver.Listen(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })
	return srv, ca
}

// identity is node's identity under a certificate ca signed.
func identity(t *testing.T, ca *multinode.CA, server string, node v1.ObjectName) nodeapi.Identity {
	t.Helper()
	sign := func(csr []byte, node v1.ObjectName) ([]byte, error) {
		cert, _, _, err := ca.SignNode(csr, node, time.Now())
		return cert, err
	}
	return nodeapicontract.NodeIdentity(t, sign, ca.Pool(), server, node)
}

// scenario: driver-conformance — NATS carries the node API as the port's contract says.
func TestScenarioNATSPassesTheNodeAPIContract(t *testing.T) {
	nodeapicontract.RunContract(t, func(t *testing.T) nodeapicontract.Driver {
		srv, ca := listen(t)
		block, _ := pem.Decode(ca.PEM())
		require.NotNil(t, block)
		url := "nats://" + srv.Addr()
		return nodeapicontract.Driver{Server: srv, Connector: natsdriver.NewConnector(), URL: url, CA: block.Bytes,
			Identity: func(node v1.ObjectName) nodeapi.Identity { return identity(t, ca, url, node) }}
	})
}

// stub is a Handler that lets every node in and records the statuses and joins it gets; it answers a snapshot of big
// objects, refuses every join, and has no change to give, ever.
type stub struct {
	nodeapi.Handler
	mu       sync.Mutex
	statuses []string
	joins    int
	big      int
	reads    atomic.Int32
	read     context.Context // the context of the last read of changes
}

func (*stub) Changed(v1.ObjectName) <-chan struct{} { return nil }

func (s *stub) Changes(ctx context.Context, _ v1.ObjectName, _ uint64, _ string) (nodeapi.Feed, error) {
	s.mu.Lock()
	s.read = ctx
	s.mu.Unlock()
	s.reads.Add(1)
	return nodeapi.Feed{}, nil
}

func (*stub) Admit(context.Context, nodeapi.Peer) error { return nil }

func (s *stub) Join(context.Context, nodeapi.JoinRequest) (nodeapi.JoinResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.joins++
	return nodeapi.JoinResponse{}, fault.Forbiddenf("stub.join", "no join here")
}

func (s *stub) Status(_ context.Context, node v1.ObjectName, in nodeapi.StatusWrite) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses = append(s.statuses, string(node)+" "+string(in.Name))
	return nil
}

func (s *stub) Snapshot(context.Context, v1.ObjectName, v1.NamespaceName) (nodeapi.Snapshot, error) {
	out := nodeapi.Snapshot{Rev: 1, Timeline: "t"}
	for i := range s.big {
		raw, _ := json.Marshal(map[string]string{"data": strings.Repeat("x", 1<<20)})
		out.Objects = append(out.Objects, store.Change{Rev: uint64(i), Kind: v1.KindConfigMap, Name: "big", Object: raw})
	}
	return out, nil
}

func serve(t *testing.T, srv nodeapi.Server, h nodeapi.Handler) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx, h)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

// raw is a NATS connection under id's certificate that keeps to no rule of the driver, as a node made by hand would.
func raw(t *testing.T, srv nodeapi.Server, id nodeapi.Identity) (*nats.Conn, string) {
	t.Helper()
	leaf, err := x509.ParseCertificate(id.Certificate.Certificate[0])
	require.NoError(t, err)
	serial := leaf.SerialNumber.Text(16)
	nc, err := nats.Connect("tls://"+srv.Addr(), nats.Secure(&tls.Config{Certificates: []tls.Certificate{id.Certificate},
		RootCAs: id.Roots, MinVersion: tls.VersionTLS13}), nats.CustomInboxPrefix("_INBOX_"+leaf.Subject.CommonName+"_"+serial))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc, serial
}

// A node publishes only under its own name and certificate serial: a request it sends as another node never reaches the
// Handler, and it cannot listen to another node's requests.
func TestANodePublishesUnderItsOwnNameOnly(t *testing.T) {
	srv, ca := listen(t)
	h := &stub{}
	serve(t, srv, h)
	id := identity(t, ca, "nats://"+srv.Addr(), "vm1")
	c, err := natsdriver.NewConnector().Dial(id)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()
	require.NoError(t, c.Status(context.Background(), nodeapi.StatusWrite{Name: "mine", Status: json.RawMessage(`{}`)}))

	nc, serial := raw(t, srv, id)
	spy, err := nc.SubscribeSync("funcd.node.>")
	require.NoError(t, err)
	body, _ := json.Marshal(nodeapi.StatusWrite{Name: "theirs", Status: json.RawMessage(`{}`)})
	msg := nats.NewMsg("funcd.node.vm2." + serial + ".status")
	msg.Header.Set("Funcd-Node-Api", "1")
	msg.Data = body
	_, err = nc.RequestMsg(msg, 500*time.Millisecond)
	require.Error(t, err, "a request as another node is refused")
	require.NoError(t, c.Status(context.Background(), nodeapi.StatusWrite{Name: "again", Status: json.RawMessage(`{}`)}))
	_, err = spy.NextMsg(300 * time.Millisecond)
	require.Error(t, err, "a node sees no other node's requests")
	h.mu.Lock()
	defer h.mu.Unlock()
	require.Equal(t, []string{"vm1 mine", "vm1 again"}, h.statuses)
}

// The control plane answers on a connection that may publish anywhere, so it answers only into the inbox of whoever
// asked: a request or a join that names another reply subject is dropped, and no node hears another's answers.
func TestAnAnswerGoesOnlyIntoTheInboxOfTheNodeThatAsked(t *testing.T) {
	srv, ca := listen(t)
	h := &stub{}
	serve(t, srv, h)
	vm1, serial1 := raw(t, srv, identity(t, ca, "nats://"+srv.Addr(), "vm1"))
	vm2, serial2 := raw(t, srv, identity(t, ca, "nats://"+srv.Addr(), "vm2"))
	heard, err := vm2.SubscribeSync("_INBOX_vm2_" + serial2 + ".>")
	require.NoError(t, err)
	called, err := vm2.SubscribeSync("funcd.call.vm2." + serial2)
	require.NoError(t, err)
	require.NoError(t, vm2.Flush())
	joiner, err := nats.Connect("tls://"+srv.Addr(), nats.Secure(&tls.Config{RootCAs: ca.Pool(), MinVersion: tls.VersionTLS13}),
		nats.CustomInboxPrefix("_INBOX_join"))
	require.NoError(t, err)
	t.Cleanup(joiner.Close)
	status := func(reply string) *nats.Msg {
		msg := nats.NewMsg("funcd.node.vm1." + serial1 + ".status")
		msg.Reply = reply
		msg.Header.Set("Funcd-Node-Api", "1")
		msg.Data = []byte(`{"name":"x","status":{}}`)
		return msg
	}
	require.NoError(t, vm1.PublishMsg(status("_INBOX_vm2_"+serial2+".a")))
	require.NoError(t, vm1.PublishMsg(status("funcd.call.vm2."+serial2)))
	require.NoError(t, joiner.PublishMsg(&nats.Msg{Subject: "funcd.join", Reply: "_INBOX_vm2_" + serial2 + ".b", Data: []byte(`{}`)}))
	_, err = heard.NextMsg(300 * time.Millisecond)
	require.ErrorIs(t, err, nats.ErrTimeout, "no answer reaches another node's inbox")
	_, err = called.NextMsg(10 * time.Millisecond)
	require.ErrorIs(t, err, nats.ErrTimeout, "nor its call subject")
	h.mu.Lock()
	require.Empty(t, h.statuses, "a dropped request reaches no Handler")
	require.Zero(t, h.joins, "nor a dropped join")
	h.mu.Unlock()

	_, err = vm1.RequestMsg(status(""), 5*time.Second)
	require.NoError(t, err, "the same request with the node's own inbox is answered")
	_, err = joiner.Request("funcd.join", []byte(`{}`), 5*time.Second)
	require.NoError(t, err, "and so is a join with the joins' inbox, here with its refusal")
}

// A node that speaks a node API version this control plane does not serve is refused, whatever it sends (ADR-0374
// Decision 1).
func TestANodeOnAnotherNodeAPIVersionIsRefused(t *testing.T) {
	srv, ca := listen(t)
	h := &stub{}
	serve(t, srv, h)
	nc, serial := raw(t, srv, identity(t, ca, "nats://"+srv.Addr(), "vm1"))
	msg := nats.NewMsg("funcd.node.vm1." + serial + ".status")
	msg.Header.Set("Funcd-Node-Api", "99")
	msg.Data = []byte(`{"name":"x","status":{}}`)
	answer, err := nc.RequestMsg(msg, 5*time.Second)
	require.NoError(t, err)
	var env struct {
		Error *fault.Problem `json:"error"`
	}
	require.NoError(t, json.Unmarshal(answer.Data, &env))
	require.NotNil(t, env.Error)
	require.Equal(t, fault.Conflict, fault.KindOfProblem(*env.Error))
	require.Contains(t, env.Error.Detail, "serves node API versions 1, and the node speaks 99; upgrade or downgrade the node")
	require.Empty(t, h.statuses)

	joins, err := nats.Connect("tls://"+srv.Addr(), nats.Secure(&tls.Config{RootCAs: ca.Pool(), MinVersion: tls.VersionTLS13}),
		nats.CustomInboxPrefix("_INBOX_join"))
	require.NoError(t, err)
	t.Cleanup(joins.Close)
	join := nats.NewMsg("funcd.join")
	join.Header.Set("Funcd-Node-Api", "99")
	join.Data = []byte(`{"token":"t"}`)
	answer, err = joins.RequestMsg(join, 5*time.Second)
	require.NoError(t, err)
	env.Error = nil
	require.NoError(t, json.Unmarshal(answer.Data, &env))
	require.NotNil(t, env.Error)
	require.Equal(t, fault.Conflict, fault.KindOfProblem(*env.Error), "a join too")
	h.mu.Lock()
	defer h.mu.Unlock()
	require.Zero(t, h.joins, "before its token is read")
}

// answerCalls answers every call c takes, as a node's call loop does, until the test ends.
func answerCalls(t *testing.T, c nodeapi.Client) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			calls, err := c.Calls(ctx, 200*time.Millisecond)
			if err != nil {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			for _, call := range calls {
				_ = c.Answer(ctx, call.ID, nodeapi.Answer{})
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func holdStatus(srv nodeapi.Server) error {
	_, err := srv.Call(context.Background(), "vm1", nodeapi.Call{Kind: nodeapi.CallHoldStatus})
	return err
}

// A node whose connection ends is offline at once once the server has read the end: the control plane's call finds no
// responder. A call sent before the server read it still goes to the node's subscription and waits its timeout.
func TestANodeThatLeavesIsOfflineAtOnce(t *testing.T) {
	srv, ca := listen(t)
	serve(t, srv, &stub{})
	c, err := natsdriver.NewConnector().Dial(identity(t, ca, "nats://"+srv.Addr(), "vm1"))
	require.NoError(t, err)
	answerCalls(t, c)
	require.Eventually(t, func() bool { return holdStatus(srv) == nil }, 10*time.Second, 20*time.Millisecond)
	require.NoError(t, c.Close())
	left := time.Now()
	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := srv.Call(ctx, "vm1", nodeapi.Call{Kind: nodeapi.CallHoldStatus})
		return fault.KindOf(err) == fault.Unavailable && strings.Contains(err.Error(), "is offline") &&
			time.Since(start) < 100*time.Millisecond
	}, 2*time.Second, 10*time.Millisecond, "a call fails at once once the server read the node's end")
	t.Logf("calls fail at once %s after the node left", time.Since(left).Round(time.Millisecond))
}

// ended is a stub whose admission refuses every request once end is set, as a certificate past its end is (ADR-0364).
type ended struct {
	stub
	end atomic.Bool
}

func (e *ended) Admit(context.Context, nodeapi.Peer) error {
	if e.end.Load() {
		return fault.Unauthorizedf("stub.admit", "this node certificate ended")
	}
	return nil
}

// A node whose certificate the control plane no longer takes gets no call, though its link is open: its answers are NATS
// replies, which no admission checks, so a refused request withdraws its certificate from the calls.
func TestANodeWhoseCertificateEndedGetsNoCall(t *testing.T) {
	srv, ca := listen(t)
	h := &ended{}
	serve(t, srv, h)
	c, err := natsdriver.NewConnector().Dial(identity(t, ca, "nats://"+srv.Addr(), "vm1"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	answerCalls(t, c)
	require.Eventually(t, func() bool { return holdStatus(srv) == nil }, 10*time.Second, 20*time.Millisecond)
	h.end.Store(true)
	_, err = c.Feed(context.Background(), 0, "t", time.Millisecond)
	require.Equal(t, fault.Unauthorized, fault.KindOf(err))
	start := time.Now()
	err = holdStatus(srv)
	require.Equal(t, fault.Unavailable, fault.KindOf(err), "a call to it fails at once")
	require.ErrorContains(t, err, "is offline")
	require.Less(t, time.Since(start), time.Second)
}

// replaced is a stub whose admission refuses the certificate of serial old, as it refuses the certificate a renewal
// replaced (ADR-0364).
type replaced struct {
	stub
	old atomic.Value
}

func (r *replaced) Admit(_ context.Context, p nodeapi.Peer) error {
	if old, _ := r.old.Load().(string); p.Serial == old {
		return fault.Unauthorizedf("stub.admit", "certificate %s is not the node's current one", p.Serial)
	}
	return nil
}

// A late request under the certificate a renewal replaced is refused, and leaves the node's calls on its new one.
func TestALateRequestUnderAReplacedCertificateKeepsTheNodesCalls(t *testing.T) {
	srv, ca := listen(t)
	h := &replaced{}
	serve(t, srv, h)
	oldID := identity(t, ca, "nats://"+srv.Addr(), "vm1")
	old, err := natsdriver.NewConnector().Dial(oldID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = old.Close() })
	renewed, err := natsdriver.NewConnector().Dial(identity(t, ca, "nats://"+srv.Addr(), "vm1"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = renewed.Close() })
	_, err = renewed.Feed(context.Background(), 0, "t", time.Millisecond)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(oldID.Certificate.Certificate[0])
	require.NoError(t, err)
	h.old.Store(leaf.SerialNumber.Text(16))
	_, err = old.Feed(context.Background(), 0, "t", time.Millisecond)
	require.Equal(t, fault.Unauthorized, fault.KindOf(err))
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err = srv.Call(ctx, "vm1", nodeapi.Call{Kind: nodeapi.CallHoldStatus})
	require.ErrorIs(t, err, context.DeadlineExceeded, "the call is not refused as offline, and waits for its answer")
	got, err := renewed.Calls(context.Background(), time.Second)
	require.NoError(t, err)
	require.Len(t, got, 1, "it went to the node's new certificate")
}

// silentLink relays a node's connection to the node API, and can go silent: it drops every byte and keeps both
// connections open, as a link that fails with no reset does.
type silentLink struct {
	ln     net.Listener
	target string
	ended  chan struct{} // closed when the node API ends a connection while the link is silent
	once   sync.Once

	mu     sync.Mutex
	silent bool
	nodes  []net.Conn
}

func newSilentLink(t *testing.T, target string) *silentLink {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	l := &silentLink{ln: ln, target: target, ended: make(chan struct{})}
	t.Cleanup(func() {
		_ = ln.Close()
		l.heal()
	})
	go l.accept()
	return l
}

func (l *silentLink) accept() {
	for {
		node, err := l.ln.Accept()
		if err != nil {
			return
		}
		api, err := net.Dial("tcp", l.target)
		if err != nil {
			_ = node.Close()
			continue
		}
		l.mu.Lock()
		l.nodes = append(l.nodes, node)
		l.mu.Unlock()
		go l.relay(node, api, false)
		go l.relay(api, node, true)
	}
}

func (l *silentLink) isSilent() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.silent
}

func (l *silentLink) relay(src, dst net.Conn, fromAPI bool) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if err != nil {
			if !l.isSilent() {
				_ = dst.Close()
			} else if fromAPI {
				l.once.Do(func() { close(l.ended) })
			}
			return
		}
		if l.isSilent() {
			continue
		}
		if _, err := dst.Write(buf[:n]); err != nil {
			return
		}
	}
}

func (l *silentLink) silence() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.silent = true
}

// heal ends the node's side, as the reset of a link that answers again does, and relays again.
func (l *silentLink) heal() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.silent = false
	for _, c := range l.nodes {
		_ = c.Close()
	}
	l.nodes = nil
}

// A node whose link goes silent stays subscribed until the node API ends the link for its unanswered pings; a call to
// it then fails at once. Once the link answers again, the node dials again and takes its calls.
func TestANodeWhoseLinkGoesSilentIsOfflineOnceTheLinkEnds(t *testing.T) {
	srv, ca := listen(t, func(c natsdriver.Config) natsdriver.Config { return natsdriver.WithPing(c, 100*time.Millisecond) })
	serve(t, srv, &stub{})
	link := newSilentLink(t, srv.Addr())
	c, err := natsdriver.NewConnector().Dial(identity(t, ca, "nats://"+link.ln.Addr().String(), "vm1"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	answerCalls(t, c)
	require.Eventually(t, func() bool { return holdStatus(srv) == nil }, 10*time.Second, 20*time.Millisecond)

	link.silence()
	select {
	case <-link.ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the node API kept a link that answers no ping")
	}
	start := time.Now()
	err = holdStatus(srv)
	require.Equal(t, fault.Unavailable, fault.KindOf(err))
	require.ErrorContains(t, err, "is offline")
	require.Less(t, time.Since(start), time.Second, "at once, once the link ended")

	link.heal()
	start = time.Now()
	require.Eventually(t, func() bool { return holdStatus(srv) == nil }, 20*time.Second, 50*time.Millisecond,
		"the node dials again and takes its calls")
	t.Logf("the node took a call %s after its link answered again", time.Since(start).Round(100*time.Millisecond))
}

// A request that waits for its answer holds little memory on its node: nats.go's default queue for the answer's
// subscription, 65,536 messages, is a channel of 512 KiB, which a node with its feed open would hold all the time.
func TestAWaitingRequestHoldsLittleMemory(t *testing.T) {
	srv, ca := listen(t)
	h := &stub{}
	serve(t, srv, h)
	const n = 40
	clients := make([]nodeapi.Client, n)
	for i := range clients {
		c, err := natsdriver.NewConnector().Dial(identity(t, ca, "nats://"+srv.Addr(), v1.ObjectName(fmt.Sprintf("vm%d", i))))
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.Close() })
		clients[i] = c
	}
	heap := func() uint64 {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return m.HeapInuse
	}
	before := heap()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for _, c := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.Feed(ctx, 0, "t", 3*time.Second)
		}()
	}
	require.Eventually(t, func() bool { return h.reads.Load() >= n }, 5*time.Second, 10*time.Millisecond, "every feed waits")
	after := heap()
	cancel()
	wg.Wait()
	require.Less(t, (int64(after)-int64(before))/n, int64(64<<10), "a waiting request holds under 64 KiB, both sides")
}

// A request in flight ends when the server closes: its handler no longer waits out its feed's wait on a store that may be
// gone, as an HTTPS request ends with its connection.
func TestARequestInFlightEndsWhenTheServerCloses(t *testing.T) {
	srv, ca := listen(t)
	h := &stub{}
	serve(t, srv, h)
	c, err := natsdriver.NewConnector().Dial(identity(t, ca, "nats://"+srv.Addr(), "vm1"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	go func() { _, _ = c.Feed(context.Background(), 0, "t", time.Minute) }()
	read := func() context.Context {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.read
	}
	require.Eventually(t, func() bool { return read() != nil }, 5*time.Second, 10*time.Millisecond, "the feed waits")
	require.NoError(t, srv.Close())
	require.Eventually(t, func() bool { return read().Err() != nil }, time.Second, 10*time.Millisecond,
		"the handler's request ends with the server")
}

// An answer over a NATS message's size comes in chunks, and the node gets it whole.
func TestABigAnswerComesInChunks(t *testing.T) {
	srv, ca := listen(t)
	serve(t, srv, &stub{big: 12})
	c, err := natsdriver.NewConnector().Dial(identity(t, ca, "nats://"+srv.Addr(), "vm1"))
	require.NoError(t, err)
	defer func() { _ = c.Close() }()
	snap, err := c.Snapshot(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, snap.Objects, 12, "twelve objects of 1 MiB each, over the 8 MiB a message holds")
	for _, o := range snap.Objects {
		require.Greater(t, len(o.Object), 1<<20)
	}
}
