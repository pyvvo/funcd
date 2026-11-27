package multinode

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/nodeapi"
	"github.com/pyvvo/funcd/internal/nodeapi/longpoll"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// clock is a time the test moves.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// joinRig is a control plane on the test's clock with WorkerNode vm1 declared.
func joinRig(t *testing.T) (*Cluster, *clock) {
	t.Helper()
	clk := &clock{now: time.Now()}
	st := store.New(memory.New(), store.WithChangeLog(0))
	_, err := st.Create(context.Background(), workerNode("vm1"))
	require.NoError(t, err)
	return clusterOn(t, st, clk), clk
}

// clusterOn is a control plane over st, on clk.
func clusterOn(t *testing.T, st store.Store, clk *clock) *Cluster {
	t.Helper()
	ca, err := LoadOrCreateCA(t.TempDir(), clk.Now())
	require.NoError(t, err)
	tlsCfg, err := ca.ServerTLS(nil, time.Now)
	require.NoError(t, err)
	api, err := longpoll.Listen(longpoll.Config{Addr: "127.0.0.1:0", TLS: tlsCfg})
	require.NoError(t, err)
	c, err := New(Config{Store: st, CA: ca, API: api, LocalNode: "local", LocalRuns: true, Now: clk.Now})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// csrFor is a certificate request for node and its key.
func csrFor(t *testing.T, node v1.ObjectName) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: string(node)}}, key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), key
}

// join swaps tok for a certificate of vm1, and is its serial.
func join(t *testing.T, c *Cluster, tok string) (string, error) {
	t.Helper()
	csr, _ := csrFor(t, "vm1")
	resp, err := c.handler.Join(context.Background(), nodeapi.JoinRequest{Token: tok, CSR: csr, Version: "test"})
	if err != nil {
		return "", err
	}
	return serialOf(t, resp.Certificate), nil
}

func serialOf(t *testing.T, certPEM []byte) string {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	require.NotNil(t, block)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	return cert.SerialNumber.Text(16)
}

func mint(t *testing.T, c *Cluster) string {
	t.Helper()
	tok, _, err := c.MintJoinToken(context.Background(), "vm1", time.Minute)
	require.NoError(t, err)
	return tok
}

// vm1 is a request of node vm1 with the certificate of serial.
func vm1(serial string) nodeapi.Peer {
	return nodeapi.Peer{Node: "vm1", Serial: serial, APIVersion: nodeapi.APIVersion, Version: "test"}
}

// renew is a renewed certificate of vm1, and is its serial.
func renew(t *testing.T, c *Cluster) string {
	t.Helper()
	csr, _ := csrFor(t, "vm1")
	resp, err := c.handler.Renew(context.Background(), "vm1", nodeapi.RenewRequest{CSR: csr})
	require.NoError(t, err)
	return serialOf(t, resp.Certificate)
}

// recreating is a control plane's store that deletes the object named name and makes it again under the name, with
// another uid, just before its first delete of it, as a person who deletes and applies it again at that moment would.
type recreating struct {
	store.Store
	store.ChangeLog
	name v1.ObjectName
	done bool
}

func (r *recreating) Delete(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName, rv string) error {
	if name == r.name && !r.done {
		r.done = true
		obj, err := r.Store.Get(ctx, gvk, ns, name)
		if err != nil {
			return err
		}
		if err := r.Store.Delete(ctx, gvk, ns, name, ""); err != nil {
			return err
		}
		m := obj.GetObjectMeta()
		m.UID, m.ResourceVersion = "", ""
		if _, err := r.Store.Create(ctx, obj); err != nil {
			return err
		}
	}
	return r.Store.Delete(ctx, gvk, ns, name, rv)
}

func TestANodesDeleteNeverTakesAnObjectMadeAgainUnderTheName(t *testing.T) {
	ctx := context.Background()
	clk := &clock{now: time.Now()}
	st := store.New(memory.New(), store.WithChangeLog(0))
	_, err := st.Create(ctx, workerNode("vm1"))
	require.NoError(t, err)
	_, err = st.Create(ctx, namespace("e1", "vm1"))
	require.NoError(t, err)
	run := &v1.JobRun{TypeMeta: tm(v1.KindJobRun)}
	run.Name, run.Namespace, run.ResourceGroup, run.Spec.Job = "r1", "e1", "rg1", "report"
	made, err := st.Create(ctx, run)
	require.NoError(t, err)
	c := clusterOn(t, &recreating{Store: st, ChangeLog: st.(store.ChangeLog), name: "r1"}, clk)
	ref := nodeapi.ObjectRef{Kind: v1.KindJobRun, Namespace: "e1", Name: "r1", UID: made.GetObjectMeta().UID}
	require.NoError(t, c.handler.DeleteObject(ctx, "vm1", ref))
	again, err := st.Get(ctx, v1.KindJobRun.GVK(), "e1", "r1")
	require.NoError(t, err, "the run made again under the name between the read and the delete stays")
	require.NotEqual(t, made.GetObjectMeta().UID, again.GetObjectMeta().UID)
}

// oracle: docs/design/multinode/join.fizz#NoJoinWithATokenPastItsEnd
func TestAJoinTokenPastItsEndIsRefused(t *testing.T) {
	c, clk := joinRig(t)
	tok := mint(t, c)
	clk.Add(2 * time.Minute)
	_, err := join(t, c, tok)
	require.Equal(t, fault.Forbidden, fault.KindOf(err))
	require.ErrorContains(t, err, "expired")
}

