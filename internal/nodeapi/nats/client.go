package nats

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/nodeapi"
	"github.com/pyvvo/funcd/internal/store"
)

// The node side's bounds: requestTimeout bounds a request but a feed, which gets its wait and feedMargin, and a
// listen, which the control plane answers at once: listenTimeout frees the call loop of one sent on a link that went
// silent. A lost connection is dialed again every reconnectWait; callBuffer calls wait for the node at most. An answer's
// subscription queues syncQueue chunks, 64 MiB, the server's own bound on what a client leaves unread; nats.go's default
// of 65,536 is a 512 KiB channel for each request in flight, so 512 KiB a node with its feed open.
const (
	requestTimeout     = time.Minute
	feedMargin         = 30 * time.Second
	listenTimeout      = 5 * time.Second
	joinRequestTimeout = 30 * time.Second
	reconnectWait      = 2 * time.Second
	callBuffer         = 256
	syncQueue          = 128
)

// scheme is the scheme of a control plane's URL on this driver: nats://host:port.
const scheme = "nats"

type connector struct{}

// NewConnector is the node side of the NATS driver.
func NewConnector() nodeapi.Connector { return connector{} }

// address is the host:port of a nats:// URL.
func address(op, server string) (string, error) {
	u, err := url.Parse(server)
	if err != nil || u.Scheme != scheme || u.Host == "" {
		return "", fault.Invalidf(op, "the control plane's node API is a nats:// URL, not %q", server)
	}
	return u.Host, nil
}

func (connector) Join(ctx context.Context, server string, trusted func(caDER []byte) bool, in nodeapi.JoinRequest) (nodeapi.JoinResponse, []byte, error) {
	const op = "nats.Join"
	host, err := address(op, server)
	if err != nil {
		return nodeapi.JoinResponse{}, nil, err
	}
	u, _ := url.Parse(server)
	pin, pinned := nodeapi.PinnedTLS(u.Hostname(), trusted)
	nc, err := nats.Connect("tls://"+host, nats.Secure(pin), nats.CustomInboxPrefix(joinInbox), nats.Name("funcd node join"),
		nats.Timeout(joinRequestTimeout), nats.SyncQueueLen(syncQueue))
	if err != nil {
		return nodeapi.JoinResponse{}, nil, fault.Wrapf(err, fault.Unavailable, op, "reach the control plane at %s", server)
	}
	defer nc.Close()
	data, err := json.Marshal(in)
	if err != nil {
		return nodeapi.JoinResponse{}, nil, fault.Wrapf(err, fault.Internal, op, "marshal the join")
	}
	msg := nats.NewMsg(joinSubject)
	msg.Data = data
	answer, err := request(ctx, nc, msg, joinRequestTimeout)
	if err != nil {
		if fault.KindOf(err) == fault.Unavailable {
			return nodeapi.JoinResponse{}, nil, err
		}
		return nodeapi.JoinResponse{}, nil, fault.Wrapf(err, fault.Forbidden, op, "the control plane refused the join")
	}
	var out nodeapi.JoinResponse
	if err := json.Unmarshal(answer, &out); err != nil {
		return nodeapi.JoinResponse{}, nil, fault.Wrapf(err, fault.Internal, op, "decode the join answer")
	}
	return out, pinned(), nil
}

func (connector) Dial(id nodeapi.Identity) (nodeapi.Client, error) {
	const op = "nats.Dial"
	host, err := address(op, id.Server)
	if err != nil {
		return nil, err
	}
	if id.Roots == nil || len(id.Certificate.Certificate) == 0 {
		return nil, fault.Invalidf(op, "the cluster CA and a node certificate are required")
	}
	leaf, err := x509.ParseCertificate(id.Certificate.Certificate[0])
	if err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, op, "parse the node certificate")
	}
	node, serial := v1.ObjectName(leaf.Subject.CommonName), leaf.SerialNumber.Text(16)
	nc, err := nats.Connect("tls://"+host,
		nats.Secure(&tls.Config{Certificates: []tls.Certificate{id.Certificate}, RootCAs: id.Roots, MinVersion: tls.VersionTLS13}),
		nats.CustomInboxPrefix(inbox(node, serial)), nats.Name("funcd node "+string(node)), nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1), nats.ReconnectWait(reconnectWait), nats.PingInterval(pingInterval),
		nats.MaxPingsOutstanding(maxPingsOut), nats.SyncQueueLen(syncQueue))
	if err != nil {
		return nil, fault.Wrapf(err, fault.Unavailable, op, "reach the control plane at %s", id.Server)
	}
	c := &client{id: id, nc: nc, node: node, serial: serial, calls: make(chan *nats.Msg, callBuffer), pending: map[string]pending{}}
	if _, err := nc.ChanSubscribe(callSubject(node, serial), c.calls); err != nil {
		nc.Close()
		return nil, fault.Wrapf(err, fault.Internal, op, "subscribe to the control plane's calls")
	}
	return c, nil
}

