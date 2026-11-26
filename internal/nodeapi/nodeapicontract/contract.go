// Package nodeapicontract is the shared conformance suite for the node API port (FEAT-0002). RunContract asserts what every
// driver carries alike: a request reaches the Handler as the node its certificate names, with its input and its answer; an
// error keeps its kind; Admit refuses a request before its method runs; a feed waits for a change and ends empty after its
// wait; a revision the Handler no longer serves is fault.PreconditionFailed; a call reaches the node that polls for it and
// fails at once for one that does not; and a join sends its token only to a control plane under the CA it trusts.
package nodeapicontract

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/funclog/logread"
	"github.com/pyvvo/funcd/internal/nodeapi"
	"github.com/pyvvo/funcd/internal/store"
)

// Driver is one driver under test.
type Driver struct {
	Server    nodeapi.Server    // the control plane's side, which RunContract serves with the suite's Handler
	Connector nodeapi.Connector // the node side
	URL       string            // where a node joins and reaches the Server
	CA        []byte            // the DER of the CA the Server's certificate chains to
	// Identity is a node's identity under a certificate that CA signed for node, reporting release "test" and platform
	// "test/contract".
	Identity func(node v1.ObjectName) nodeapi.Identity
}

// NodeIdentity is node's identity on the control plane at server: a key made for it, the certificate sign makes of its
// CSR, and roots, reporting release "test" and platform "test/contract".
func NodeIdentity(t *testing.T, sign func(csrPEM []byte, node v1.ObjectName) ([]byte, error), roots *x509.CertPool,
	server string, node v1.ObjectName) nodeapi.Identity {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: string(node)}}, key)
	require.NoError(t, err)
	certPEM, err := sign(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr}), node)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	cert, err := tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	require.NoError(t, err)
	return nodeapi.Identity{Server: server, Certificate: cert, Roots: roots, Version: "test", Platform: "test/contract"}
}

// RunContract runs the driver-agnostic node API scenarios against the Driver from newDriver, a fresh one each call.
func RunContract(t *testing.T, newDriver func(t *testing.T) Driver) {
	t.Helper()
	t.Run("every-request-reaches-the-handler-as-its-node", func(t *testing.T) { testRequests(t, serve(t, newDriver(t))) })
	t.Run("an-error-keeps-its-kind", func(t *testing.T) { testErrorKinds(t, serve(t, newDriver(t))) })
	t.Run("admit-refuses-before-the-request", func(t *testing.T) { testAdmitRefuses(t, serve(t, newDriver(t))) })
	t.Run("a-feed-waits-for-a-change", func(t *testing.T) { testFeedWaits(t, serve(t, newDriver(t))) })
	t.Run("a-gone-revision-fails-its-precondition", func(t *testing.T) { testFeedGone(t, serve(t, newDriver(t))) })
	t.Run("a-call-reaches-the-node-that-polls", func(t *testing.T) { testCalls(t, serve(t, newDriver(t))) })
	t.Run("a-join-sends-its-token-only-to-the-ca-it-trusts", func(t *testing.T) { testJoin(t, serve(t, newDriver(t))) })
}

// served is a Driver whose Server serves h.
type served struct {
	Driver
	h *handler
}

