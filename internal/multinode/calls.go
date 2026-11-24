package multinode

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/funclog/logread"
)

// The call pacing: a node with no poll open, nor one within callOffline, is offline; a node gets callTimeout to answer.
const (
	callOffline = 3 * time.Second
	callTimeout = 10 * time.Second
)

// Calls queues the node calls of each node (FEAT-0002 decision 9). A node long-polls its own queue and posts each answer
// back; a call to an offline node fails at once, as decided for a read or an action.
type Calls struct {
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
	call Call
	done chan Answer
}

// NewCalls makes the queues.
func NewCalls(now func() time.Time) *Calls {
	return &Calls{now: now, nodes: map[v1.ObjectName]*nodeQueue{}}
}

func (c *Calls) queue(node v1.ObjectName) *nodeQueue {
	q, ok := c.nodes[node]
	if !ok {
		q = &nodeQueue{inflight: map[string]*pendingCall{}}
		c.nodes[node] = q
	}
	return q
}

// Online reports whether node has a poll open, or had one within callOffline.
func (c *Calls) Online(node v1.ObjectName) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	q := c.queue(node)
	return q.polls > 0 || c.now().Sub(q.lastPoll) <= callOffline
}

// Do queues call for node and waits for its answer, callTimeout at most.
func (c *Calls) Do(ctx context.Context, node v1.ObjectName, call Call) (Answer, error) {
	const op = "multinode.call"
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return Answer{}, fault.Wrapf(err, fault.Internal, op, "draw a call id")
	}
	call.ID = hex.EncodeToString(b[:])
	p := &pendingCall{call: call, done: make(chan Answer, 1)}
	c.mu.Lock()
	q := c.queue(node)
	if q.polls == 0 && c.now().Sub(q.lastPoll) > callOffline {
		c.mu.Unlock()
		if call.Namespace == "" {
			return Answer{}, fault.Unavailablef(op, "worker node %s is offline", node)
		}
		return Answer{}, fault.Unavailablef(op, "worker node %s of namespace %s is offline", node, call.Namespace)
	}
	q.pending = append(q.pending, p)
	q.inflight[call.ID] = p
	if q.added != nil {
		close(q.added)
		q.added = nil
	}
	c.mu.Unlock()
	t := time.NewTimer(callTimeout)
	defer t.Stop()
	select {
	case a := <-p.done:
		if a.Error != "" {
			return Answer{}, fault.Wrapf(errors.New(a.Error), cmp.Or(a.Fault, fault.Unavailable), op, "worker node %s answered: %s", node, a.Error)
		}
		return a, nil
	case <-t.C:
		c.drop(node, call.ID)
		return Answer{}, fault.Unavailablef(op, "worker node %s did not answer within %s", node, callTimeout)
	case <-ctx.Done():
		c.drop(node, call.ID)
		return Answer{}, ctx.Err()
	}
}

func (c *Calls) drop(node v1.ObjectName, id string) {
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

// Poll returns node's queued calls, as one batch, waiting up to wait for one.
func (c *Calls) Poll(ctx context.Context, node v1.ObjectName, wait time.Duration) []Call {
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
			out := make([]Call, 0, len(q.pending))
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

// Answer delivers node's answer to call id. A node answers only its own calls (the sender check of decision 9).
func (c *Calls) Answer(node v1.ObjectName, id string, a Answer) error {
	c.mu.Lock()
	p, ok := c.queue(node).inflight[id]
	delete(c.queue(node).inflight, id)
	c.mu.Unlock()
	if !ok {
		return fault.NotFoundf("multinode.Answer", "worker node %s has no call %s waiting", node, id)
	}
	p.done <- a
	return nil
}

// LogReader is the control plane's log read (controlplane.LogQuerier).
type LogReader interface {
	Read(ctx context.Context, q logread.Query) ([]logread.Line, error)
}

// RunLogReader is the control plane's run-log read (controlplane.WorkflowRunLogQuerier).
type RunLogReader interface {
	RunLogs(ctx context.Context, ns v1.NamespaceName, run v1.ObjectName, q logread.Query) ([]logread.Line, error)
}

// router sends a call for a namespace that another node runs through that node's queue.
type router struct {
	placement *Placement
	calls     *Calls
}

// remote is the node that runs ns, when it is not this process; "" with no error ⇒ here.
func (r router) remote(ctx context.Context, ns v1.NamespaceName) (v1.ObjectName, error) {
	if !r.placement.Skip(ns) {
		return "", nil
	}
	node, err := r.placement.NodeOf(ctx, ns)
	if err != nil {
		return "", err
	}
	if node == "" {
		return "", fault.Unavailablef("multinode.route", "namespace %s is placed on no worker node, and this control plane runs none", ns)
	}
	return node, nil
}

// LogRouter reads a Function's logs where its namespace runs.
type LogRouter struct {
	router
	local LogReader
}

func (r LogRouter) Read(ctx context.Context, q logread.Query) ([]logread.Line, error) {
	node, err := r.remote(ctx, v1.NamespaceName(q.Namespace))
	if err != nil {
		return nil, err
	}
	if node == "" {
		if r.local == nil {
			return nil, fault.Unavailablef("multinode.Read", "this control plane keeps no logs")
		}
		return r.local.Read(ctx, q)
	}
	a, err := r.calls.Do(ctx, node, Call{Kind: CallLogs, Namespace: v1.NamespaceName(q.Namespace), Query: q})
	return a.Lines, err
}

// RunLogRouter reads a WorkflowRun's logs where its namespace runs.
type RunLogRouter struct {
	router
	local RunLogReader
}

func (r RunLogRouter) RunLogs(ctx context.Context, ns v1.NamespaceName, run v1.ObjectName, q logread.Query) ([]logread.Line, error) {
	node, err := r.remote(ctx, ns)
	if err != nil {
		return nil, err
	}
	if node == "" {
		if r.local == nil {
			return nil, fault.Unavailablef("multinode.RunLogs", "this control plane keeps no run logs")
		}
		return r.local.RunLogs(ctx, ns, run, q)
	}
	a, err := r.calls.Do(ctx, node, Call{Kind: CallRunLogs, Namespace: ns, Run: run, Query: q})
	return a.Lines, err
}