type client struct {
	id     nodeapi.Identity
	nc     *nats.Conn
	node   v1.ObjectName
	serial string
	calls  chan *nats.Msg

	mu      sync.Mutex
	pending map[string]pending // the calls taken and not answered yet, by id
}

type pending struct {
	msg   *nats.Msg
	taken time.Time
}

func (c *client) Snapshot(ctx context.Context, ns v1.NamespaceName) (nodeapi.Snapshot, error) {
	var out nodeapi.Snapshot
	return out, c.do(ctx, opSnapshot, snapshotRequest{Namespace: ns}, &out, requestTimeout)
}

func (c *client) Feed(ctx context.Context, since uint64, timeline string, wait time.Duration) (nodeapi.Feed, error) {
	var out nodeapi.Feed
	return out, c.do(ctx, opFeed, feedRequest{Since: since, Timeline: timeline, Wait: wait}, &out, nodeapi.Wait(wait)+feedMargin)
}

func (c *client) Children(ctx context.Context) ([]store.Change, error) {
	var out []store.Change
	return out, c.do(ctx, opChildren, nil, &out, requestTimeout)
}

func (c *client) Status(ctx context.Context, in nodeapi.StatusWrite) error {
	return c.do(ctx, opStatus, in, nil, requestTimeout)
}

func (c *client) PutObject(ctx context.Context, in nodeapi.ObjectWrite) error {
	return c.do(ctx, opPutObject, in, nil, requestTimeout)
}

func (c *client) DeleteObject(ctx context.Context, in nodeapi.ObjectRef) error {
	return c.do(ctx, opDeleteObject, in, nil, requestTimeout)
}

func (c *client) Report(ctx context.Context, in nodeapi.NodeReport) error {
	return c.do(ctx, opReport, in, nil, requestTimeout)
}

func (c *client) Key(ctx context.Context, ns v1.NamespaceName) (nodeapi.NamespaceKey, error) {
	var out nodeapi.NamespaceKey
	return out, c.do(ctx, opKey, keyRequest{Namespace: ns}, &out, requestTimeout)
}

func (c *client) Secret(ctx context.Context, in nodeapi.SecretWrite) (nodeapi.SecretAnswer, error) {
	var out nodeapi.SecretAnswer
	return out, c.do(ctx, opSecret, in, &out, requestTimeout)
}

func (c *client) Renew(ctx context.Context, in nodeapi.RenewRequest) (nodeapi.JoinResponse, error) {
	var out nodeapi.JoinResponse
	return out, c.do(ctx, opRenew, in, &out, requestTimeout)
}

func (c *client) Renewed(ctx context.Context) error {
	return c.do(ctx, opRenewed, nil, nil, requestTimeout)
}

// Calls tells the control plane that the node takes calls under its certificate, then waits for the calls it sends.
func (c *client) Calls(ctx context.Context, wait time.Duration) ([]nodeapi.Call, error) {
	if err := c.do(ctx, opListen, nil, nil, listenTimeout); err != nil {
		return nil, err
	}
	c.forget()
	t := time.NewTimer(nodeapi.Wait(wait))
	defer t.Stop()
	var msgs []*nats.Msg
	select {
	case m := <-c.calls:
		msgs = append(msgs, m)
	case <-t.C:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	for more := true; more; {
		select {
		case m := <-c.calls:
			msgs = append(msgs, m)
		default:
			more = false
		}
	}
	out := make([]nodeapi.Call, 0, len(msgs))
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, m := range msgs {
		var call nodeapi.Call
		if json.Unmarshal(m.Data, &call) != nil || call.ID == "" {
			continue
		}
		c.pending[call.ID] = pending{msg: m, taken: time.Now()}
		out = append(out, call)
	}
	return out, nil
}

// forget drops the calls taken longer ago than a call's timeout: their callers have gone.
func (c *client) forget() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, p := range c.pending {
		if time.Since(p.taken) > nodeapi.CallTimeout {
			delete(c.pending, id)
		}
	}
}

