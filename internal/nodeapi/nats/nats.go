// Package nats is the node API port's NATS driver (FEAT-0002): core NATS on a server the control plane embeds, over TLS 1.3
// with mTLS, and no JetStream. A node opens one connection. Its requests are NATS requests on subjects that name the node
// and its certificate's serial, which the server lets that certificate alone publish on; the control plane's calls are
// requests on a subject the node subscribes to, so a node that is not connected is offline at once (no responders). The
// feed is the same long poll as the HTTPS driver's, a request the control plane answers once a change comes, and the change
// log stays in the metastore. It is the production driver and the test driver alike.
package nats

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/nodeapi"
	"github.com/pyvvo/funcd/internal/store"
)

// The wire's pacing and bounds: an answer travels in chunks of chunkSize, a message holds maxPayload at most, a dead
// connection is dropped after maxPingsOut pings unanswered every pingInterval, within 3 s, as a long poll's node is offline
// 3 s after its last poll (the decider, 2026-11-27), and a request is served handlerTimeout at most, a feed's longest wait
// included.
const (
	chunkSize      = 512 << 10
	maxPayload     = 8 << 20
	pingInterval   = time.Second
	maxPingsOut    = 2
	readyTimeout   = 10 * time.Second
	handlerTimeout = nodeapi.MaxWait + 30*time.Second
)

// The subjects: a joined node's requests are funcd.node.<node>.<serial>.<request>, the control plane's calls to it
// funcd.call.<node>.<serial>, and a join funcd.join, whose answer goes to an inbox under joinInbox.
const (
	joinSubject = "funcd.join"
	joinInbox   = "_INBOX_join"
)

// The headers of a node's request (ADR-0374), and of each chunk of an answer.
const (
	headerAPIVersion = "Funcd-Node-Api"
	headerVersion    = "Funcd-Node-Version"
	headerPlatform   = "Funcd-Node-Platform"
	headerChunk      = "Funcd-Chunk"
	headerChunks     = "Funcd-Chunks"
)

// The requests of a joined node, the last token of their subject.
const (
	opRenew        = "renew"
	opRenewed      = "renewed"
	opSnapshot     = "snapshot"
	opFeed         = "feed"
	opChildren     = "children"
	opStatus       = "status"
	opPutObject    = "putObject"
	opDeleteObject = "deleteObject"
	opReport       = "report"
	opKey          = "key"
	opSecret       = "secret"
	opListen       = "listen"
)

// subjectToken is what a node's name and a certificate's serial in hex must look like to be one subject token.
var subjectToken = regexp.MustCompile(`^[a-z0-9-]+$`) //nolint:gochecknoglobals // a compiled constant

func nodeSubject(node v1.ObjectName, serial, op string) string {
	return "funcd.node." + string(node) + "." + serial + "." + op
}

func callSubject(node v1.ObjectName, serial string) string {
	return "funcd.call." + string(node) + "." + serial
}

func inbox(node v1.ObjectName, serial string) string { return "_INBOX_" + string(node) + "_" + serial }

// envelope is an answer: what a request returns, or the problem it met.
type envelope struct {
	OK    json.RawMessage `json:"ok,omitempty"`
	Error *fault.Problem  `json:"error,omitempty"`
}

type snapshotRequest struct {
	Namespace v1.NamespaceName `json:"namespace,omitempty"`
}

type feedRequest struct {
	Since    uint64        `json:"since"`
	Timeline string        `json:"timeline"`
	Wait     time.Duration `json:"wait"`
}

type keyRequest struct {
	Namespace v1.NamespaceName `json:"namespace"`
}

// Config configures the control plane's side.
type Config struct {
	Addr   string      // where the node API listens, host:port; port 0 picks a free one
	TLS    *tls.Config // the node API's certificate, and the CA that signs the nodes' (a client certificate is verified if given)
	Logger *slog.Logger

	ping time.Duration // pingInterval, unless a test sets a shorter one
}

