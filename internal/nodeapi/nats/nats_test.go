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
}

func (*stub) Changed() <-chan struct{} { return nil }

func (s *stub) Changes(context.Context, v1.ObjectName, uint64, string) (nodeapi.Feed, error) {
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

// A node that speaks a node API version this control plane does not serve is refused, whatever it sends (decision 12).
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
	require.Contains(t, env.Error.Detail, "upgrade or downgrade the node")
	require.Empty(t, h.statuses)
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

// A node whose connection ends is offline at once: the control plane's call finds no responder.
func TestANodeThatLeavesIsOfflineAtOnce(t *testing.T) {
	srv, ca := listen(t)
	serve(t, srv, &stub{})
	c, err := natsdriver.NewConnector().Dial(identity(t, ca, "nats://"+srv.Addr(), "vm1"))
	require.NoError(t, err)
	answerCalls(t, c)
	require.Eventually(t, func() bool { return holdStatus(srv) == nil }, 10*time.Second, 20*time.Millisecond)
	require.NoError(t, c.Close())
	start := time.Now()
	err = holdStatus(srv)
	require.Equal(t, fault.Unavailable, fault.KindOf(err))
	require.ErrorContains(t, err, "is offline")
	require.Less(t, time.Since(start), time.Second, "at once")
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
