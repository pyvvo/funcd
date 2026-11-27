// Package multinode is funcd's multi-node control plane (FEAT-0002): the rules of the node API that worker nodes call
// (Handler), the cluster CA and the join tokens, the placement of namespaces on nodes, the nodes' liveness, and the routers
// that send a node call to the node that runs its namespace. The control plane never dials a node: a node joins with a
// one-time token, then follows the change log of the namespaces placed on it, writes their status back and answers the
// node calls, over the node API port (internal/nodeapi) and its driver.
package multinode

import (
	"context"
	"log/slog"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/eventing/deadletter"
	"github.com/pyvvo/funcd/internal/nodeapi"
	"github.com/pyvvo/funcd/internal/platform/version"
	"github.com/pyvvo/funcd/internal/store"
)

// The join tokens' lifetime: defaultTokenTTL unless asked, from minTokenTTL to maxTokenTTL.
const (
	defaultTokenTTL = time.Hour
	minTokenTTL     = time.Second
	maxTokenTTL     = 24 * time.Hour
)

// Config configures the multi-node control plane.
type Config struct {
	Store      store.Store
	Logger     *slog.Logger
	CA         *CA            // the cluster CA (LoadOrCreateCA), which signs the node API's certificate and the nodes'
	API        nodeapi.Server // the node API's driver, which carries the nodes' requests and the node calls
	LocalNode  v1.ObjectName  // the control plane's own node
	LocalRuns  bool           // false on a control plane alone (funcd server), which runs no namespace
	Master     []byte         // the control plane's master secret, the root of the namespace keys; nil ⇒ none
	Secrets    SecretWriter   // makes the Secret writes of the nodes' workloads; nil ⇒ none
	Held       func() bool    // the control plane is held after its restore, so no node follows it; nil ⇒ never
	Version    string         // the control plane's release, which no node may be newer than; "" ⇒ this binary's
	NodeFields FieldVersions  // the spec fields a newer node API brings; nil ⇒ this release's
	Now        func() time.Time
}

// Cluster is the control plane's side of multi-node: the node API's rules and its driver, the CA, and the placement and
// the liveness of nodes.
type Cluster struct {
	cfg       Config
	ca        *CA
	placement *Placement
	liveness  *Liveness
	handler   *Handler
}