type server struct {
	cfg  Config
	srv  *natsserver.Server
	nc   *nats.Conn
	once sync.Once
	done chan struct{}
	// life ends with Close, and every request's handler with it, so none waits out its feed on a closed server.
	life context.Context
	end  context.CancelFunc

	mu      sync.Mutex
	serials map[v1.ObjectName]string // the certificate serial of each node's last admitted request
}

// Listen starts the node API's NATS server on cfg.Addr, with the control plane's own connection to it.
func Listen(cfg Config) (nodeapi.Server, error) {
	const op = "nats.Listen"
	if cfg.TLS == nil {
		return nil, fault.Invalidf(op, "the node API's TLS is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	host, portText, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, op, "the node API's address %q", cfg.Addr)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, op, "the node API's port %q", portText)
	}
	if port == 0 {
		port = natsserver.RANDOM_PORT
	}
	secret, err := randomHex(32)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "draw the control plane's own credential")
	}
	if cfg.ping == 0 {
		cfg.ping = pingInterval
	}
	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "funcd-node-api", Host: host, Port: port, NoLog: true, NoSigs: true, MaxPayload: maxPayload,
		TLS: true, TLSConfig: cfg.TLS, TLSTimeout: 5, PingInterval: cfg.ping, MaxPingsOut: maxPingsOut,
		CustomClientAuthentication: auth{secret: secret},
	})
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "make the node API's NATS server")
	}
	go srv.Start()
	if !srv.ReadyForConnections(readyTimeout) {
		srv.Shutdown()
		return nil, fault.Internalf(op, "bind the node API on %s", cfg.Addr)
	}
	// The in-process connection still meets the server's TLS: it trusts the CA that signs the node API's certificate.
	own := &tls.Config{RootCAs: cfg.TLS.ClientCAs, ServerName: "localhost", MinVersion: tls.VersionTLS13}
	nc, err := nats.Connect(srv.ClientURL(), nats.InProcessServer(srv), nats.Secure(own), nats.Token(secret),
		nats.Name("funcd control plane"))
	if err != nil {
		srv.Shutdown()
		return nil, fault.Wrapf(err, fault.Internal, op, "connect the control plane to its node API")
	}
	life, end := context.WithCancel(context.Background())
	return &server{cfg: cfg, srv: srv, nc: nc, done: make(chan struct{}), life: life, end: end,
		serials: map[v1.ObjectName]string{}}, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *server) Addr() string { return s.srv.Addr().String() }

// Serve answers the nodes' requests and joins with h until ctx ends or the server closes.
func (s *server) Serve(ctx context.Context, h nodeapi.Handler) error {
	const op = "nats.Serve"
	reqs, err := s.nc.Subscribe("funcd.node.*.*.*", func(m *nats.Msg) { go s.request(h, m) })
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "subscribe to the nodes' requests")
	}
	joins, err := s.nc.Subscribe(joinSubject, func(m *nats.Msg) { go s.join(h, m) })
	if err != nil {
		_ = reqs.Unsubscribe()
		return fault.Wrapf(err, fault.Internal, op, "subscribe to the joins")
	}
	if err := s.nc.Flush(); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "subscribe")
	}
	select {
	case <-ctx.Done():
	case <-s.done:
	}
	_ = reqs.Unsubscribe()
	_ = joins.Unsubscribe()
	return s.Close()
}

// Close drops every connection, and the server.
func (s *server) Close() error {
	s.once.Do(func() {
		s.end()
		close(s.done)
		s.nc.Close()
		s.srv.Shutdown()
	})
	return nil
}