// Answer answers the call id, one this node took.
func (c *client) Answer(_ context.Context, id string, a nodeapi.Answer) error {
	const op = "nats.Answer"
	c.mu.Lock()
	p, ok := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if !ok {
		return fault.NotFoundf(op, "worker node %s has no call %s waiting", c.node, id)
	}
	data, err := json.Marshal(a)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "marshal the answer")
	}
	if err := p.msg.Respond(data); err != nil {
		return fault.Wrapf(err, fault.Unavailable, op, "answer call %s", id)
	}
	return nil
}

func (c *client) Close() error {
	c.nc.Close()
	return nil
}

// do sends one request of the node API and decodes its answer into out: an error of the kind the control plane answered.
//
//nolint:forbidigo // body and out are the node API's typed requests and answers
func (c *client) do(ctx context.Context, op string, body, out any, timeout time.Duration) error {
	msg := nats.NewMsg(nodeSubject(c.node, c.serial, op))
	msg.Header.Set(headerAPIVersion, strconv.Itoa(nodeapi.APIVersion))
	msg.Header.Set(headerVersion, c.id.Version)
	msg.Header.Set(headerPlatform, c.id.Platform)
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fault.Wrapf(err, fault.Internal, "nats.request", "marshal the %s", op)
		}
		msg.Data = data
	}
	answer, err := request(ctx, c.nc, msg, timeout)
	if err != nil || out == nil || len(answer) == 0 {
		return err
	}
	if err := json.Unmarshal(answer, out); err != nil {
		return fault.Wrapf(err, fault.Internal, "nats.request", "decode the answer of %s", op)
	}
	return nil
}

// request sends msg and gathers the chunks of its answer: the answer's body, or the problem the control plane met with its
// kind; fault.Unavailable when no control plane listens, or none answers within timeout.
func request(ctx context.Context, nc *nats.Conn, msg *nats.Msg, timeout time.Duration) (json.RawMessage, error) {
	const op = "nats.request"
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	reply := nc.NewInbox()
	sub, err := nc.SubscribeSync(reply)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Unavailable, op, "wait for the answer of %s", msg.Subject)
	}
	defer func() { _ = sub.Unsubscribe() }()
	msg.Reply = reply
	if err := nc.PublishMsg(msg); err != nil {
		return nil, fault.Wrapf(err, fault.Unavailable, op, "send %s", msg.Subject)
	}
	var chunks [][]byte
	for got := 0; chunks == nil || got < len(chunks); {
		m, err := sub.NextMsgWithContext(ctx)
		if err != nil {
			return nil, fault.Wrapf(err, fault.Unavailable, op, "no answer to %s", msg.Subject)
		}
		if len(m.Data) == 0 && m.Header.Get("Status") == "503" {
			return nil, fault.Unavailablef(op, "no control plane answers %s", msg.Subject)
		}
		i, ierr := strconv.Atoi(m.Header.Get(headerChunk))
		n, nerr := strconv.Atoi(m.Header.Get(headerChunks))
		switch {
		case ierr != nil || nerr != nil || n < 1 || i < 0 || i >= n || chunks != nil && n != len(chunks):
			return nil, fault.Internalf(op, "a malformed answer to %s", msg.Subject)
		case chunks == nil:
			chunks = make([][]byte, n)
		}
		if chunks[i] == nil {
			chunks[i] = append([]byte{}, m.Data...)
			got++
		}
	}
	var env envelope
	var all []byte
	for _, c := range chunks {
		all = append(all, c...)
	}
	if err := json.Unmarshal(all, &env); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "decode the answer to %s", msg.Subject)
	}
	if env.Error != nil {
		return nil, fault.Wrapf(nil, fault.KindOfProblem(*env.Error), op, "%s: %s", msg.Subject, env.Error.Detail)
	}
	return env.OK, nil
}