func serve(t *testing.T, d Driver) served {
	t.Helper()
	h := &handler{changed: make(chan struct{}), refuse: map[v1.ObjectName]error{}, errs: map[string]error{}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = d.Server.Serve(ctx, h)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return served{Driver: d, h: h}
}

func (s served) client(t *testing.T, node v1.ObjectName) nodeapi.Client {
	t.Helper()
	c, err := s.Connector.Dial(s.Identity(node))
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func testRequests(t *testing.T, s served) {
	ctx := context.Background()
	c := s.client(t, "vm1")

	snap, err := c.Snapshot(ctx, "e1")
	require.NoError(t, err)
	require.Equal(t, uint64(7), snap.Rev)
	require.Equal(t, "t1", snap.Timeline)
	require.Len(t, snap.Objects, 1)
	_, err = c.Snapshot(ctx, "")
	require.NoError(t, err)
	children, err := c.Children(ctx)
	require.NoError(t, err)
	require.Len(t, children, 1)
	require.NoError(t, c.Status(ctx, nodeapi.StatusWrite{Kind: v1.KindKVStore, Namespace: "e1", Name: "kv", UID: "u1",
		Status: json.RawMessage(`{"phase":"Ready"}`)}))
	require.NoError(t, c.PutObject(ctx, nodeapi.ObjectWrite{Kind: v1.KindConfigMap, Object: json.RawMessage(`{"metadata":{"name":"c"}}`)}))
	require.NoError(t, c.DeleteObject(ctx, nodeapi.ObjectRef{Kind: v1.KindJobRun, Namespace: "e1", Name: "run", UID: "u2"}))
	require.NoError(t, c.Report(ctx, nodeapi.NodeReport{BlobStore: "s3://node-blobs"}))
	key, err := c.Key(ctx, "e1")
	require.NoError(t, err)
	require.Equal(t, nodeapi.NamespaceKey{Namespace: "e1", Version: 1, Key: []byte("master")}, key)
	value := "v"
	answer, err := c.Secret(ctx, nodeapi.SecretWrite{Namespace: "e1", Workload: v1.WorkloadRef{Kind: v1.KindFunction, Name: "api"},
		Secret: "oauth", Key: "k", Value: &value})
	require.NoError(t, err)
	require.Equal(t, int64(3), answer.Generation)
	renewed, err := c.Renew(ctx, nodeapi.RenewRequest{CSR: []byte("csr")})
	require.NoError(t, err)
	require.Equal(t, []byte("cert"), renewed.Certificate)
	require.NoError(t, c.Renewed(ctx))

	require.Equal(t, []string{
		"Snapshot vm1 e1", "Snapshot vm1 ", "Children vm1", "Status vm1 KVStore e1/kv u1 {\"phase\":\"Ready\"}",
		"PutObject vm1 ConfigMap {\"metadata\":{\"name\":\"c\"}}", "DeleteObject vm1 JobRun e1/run u2", "Report vm1 s3://node-blobs",
		"Key vm1 e1", "Secret vm1 e1/oauth k=v", "Renew vm1 csr",
	}, s.h.requests())
	peers := s.h.peers()
	require.Len(t, peers, 11, "Admit lets each request in, the renewed certificate's first one too")
	p := peers[0]
	require.Equal(t, v1.ObjectName("vm1"), p.Node)
	require.NotEmpty(t, p.Serial)
	require.Equal(t, nodeapi.APIVersion, p.APIVersion)
	require.Equal(t, "test", p.Version)
	require.Equal(t, "test/contract", p.Platform)
}

func testErrorKinds(t *testing.T, s served) {
	ctx := context.Background()
	c := s.client(t, "vm1")
	s.h.fail("Status", fault.Conflictf("contract", "was created again"))
	s.h.fail("PutObject", fault.Forbiddenf("contract", "a person wrote it"))
	s.h.fail("Key", fault.NotFoundf("contract", "no such namespace"))
	s.h.fail("Report", fault.Unavailablef("contract", "away"))
	s.h.fail("Children", fault.Invalidf("contract", "bad"))

	err := c.Status(ctx, nodeapi.StatusWrite{Kind: v1.KindKVStore, Namespace: "e1", Name: "kv", UID: "u1", Status: json.RawMessage(`{}`)})
	require.Equal(t, fault.Conflict, fault.KindOf(err))
	require.ErrorContains(t, err, "was created again")
	err = c.PutObject(ctx, nodeapi.ObjectWrite{Kind: v1.KindConfigMap, Object: json.RawMessage(`{}`)})
	require.Equal(t, fault.Forbidden, fault.KindOf(err))
	_, err = c.Key(ctx, "e1")
	require.Equal(t, fault.NotFound, fault.KindOf(err))
	err = c.Report(ctx, nodeapi.NodeReport{})
	require.Equal(t, fault.Unavailable, fault.KindOf(err))
	_, err = c.Children(ctx)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
}

func testAdmitRefuses(t *testing.T, s served) {
	ctx := context.Background()
	s.h.mu.Lock()
	s.h.refuse["intruder"] = fault.Unauthorizedf("contract", "worker node intruder is not declared")
	s.h.mu.Unlock()
	err := s.client(t, "intruder").Status(ctx, nodeapi.StatusWrite{Kind: v1.KindKVStore, Namespace: "e1", Name: "kv", UID: "u1",
		Status: json.RawMessage(`{}`)})
	require.Equal(t, fault.Unauthorized, fault.KindOf(err))
	require.Empty(t, s.h.requests(), "a refused request reaches no method")
}

func testFeedWaits(t *testing.T, s served) {
	ctx := context.Background()
	c := s.client(t, "vm1")
	start := time.Now()
	f, err := c.Feed(ctx, 0, "t1", 300*time.Millisecond)
	require.NoError(t, err)
	require.Empty(t, f.Changes, "a feed with nothing to send ends empty")
	require.GreaterOrEqual(t, time.Since(start), 250*time.Millisecond, "after its wait")

	go func() {
		time.Sleep(200 * time.Millisecond)
		s.h.add(store.Change{Rev: 1, Type: store.Added, Kind: v1.KindConfigMap, Namespace: "e1", Name: "a"})
	}()
	start = time.Now()
	f, err = c.Feed(ctx, 0, "t1", 10*time.Second)
	require.NoError(t, err)
	require.Less(t, time.Since(start), 5*time.Second, "a change ends the wait")
	require.Len(t, f.Changes, 1)
	require.Equal(t, v1.ObjectName("a"), f.Changes[0].Name)
	require.Equal(t, uint64(1), f.Rev)
}

func testFeedGone(t *testing.T, s served) {
	s.h.mu.Lock()
	s.h.gone = true
	s.h.mu.Unlock()
	_, err := s.client(t, "vm1").Feed(context.Background(), 3, "t0", time.Second)
	require.Equal(t, fault.PreconditionFailed, fault.KindOf(err))
}

func testCalls(t *testing.T, s served) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	_, err := s.Server.Call(ctx, "vm9", nodeapi.Call{Kind: nodeapi.CallLogs, Namespace: "e9"})
	require.Equal(t, fault.Unavailable, fault.KindOf(err), "a node that does not poll is offline")
	require.Less(t, time.Since(start), time.Second, "and its call fails at once")

	c := s.client(t, "vm1")
	go func() {
		for ctx.Err() == nil {
			calls, err := c.Calls(ctx, time.Second)
			if err != nil {
				continue
			}
			for _, call := range calls {
				a := nodeapi.Answer{Lines: []logread.Line{{Body: "from vm1: " + call.Kind}}}
				if call.Kind == nodeapi.CallAppRetry {
					a = nodeapi.Answer{Error: "the App is running", Fault: fault.Conflict}
				}
				_ = c.Answer(ctx, call.ID, a)
			}
		}
	}()
	var a nodeapi.Answer
	require.Eventually(t, func() bool {
		a, err = s.Server.Call(ctx, "vm1", nodeapi.Call{Kind: nodeapi.CallLogs, Namespace: "e1"})
		return err == nil
	}, 10*time.Second, 20*time.Millisecond, "the call reaches the node once it polls")
	require.Equal(t, "from vm1: logs", a.Lines[0].Body)
	_, err = s.Server.Call(ctx, "vm1", nodeapi.Call{Kind: nodeapi.CallAppRetry, Namespace: "e1", Name: "shop"})
	require.Equal(t, fault.Conflict, fault.KindOf(err), "the node's error keeps its kind")
	require.ErrorContains(t, err, "the App is running")

	err = s.client(t, "vm2").Answer(ctx, "not-a-call-of-vm2", nodeapi.Answer{})
	require.Equal(t, fault.NotFound, fault.KindOf(err), "a node answers only its own calls")
}

func testJoin(t *testing.T, s served) {
	ctx := context.Background()
	in := nodeapi.JoinRequest{Token: "one-time", CSR: []byte("csr"), Version: "test", Platform: "test/contract"}
	_, _, err := s.Connector.Join(ctx, s.URL, func([]byte) bool { return false }, in)
	require.Error(t, err, "a control plane under no trusted CA")
	require.Empty(t, s.h.requests(), "never gets the token")

	out, caDER, err := s.Connector.Join(ctx, s.URL, func(der []byte) bool { return bytes.Equal(der, s.CA) }, in)
	require.NoError(t, err)
	require.Equal(t, []byte("cert"), out.Certificate)
	require.Equal(t, s.CA, caDER, "the join hands back the CA it trusted")
	require.Equal(t, []string{"Join one-time test test/contract"}, s.h.requests())

	s.h.fail("Join", fault.Forbiddenf("contract", "the join token expired"))
	_, _, err = s.Connector.Join(ctx, s.URL, func(der []byte) bool { return bytes.Equal(der, s.CA) }, in)
	require.ErrorContains(t, err, "the join token expired")
}

// handler is the suite's Handler: it records each request, answers with fixed values, and fails a method on demand.
type handler struct {
	mu      sync.Mutex
	got     []string
	admits  []nodeapi.Peer
	refuse  map[v1.ObjectName]error
	errs    map[string]error
	feed    []store.Change
	changed chan struct{}
	gone    bool
}

var _ nodeapi.Handler = (*handler)(nil)

func (h *handler) fail(method string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.errs[method] = err
}

func (h *handler) record(method string, parts ...string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	line := method
	for _, p := range parts {
		line += " " + p
	}
	h.got = append(h.got, line)
	return h.errs[method]
}

func (h *handler) requests() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.got...)
}