// oracle: docs/design/multinode/join.fizz#NoJoinWithAReplacedToken
func TestANewJoinTokenEndsTheUnusedOne(t *testing.T) {
	c, _ := joinRig(t)
	first := mint(t, c)
	second := mint(t, c)
	_, err := join(t, c, first)
	require.Equal(t, fault.Forbidden, fault.KindOf(err))
	require.ErrorContains(t, err, "does not match")
	_, err = join(t, c, second)
	require.NoError(t, err)
}

// oracle: docs/design/multinode/join.fizz#NoJoinOverAPollingNode
func TestAJoinUnderTheNameOfAPollingNodeIsRefused(t *testing.T) {
	c, clk := joinRig(t)
	_, err := join(t, c, mint(t, c))
	require.NoError(t, err)
	c.liveness.Seen("vm1", "test", nodeapi.APIVersion, "linux/arm64")
	again := mint(t, c)
	_, err = join(t, c, again)
	require.Equal(t, fault.Conflict, fault.KindOf(err))
	require.ErrorContains(t, err, "a running node holds the name")
	clk.Add(defaultReadyFor)
	_, err = join(t, c, again)
	require.NoError(t, err, "a node that stopped polling frees its name")
}

// oracle: docs/design/multinode/join.fizz#NoRequestRefusedMidRenewal
// oracle: docs/design/multinode/join.fizz#TheOldCertificateEndsAtTheNewOnesFirstRequest
func TestARenewedCertificateTakesOverAtItsFirstRequest(t *testing.T) {
	ctx := context.Background()
	c, _ := joinRig(t)
	old, err := join(t, c, mint(t, c))
	require.NoError(t, err)
	require.NoError(t, c.handler.Admit(ctx, vm1(old)))
	renewed := renew(t, c)
	require.NoError(t, c.handler.Admit(ctx, vm1(old)), "a request with the old certificate passes until the new one's first")
	require.NoError(t, c.handler.Admit(ctx, vm1(renewed)))
	require.Equal(t, fault.Unauthorized, fault.KindOf(c.handler.Admit(ctx, vm1(old))), "the new one's first request ends the old one")
	require.NoError(t, c.handler.Admit(ctx, vm1(renewed)))
}

func TestALostRenewAnswerCostsNothing(t *testing.T) {
	ctx := context.Background()
	c, _ := joinRig(t)
	old, err := join(t, c, mint(t, c))
	require.NoError(t, err)
	lost := renew(t, c)
	again := renew(t, c)
	require.NoError(t, c.handler.Admit(ctx, vm1(old)), "a node no renewal reached keeps its certificate")
	require.Equal(t, fault.Unauthorized, fault.KindOf(c.handler.Admit(ctx, vm1(lost))), "the next renewal replaces the lost one")
	require.NoError(t, c.handler.Admit(ctx, vm1(again)))
}

// oracle: docs/design/multinode/join.fizz#NoRequestWithACertificateAJoinReplaced
func TestARejoinEndsTheOldCertificates(t *testing.T) {
	ctx := context.Background()
	c, _ := joinRig(t)
	old, err := join(t, c, mint(t, c))
	require.NoError(t, err)
	pending := renew(t, c)
	fresh, err := join(t, c, mint(t, c))
	require.NoError(t, err)
	require.Equal(t, fault.Unauthorized, fault.KindOf(c.handler.Admit(ctx, vm1(old))), "the joined certificate ends")
	require.Equal(t, fault.Unauthorized, fault.KindOf(c.handler.Admit(ctx, vm1(pending))), "and so does the one it renewed")
	require.NoError(t, c.handler.Admit(ctx, vm1(fresh)))
}

// oracle: docs/design/multinode/join.fizz#NoRequestWithACertificatePastItsEnd
func TestANodeCertificatePastItsEndIsRefusedOnAnOpenLink(t *testing.T) {
	ctx := context.Background()
	c, clk := joinRig(t)
	serial, err := join(t, c, mint(t, c))
	require.NoError(t, err)
	require.NoError(t, c.handler.Admit(ctx, vm1(serial)))
	clk.Add(certLifetime + time.Minute)
	err = c.handler.Admit(ctx, vm1(serial))
	require.Equal(t, fault.Unauthorized, fault.KindOf(err), "a link opened before the end checked nothing since")
	require.ErrorContains(t, err, "ended")
}

// oracle: docs/design/multinode/join.fizz#NoRequestWithACertificatePastItsEnd
func TestANodeCertificatePastItsEndIsRefused(t *testing.T) {
	now := time.Now()
	ca, err := LoadOrCreateCA(t.TempDir(), now)
	require.NoError(t, err)
	csr, key := csrFor(t, "vm1")
	certPEM, _, notAfter, err := ca.SignNode(csr, "vm1", now.Add(-certLifetime-time.Hour))
	require.NoError(t, err)
	require.True(t, notAfter.Before(now))
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	pair, err := tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	require.NoError(t, err)
	serverCfg, err := ca.ServerTLS(nil, time.Now)
	require.NoError(t, err)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", serverCfg)
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	refused := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			refused <- err
			return
		}
		defer func() { _ = conn.Close() }()
		refused <- conn.(*tls.Conn).Handshake()
	}()
	conn, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	client := tls.Client(conn, &tls.Config{Certificates: []tls.Certificate{pair}, RootCAs: ca.Pool(), ServerName: "localhost",
		MinVersion: tls.VersionTLS13})
	_ = client.Handshake()
	_, _ = client.Read(make([]byte, 1))
	err = <-refused
	require.Error(t, err)
	require.ErrorContains(t, err, "expired")
}