// Call sends c to node over the subject of the certificate its last admitted request came with: no subscriber there is a
// node that is offline, at once.
func (s *server) Call(ctx context.Context, node v1.ObjectName, c nodeapi.Call) (nodeapi.Answer, error) {
	const op = "nats.call"
	offline := func() error {
		if c.Namespace == "" {
			return fault.Unavailablef(op, "worker node %s is offline", node)
		}
		return fault.Unavailablef(op, "worker node %s of namespace %s is offline", node, c.Namespace)
	}
	s.mu.Lock()
	serial := s.serials[node]
	s.mu.Unlock()
	if serial == "" {
		return nodeapi.Answer{}, offline()
	}
	id, err := randomHex(8)
	if err != nil {
		return nodeapi.Answer{}, fault.Wrapf(err, fault.Internal, op, "draw a call id")
	}
	c.ID = id
	data, err := json.Marshal(c)
	if err != nil {
		return nodeapi.Answer{}, fault.Wrapf(err, fault.Internal, op, "marshal the call")
	}
	callCtx, cancel := context.WithTimeout(ctx, nodeapi.CallTimeout)
	defer cancel()
	m, err := s.nc.RequestWithContext(callCtx, callSubject(node, serial), data)
	switch {
	case errors.Is(err, nats.ErrNoResponders):
		return nodeapi.Answer{}, offline()
	case err != nil && ctx.Err() != nil:
		return nodeapi.Answer{}, ctx.Err()
	case err != nil:
		return nodeapi.Answer{}, fault.Wrapf(err, fault.Unavailable, op, "worker node %s did not answer within %s", node, nodeapi.CallTimeout)
	}
	var a nodeapi.Answer
	if err := json.Unmarshal(m.Data, &a); err != nil {
		return nodeapi.Answer{}, fault.Wrapf(err, fault.Internal, op, "decode worker node %s's answer", node)
	}
	if err := nodeapi.AnswerError(op, node, a); err != nil {
		return nodeapi.Answer{}, err
	}
	return a, nil
}

// request serves one request of a joined node, the one its subject names, as the node its subject names: only that node's
// certificate may publish there (auth). The control plane answers on its own connection, which may publish anywhere, so
// it answers only into the inbox of the node that asked, and drops a request that names another reply subject.
func (s *server) request(h nodeapi.Handler, m *nats.Msg) {
	parts := strings.Split(m.Subject, ".")
	if len(parts) != 5 || !strings.HasPrefix(m.Reply, inbox(v1.ObjectName(parts[2]), parts[3])+".") {
		s.cfg.Logger.Warn("node API: a request whose answer would leave its node's inbox is dropped", "subject", m.Subject,
			"reply", m.Reply)
		return
	}
	ctx, cancel := context.WithTimeout(s.life, handlerTimeout)
	defer cancel()
	out, err := s.serve(ctx, h, m, v1.ObjectName(parts[2]), parts[3], parts[4])
	s.reply(m, out, err)
}

func (s *server) serve(ctx context.Context, h nodeapi.Handler, m *nats.Msg, node v1.ObjectName, serial, name string) (json.RawMessage, error) {
	const op = "nats.request"
	version, err := strconv.Atoi(m.Header.Get(headerAPIVersion))
	if err != nil || !nodeapi.Supported(version) {
		return nil, fault.Conflictf(op, "this control plane serves node API versions %s; upgrade or downgrade the node", versions())
	}
	p := nodeapi.Peer{Node: node, Serial: serial, APIVersion: version, Version: m.Header.Get(headerVersion),
		Platform: m.Header.Get(headerPlatform)}
	if err := h.Admit(ctx, p); err != nil {
		if fault.KindOf(err) == fault.Unauthorized { // a certificate the control plane no longer takes gets no call (ADR-0372 Decision 2)
			s.mu.Lock()
			if s.serials[node] == serial {
				delete(s.serials, node)
			}
			s.mu.Unlock()
		}
		return nil, err
	}
	s.mu.Lock()
	s.serials[node] = serial
	s.mu.Unlock()
	return s.dispatch(ctx, h, node, name, m.Data)
}