func (h *handler) peers() []nodeapi.Peer {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]nodeapi.Peer(nil), h.admits...)
}

func (h *handler) add(c store.Change) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.feed = append(h.feed, c)
	close(h.changed)
	h.changed = make(chan struct{})
}

func (h *handler) Join(_ context.Context, in nodeapi.JoinRequest) (nodeapi.JoinResponse, error) {
	if err := h.record("Join", in.Token, in.Version, in.Platform); err != nil {
		return nodeapi.JoinResponse{}, err
	}
	return nodeapi.JoinResponse{Certificate: []byte("cert"), CA: []byte("ca")}, nil
}

func (h *handler) Admit(_ context.Context, p nodeapi.Peer) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.refuse[p.Node]; err != nil {
		return err
	}
	h.admits = append(h.admits, p)
	return nil
}

func (h *handler) Renew(_ context.Context, node v1.ObjectName, in nodeapi.RenewRequest) (nodeapi.JoinResponse, error) {
	if err := h.record("Renew", string(node), string(in.CSR)); err != nil {
		return nodeapi.JoinResponse{}, err
	}
	return nodeapi.JoinResponse{Certificate: []byte("cert"), CA: []byte("ca")}, nil
}

func (h *handler) Snapshot(_ context.Context, node v1.ObjectName, ns v1.NamespaceName) (nodeapi.Snapshot, error) {
	if err := h.record("Snapshot", string(node), string(ns)); err != nil {
		return nodeapi.Snapshot{}, err
	}
	return nodeapi.Snapshot{Rev: 7, Timeline: "t1", Objects: []store.Change{{Rev: 7, Type: store.Added, Kind: v1.KindNamespace, Name: "e1"}}}, nil
}