// New serves the node API's rules through cfg.API.
func New(cfg Config) (*Cluster, error) {
	const op = "multinode.New"
	if cfg.Store == nil || cfg.CA == nil || cfg.API == nil {
		return nil, fault.Invalidf(op, "a store, the cluster CA and the node API's driver are required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	cfg.Logger = cfg.Logger.With("component", "cluster")
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Version == "" {
		cfg.Version = version.Get().Version
	}
	if cfg.NodeFields == nil {
		cfg.NodeFields = nodeFields()
	}
	c := &Cluster{cfg: cfg, ca: cfg.CA, placement: NewPlacement(cfg.Store, cfg.LocalNode, cfg.LocalRuns),
		liveness: NewLiveness(cfg.Store, cfg.Logger, cfg.Now, 0, 0)}
	var keys *NamespaceKeys
	if len(cfg.Master) > 0 {
		keys = NewNamespaceKeys(cfg.Store, cfg.Master)
	}
	var err error
	c.handler, err = NewHandler(HandlerConfig{Store: cfg.Store, CA: cfg.CA, Placement: c.placement, Liveness: c.liveness,
		Keys: keys, Secrets: cfg.Secrets, Held: cfg.Held, Logger: cfg.Logger, Version: cfg.Version, Now: cfg.Now})
	if err != nil {
		return nil, err
	}
	return c, nil
}

// Run serves the node API and keeps the WorkerNodes' statuses until ctx ends. The nodes that read Ready are assumed to
// poll before the node API serves, so no join slips in before their names are held (ADR-0367 Decision 3).
func (c *Cluster) Run(ctx context.Context) error {
	c.liveness.assumeReady(ctx)
	go c.liveness.Run(ctx)
	if err := c.handler.startRouting(ctx); err != nil {
		return err
	}
	c.cfg.Logger.InfoContext(ctx, "node API listening", "addr", c.cfg.API.Addr(), "caHash", c.ca.Hash())
	return c.cfg.API.Serve(ctx, c.handler)
}

// Close stops the node API.
func (c *Cluster) Close() error { return c.cfg.API.Close() }

// Addr is the node API's bound address.
func (c *Cluster) Addr() string { return c.cfg.API.Addr() }

// CA is the cluster CA.
func (c *Cluster) CA() *CA { return c.ca }

// Skip reports whether this process leaves namespace ns to a worker node (the controller's and the GC's filter).
func (c *Cluster) Skip(ns v1.NamespaceName) bool { return c.placement.Skip(ns) }

// Logs routes a Function log read to the node that runs its namespace.
func (c *Cluster) Logs(local LogReader) LogReader {
	return LogRouter{router: c.router(), local: local}
}

// RunLogs routes a WorkflowRun log read to the node that runs its namespace.
func (c *Cluster) RunLogs(local RunLogReader) RunLogReader {
	return RunLogRouter{router: c.router(), local: local}
}

// DeadLetters routes the API's dead-letter reads and discards to the node that runs their namespace.
func (c *Cluster) DeadLetters(local deadletter.Store) deadletter.Store {
	return DeadLetterRouter{router: c.router(), Store: local}
}

// Replayer routes a dead letter's replay to the node that runs its namespace.
func (c *Cluster) Replayer(local Replayer) Replayer {
	return ReplayRouter{router: c.router(), local: local}
}

// AppRetrier routes an App's retry to the node that runs its namespace.
func (c *Cluster) AppRetrier(local AppRetrier) AppRetrier {
	return AppRetryRouter{router: c.router(), local: local}
}

// Volumes routes a retained volume's delete to the node that holds it.
func (c *Cluster) Volumes(local VolumeDeleter) VolumeDeleter {
	return VolumeRouter{router: c.router(), local: local}
}

// Hold routes a namespace's hold release and quarantine return to the node that runs it.
func (c *Cluster) Hold(local Holder) Holder { return HoldRouter{router: c.router(), Holder: local} }

// Nodes reaches the worker nodes' own holds and platform backups; hold and backup are this process's, backup nil ⇒ none.
func (c *Cluster) Nodes(hold Holder, backup Backups) Nodes {
	return Nodes{calls: c.cfg.API, st: c.cfg.Store, local: c.cfg.LocalNode, hold: hold, backup: backup}
}

func (c *Cluster) router() router { return router{placement: c.placement, calls: c.cfg.API} }

// Admissions are the multi-node checks of the control plane's write path.
func (c *Cluster) Admissions() []admission.Admission {
	return []admission.Admission{namespaceNode{st: c.cfg.Store, local: c.cfg.LocalNode}, workerNodeInUse{st: c.cfg.Store},
		hostOnOneNode{st: c.cfg.Store, placement: c.placement},
		nodeTooOld{st: c.cfg.Store, placement: c.placement, fields: c.cfg.NodeFields}}
}

// MintJoinToken mints a one-time join token for the declared WorkerNode node, valid for ttl (0 ⇒ an hour), and keeps
// its hash in the node's status; a token minted before and not used stops working.
func (c *Cluster) MintJoinToken(ctx context.Context, node v1.ObjectName, ttl time.Duration) (string, time.Time, error) {
	const op = "multinode.MintJoinToken"
	switch {
	case ttl == 0:
		ttl = defaultTokenTTL
	case ttl < minTokenTTL || ttl > maxTokenTTL:
		return "", time.Time{}, fault.Invalidf(op, "a join token lives from %s to %s, not %s", minTokenTTL, maxTokenTTL, ttl)
	}
	secret, err := newSecret()
	if err != nil {
		return "", time.Time{}, err
	}
	expires := c.cfg.Now().Add(ttl)
	err = writeNodeStatus(ctx, c.cfg.Store, node, func(n *v1.WorkerNode) bool {
		n.Status.PendingJoin = &v1.PendingJoin{TokenHash: HashSecret(secret), ExpiresAt: v1.NewTimestamp(expires)}
		return true
	})
	if fault.KindOf(err) == fault.NotFound {
		return "", time.Time{}, fault.NotFoundf(op, "no WorkerNode %s is declared: apply one first", node)
	}
	if err != nil {
		return "", time.Time{}, err
	}
	return Token{CAHash: c.ca.Hash(), Node: node, Secret: secret}.String(), expires, nil
}