//nolint:gocyclo // one case per request of the node API, each a decode, a Handler call and an encode
func (s *server) dispatch(ctx context.Context, h nodeapi.Handler, node v1.ObjectName, name string, data []byte) (json.RawMessage, error) {
	switch name {
	case opListen, opRenewed:
		return nil, nil
	case opRenew:
		var in nodeapi.RenewRequest
		if err := decode(data, nodeapi.MaxJoinBody, &in); err != nil {
			return nil, err
		}
		return encode(h.Renew(ctx, node, in))
	case opSnapshot:
		var in snapshotRequest
		if err := decode(data, nodeapi.MaxJoinBody, &in); err != nil {
			return nil, err
		}
		return encode(h.Snapshot(ctx, node, in.Namespace))
	case opFeed:
		var in feedRequest
		if err := decode(data, nodeapi.MaxJoinBody, &in); err != nil {
			return nil, err
		}
		return encode(nodeapi.WaitForChanges(ctx, h, node, in.Since, in.Timeline, in.Wait))
	case opChildren:
		out, err := h.Children(ctx, node)
		if out == nil {
			out = []store.Change{}
		}
		return encode(out, err)
	case opStatus:
		var in nodeapi.StatusWrite
		if err := decode(data, nodeapi.MaxStatusBody, &in); err != nil {
			return nil, err
		}
		return nil, h.Status(ctx, node, in)
	case opPutObject:
		var in nodeapi.ObjectWrite
		if err := decode(data, nodeapi.MaxObjectBody, &in); err != nil {
			return nil, err
		}
		return nil, h.PutObject(ctx, node, in)
	case opDeleteObject:
		var in nodeapi.ObjectRef
		if err := decode(data, nodeapi.MaxJoinBody, &in); err != nil {
			return nil, err
		}
		return nil, h.DeleteObject(ctx, node, in)
	case opReport:
		var in nodeapi.NodeReport
		if err := decode(data, nodeapi.MaxStatusBody, &in); err != nil {
			return nil, err
		}
		return nil, h.Report(ctx, node, in)
	case opKey:
		var in keyRequest
		if err := decode(data, nodeapi.MaxJoinBody, &in); err != nil {
			return nil, err
		}
		return encode(h.Key(ctx, node, in.Namespace))
	case opSecret:
		var in nodeapi.SecretWrite
		if err := decode(data, nodeapi.MaxObjectBody, &in); err != nil {
			return nil, err
		}
		return encode(h.Secret(ctx, node, in))
	}
	return nil, fault.NotFoundf("nats.request", "no request %q", name)
}

// join swaps a one-time token for a node certificate: the one request of a connection with no certificate.
func (s *server) join(h nodeapi.Handler, m *nats.Msg) {
	if !strings.HasPrefix(m.Reply, joinInbox+".") {
		s.cfg.Logger.Warn("node API: a join whose answer would leave the joins' inbox is dropped", "reply", m.Reply)
		return
	}
	ctx, cancel := context.WithTimeout(s.life, handlerTimeout)
	defer cancel()
	var in nodeapi.JoinRequest
	if err := decode(m.Data, nodeapi.MaxJoinBody, &in); err != nil {
		s.reply(m, nil, err)
		return
	}
	out, err := encode(h.Join(ctx, in))
	s.reply(m, out, err)
}

// decode reads a request's body into v, one of the node API's typed requests, refusing one over limit bytes.
//
//nolint:forbidigo // v is one of the node API's typed requests
func decode(data []byte, limit int, v any) error {
	const op = "nats.decode"
	if len(data) > limit {
		return fault.PayloadTooLargef(op, "the request holds %d bytes, over its %d", len(data), limit)
	}
	if len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fault.Wrapf(err, fault.Invalid, op, "decode the request")
	}
	return nil
}

// encode is v, one of the node API's typed answers, in JSON, unless err.
//
//nolint:forbidigo // v is one of the node API's typed answers
func encode[T any](v T, err error) (json.RawMessage, error) {
	if err != nil {
		return nil, err
	}
	b, merr := json.Marshal(v)
	if merr != nil {
		return nil, fault.Wrapf(merr, fault.Internal, "nats.encode", "marshal the answer")
	}
	return b, nil
}