func (h *handler) Changes(_ context.Context, _ v1.ObjectName, since uint64, _ string) (nodeapi.Feed, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.gone {
		return nodeapi.Feed{}, fault.PreconditionFailedf("contract", "the change log no longer holds revision %d", since)
	}
	out := nodeapi.Feed{Rev: uint64(len(h.feed))}
	for _, c := range h.feed {
		if c.Rev > since {
			out.Changes = append(out.Changes, c)
		}
	}
	return out, nil
}

func (h *handler) Changed() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.changed
}

func (h *handler) Children(_ context.Context, node v1.ObjectName) ([]store.Change, error) {
	if err := h.record("Children", string(node)); err != nil {
		return nil, err
	}
	return []store.Change{{Type: store.Added, Kind: v1.KindRevision, Namespace: "e1", Name: "fn-1"}}, nil
}

func (h *handler) Status(_ context.Context, node v1.ObjectName, in nodeapi.StatusWrite) error {
	return h.record("Status", string(node), string(in.Kind), string(in.Namespace)+"/"+string(in.Name), string(in.UID), string(in.Status))
}

func (h *handler) PutObject(_ context.Context, node v1.ObjectName, in nodeapi.ObjectWrite) error {
	return h.record("PutObject", string(node), string(in.Kind), string(in.Object))
}

func (h *handler) DeleteObject(_ context.Context, node v1.ObjectName, in nodeapi.ObjectRef) error {
	return h.record("DeleteObject", string(node), string(in.Kind), string(in.Namespace)+"/"+string(in.Name), string(in.UID))
}

func (h *handler) Report(_ context.Context, node v1.ObjectName, in nodeapi.NodeReport) error {
	return h.record("Report", string(node), in.BlobStore)
}

func (h *handler) Key(_ context.Context, node v1.ObjectName, ns v1.NamespaceName) (nodeapi.NamespaceKey, error) {
	if err := h.record("Key", string(node), string(ns)); err != nil {
		return nodeapi.NamespaceKey{}, err
	}
	return nodeapi.NamespaceKey{Namespace: ns, Version: 1, Key: []byte("master")}, nil
}

func (h *handler) Secret(_ context.Context, node v1.ObjectName, in nodeapi.SecretWrite) (nodeapi.SecretAnswer, error) {
	value := ""
	if in.Value != nil {
		value = *in.Value
	}
	if err := h.record("Secret", string(node), string(in.Namespace)+"/"+string(in.Secret), in.Key+"="+value); err != nil {
		return nodeapi.SecretAnswer{}, err
	}
	return nodeapi.SecretAnswer{Generation: 3}, nil
}
