package multinode

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
)

// The liveness pacing (ADR-0367 Decision 2): a node is Ready while its last poll is under readyFor old; its lastSeen is
// written at most every seenEvery; the statuses are checked every livenessTick.
const (
	defaultReadyFor  = 40 * time.Second
	defaultSeenEvery = 30 * time.Second
	livenessTick     = 5 * time.Second
)

// The reasons of a WorkerNode's Ready condition.
const (
	ReasonNotJoined  = "NotJoined"
	ReasonPolling    = "Polling"
	ReasonNotPolling = "NotPolling"
)

// Liveness tracks each node's last contact with the control plane, and writes it into the WorkerNode's status.
type Liveness struct {
	st                  store.Store
	log                 *slog.Logger
	now                 func() time.Time
	readyFor, seenEvery time.Duration

	mu       sync.Mutex
	contacts map[v1.ObjectName]contact
}

type contact struct {
	at       time.Time
	version  string
	api      int
	platform string
	assumed  bool // taken at the control plane's start from a WorkerNode that read Ready, not from a poll
}

// NewLiveness tracks the nodes of st; zero durations take the defaults.
func NewLiveness(st store.Store, log *slog.Logger, now func() time.Time, readyFor, seenEvery time.Duration) *Liveness {
	if readyFor <= 0 {
		readyFor = defaultReadyFor
	}
	if seenEvery <= 0 {
		seenEvery = defaultSeenEvery
	}
	return &Liveness{st: st, log: log, now: now, readyFor: readyFor, seenEvery: seenEvery, contacts: map[v1.ObjectName]contact{}}
}

// Seen records a contact of node, with what it reported of itself.
func (l *Liveness) Seen(node v1.ObjectName, version string, api int, platform string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.contacts[node] = contact{at: l.now(), version: version, api: api, platform: platform}
}

// Ready reports whether node polled within readyFor.
func (l *Liveness) Ready(node v1.ObjectName) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	c, ok := l.contacts[node]
	return ok && l.now().Sub(c.at) < l.readyFor
}

// Run writes the WorkerNodes' statuses every livenessTick until ctx ends; the control plane assumes its nodes' contacts
// first (assumeReady).
func (l *Liveness) Run(ctx context.Context) {
	t := time.NewTicker(livenessTick)
	defer t.Stop()
	for {
		if err := l.Sync(ctx); err != nil && ctx.Err() == nil {
			l.log.WarnContext(ctx, "node liveness: sync the WorkerNodes' statuses", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// assumeReady takes each node whose WorkerNode reads Ready as polled at the control plane's start, so a restart of the
// control plane flips no node that polls again within readyFor, and holds its name against a join (ADR-0367 Decision 3).
// The control plane calls it before the node API serves; a failed read assumes no contact.
func (l *Liveness) assumeReady(ctx context.Context) {
	res, err := l.st.List(ctx, v1.KindWorkerNode.GVK(), store.ListOptions{})
	if err != nil {
		l.log.WarnContext(ctx, "node liveness: read the WorkerNodes at start", "error", err)
		return
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, obj := range res.Items {
		n, ok := obj.(*v1.WorkerNode)
		if !ok || n.Status.Phase != v1.PhaseReady {
			continue
		}
		if _, polled := l.contacts[n.Name]; polled {
			continue
		}
		s := n.Status
		l.contacts[n.Name] = contact{at: now, version: s.Version, api: s.NodeAPIVersion, platform: s.Platform, assumed: true}
	}
}

// Sync writes each WorkerNode's status where its phase changed or its lastSeen is stale.
func (l *Liveness) Sync(ctx context.Context) error {
	res, err := l.st.List(ctx, v1.KindWorkerNode.GVK(), store.ListOptions{})
	if err != nil {
		return err
	}
	for _, obj := range res.Items {
		n, ok := obj.(*v1.WorkerNode)
		if !ok {
			continue
		}
		if err := writeNodeStatus(ctx, l.st, n.Name, l.apply); err != nil && fault.KindOf(err) != fault.NotFound {
			return err
		}
	}
	return nil
}

// apply sets n's liveness fields; it reports whether they changed enough to write.
func (l *Liveness) apply(n *v1.WorkerNode) bool {
	l.mu.Lock()
	c, seen := l.contacts[n.Name]
	l.mu.Unlock()
	now := l.now()
	phase, cond, reason := v1.PhasePending, v1.ConditionFalse, ReasonNotJoined
	switch {
	case seen && now.Sub(c.at) < l.readyFor:
		phase, cond, reason = v1.PhaseReady, v1.ConditionTrue, ReasonPolling
	case n.Status.Certificate != nil:
		phase, reason = v1.PhaseDegraded, ReasonNotPolling
	}
	s := &n.Status
	changed := s.Phase != phase || !hasCondition(s.Conditions, cond, reason)
	if seen && !c.assumed && (changed || now.Sub(time.Time(s.LastSeen)) >= l.seenEvery) {
		s.LastSeen, s.Version, s.NodeAPIVersion, s.Platform = v1.NewTimestamp(c.at), c.version, c.api, c.platform
		changed = true
	}
	if !changed {
		return false
	}
	s.Phase = phase
	setCondition(&s.Conditions, v1.Condition{Type: v1.ConditionNodeReady, Status: cond, Reason: reason,
		Message: readyMessage(reason), LastTransitionTime: v1.NewTimestamp(now)})
	return true
}

func readyMessage(reason string) string {
	switch reason {
	case ReasonPolling:
		return "The node polls the control plane."
	case ReasonNotPolling:
		return "The node joined but has not polled the control plane lately."
	default:
		return "No node has joined under this name yet; mint a join token with funcdctl node token."
	}
}

func hasCondition(cs v1.Conditions, status v1.ConditionStatus, reason string) bool {
	for _, c := range cs {
		if c.Type == v1.ConditionNodeReady {
			return c.Status == status && c.Reason == reason
		}
	}
	return false
}

// setCondition replaces the condition of c's type, keeping its transition time when its status did not change.
func setCondition(cs *v1.Conditions, c v1.Condition) {
	for i, old := range *cs {
		if old.Type == c.Type {
			if old.Status == c.Status {
				c.LastTransitionTime = old.LastTransitionTime
			}
			(*cs)[i] = c
			return
		}
	}
	*cs = append(*cs, c)
}

// writeNodeStatus applies mutate to the WorkerNode name and writes it, when mutate reports a change; a write that loses
// a race reads again. Status writes stay out of the change log: no node follows WorkerNodes.
func writeNodeStatus(ctx context.Context, st store.Store, name v1.ObjectName, mutate func(*v1.WorkerNode) bool) error {
	ctx = store.WithoutChangeLog(ctx)
	for range 5 {
		obj, err := st.Get(ctx, v1.KindWorkerNode.GVK(), "", name)
		if err != nil {
			return err
		}
		n, ok := obj.(*v1.WorkerNode)
		if !ok {
			return fault.Internalf("multinode.writeNodeStatus", "WorkerNode %q decoded as %T", name, obj)
		}
		if !mutate(n) {
			return nil
		}
		if _, err = st.Update(ctx, n); fault.KindOf(err) != fault.Conflict {
			return err
		}
	}
	return fault.Conflictf("multinode.writeNodeStatus", "WorkerNode %q kept changing", name)
}
