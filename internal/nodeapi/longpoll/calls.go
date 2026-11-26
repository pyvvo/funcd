package longpoll

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/nodeapi"
)

// callOffline is the call pacing: a node with no poll open, nor one within it, is offline. A node gets
// nodeapi.CallTimeout to answer.
const callOffline = 3 * time.Second

// calls queues the node calls of each node (FEAT-0002 decision 9). A node long-polls its own queue and posts each answer
// back; a call to an offline node fails at once, as decided for a read or an action.
type calls struct {
	now func() time.Time

	mu    sync.Mutex
	nodes map[v1.ObjectName]*nodeQueue
}

type nodeQueue struct {
	pending  []*pendingCall
	inflight map[string]*pendingCall
	added    chan struct{}
	polls    int
	lastPoll time.Time
}

type pendingCall struct {
	call nodeapi.Call
	done chan nodeapi.Answer
}

func newCalls(now func() time.Time) *calls {
	return &calls{now: now, nodes: map[v1.ObjectName]*nodeQueue{}}
}

func (c *calls) queue(node v1.ObjectName) *nodeQueue {
	q, ok := c.nodes[node]
	if !ok {
		q = &nodeQueue{inflight: map[string]*pendingCall{}}
		c.nodes[node] = q
	}
	return q
}

// online reports whether node has a poll open, or had one within callOffline.
func (c *calls) online(node v1.ObjectName) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	q := c.queue(node)
	return q.polls > 0 || c.now().Sub(q.lastPoll) <= callOffline
}

// do queues call for node and waits for its answer, callTimeout at most.
func (c *calls) do(ctx context.Context, node v1.ObjectName, call nodeapi.Call) (nodeapi.Answer, error) {
	const op = "longpoll.call"
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nodeapi.Answer{}, fault.Wrapf(err, fault.Internal, op, "draw a call id")
	}
	call.ID = hex.EncodeToString(b[:])
	p := &pendingCall{call: call, done: make(chan nodeapi.Answer, 1)}
	c.mu.Lock()
	q := c.queue(node)
	if q.polls == 0 && c.now().Sub(q.lastPoll) > callOffline {
		c.mu.Unlock()
		if call.Namespace == "" {
			return nodeapi.Answer{}, fault.Unavailablef(op, "worker node %s is offline", node)
		}
		return nodeapi.Answer{}, fault.Unavailablef(op, "worker node %s of namespace %s is offline", node, call.Namespace)
	}
	q.pending = append(q.pending, p)
	q.inflight[call.ID] = p
	if q.added != nil {
		close(q.added)
		q.added = nil
	}
	c.mu.Unlock()
	t := time.NewTimer(nodeapi.CallTimeout)
	defer t.Stop()
	select {
	case a := <-p.done:
		if err := nodeapi.AnswerError(op, node, a); err != nil {
			return nodeapi.Answer{}, err
		}
		return a, nil
	case <-t.C:
		c.drop(node, call.ID)
		return nodeapi.Answer{}, fault.Unavailablef(op, "worker node %s did not answer within %s", node, nodeapi.CallTimeout)
	case <-ctx.Done():
		c.drop(node, call.ID)
		return nodeapi.Answer{}, ctx.Err()
	}
}

func (c *calls) drop(node v1.ObjectName, id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	q := c.queue(node)
	delete(q.inflight, id)
	for i, p := range q.pending {
		if p.call.ID == id {
			q.pending = append(q.pending[:i], q.pending[i+1:]...)
			break
		}
	}
}

// poll returns node's queued calls, as one batch, waiting up to wait for one.
func (c *calls) poll(ctx context.Context, node v1.ObjectName, wait time.Duration) []nodeapi.Call {
	c.mu.Lock()
	c.queue(node).polls++
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		q := c.queue(node)
		q.polls--
		q.lastPoll = c.now()
		c.mu.Unlock()
	}()
	t := time.NewTimer(wait)
	defer t.Stop()
	for {
		c.mu.Lock()
		q := c.queue(node)
		if len(q.pending) > 0 {
			out := make([]nodeapi.Call, 0, len(q.pending))
			for _, p := range q.pending {
				out = append(out, p.call)
			}
			q.pending = nil
			c.mu.Unlock()
			return out
		}
		if q.added == nil {
			q.added = make(chan struct{})
		}
		added := q.added
		c.mu.Unlock()
		select {
		case <-added:
		case <-t.C:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}

// answer delivers node's answer to call id. A node answers only its own calls (the sender check of decision 9).
func (c *calls) answer(node v1.ObjectName, id string, a nodeapi.Answer) error {
	c.mu.Lock()
	p, ok := c.queue(node).inflight[id]
	delete(c.queue(node).inflight, id)
	c.mu.Unlock()
	if !ok {
		return fault.NotFoundf("longpoll.answer", "worker node %s has no call %s waiting", node, id)
	}
	p.done <- a
	return nil
}