// reply answers m with out or err, in chunks of chunkSize. The owner of the request logs once a failure whose problem
// leaves part of it out (ADR-0002 §3), as fault.LogCauses does for the HTTPS driver.
func (s *server) reply(m *nats.Msg, out json.RawMessage, err error) {
	if m.Reply == "" {
		return
	}
	env := envelope{OK: out}
	if err != nil {
		p := fault.ToProblem(err)
		env = envelope{Error: &p}
		if p.Detail != err.Error() {
			level := slog.LevelWarn
			if p.Status >= 500 {
				level = slog.LevelError
			}
			s.cfg.Logger.Log(context.Background(), level, "node API request failed", "subject", m.Subject, "error", err)
		}
	}
	data, merr := json.Marshal(env)
	if merr != nil {
		data, _ = json.Marshal(envelope{Error: &fault.Problem{Type: "about:blank", Status: 500, Detail: "internal error"}})
	}
	n := max(1, (len(data)+chunkSize-1)/chunkSize)
	for i := range n {
		chunk := nats.NewMsg(m.Reply)
		chunk.Header.Set(headerChunk, strconv.Itoa(i))
		chunk.Header.Set(headerChunks, strconv.Itoa(n))
		chunk.Data = data[i*chunkSize : min(len(data), (i+1)*chunkSize)]
		if err := s.nc.PublishMsg(chunk); err != nil {
			s.cfg.Logger.Warn("node API: answer a request", "subject", m.Subject, "error", err)
			return
		}
	}
}

func versions() string {
	out := make([]string, 0, len(nodeapi.SupportedAPIVersions))
	for _, v := range nodeapi.SupportedAPIVersions {
		out = append(out, strconv.Itoa(v))
	}
	return strings.Join(out, ", ")
}

// auth lets in the control plane's own connection by its credential, a connection with a node certificate the CA verified
// as that node, publishing only on the subjects of its name and certificate serial, and one with no certificate as a join,
// publishing on the join's subject alone.
type auth struct{ secret string }

func (a auth) Check(c natsserver.ClientAuthentication) bool {
	if c.Kind() != natsserver.CLIENT {
		return false
	}
	if o := c.GetOpts(); o != nil && o.Token != "" {
		if subtle.ConstantTimeCompare([]byte(o.Token), []byte(a.secret)) != 1 {
			return false
		}
		c.RegisterUser(&natsserver.User{Username: "funcd"})
		return true
	}
	cs := c.GetTLSConnectionState()
	if cs == nil {
		return false
	}
	if len(cs.VerifiedChains) == 0 {
		c.RegisterUser(&natsserver.User{Username: "join", Permissions: &natsserver.Permissions{
			Publish:   &natsserver.SubjectPermission{Allow: []string{joinSubject}},
			Subscribe: &natsserver.SubjectPermission{Allow: []string{joinInbox + ".>"}},
		}})
		return true
	}
	leaf := cs.PeerCertificates[0]
	node, serial := v1.ObjectName(leaf.Subject.CommonName), leaf.SerialNumber.Text(16)
	if !subjectToken.MatchString(string(node)) || !subjectToken.MatchString(serial) {
		return false
	}
	c.RegisterUser(&natsserver.User{Username: string(node) + "/" + serial, Permissions: &natsserver.Permissions{
		Publish:   &natsserver.SubjectPermission{Allow: []string{nodeSubject(node, serial, ">")}},
		Subscribe: &natsserver.SubjectPermission{Allow: []string{inbox(node, serial) + ".>", callSubject(node, serial)}},
		Response:  &natsserver.ResponsePermission{MaxMsgs: 1, Expires: nodeapi.CallTimeout},
	}})
	return true
}
