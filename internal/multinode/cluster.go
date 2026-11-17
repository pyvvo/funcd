package multinode

import (
	"context"
	"log/slog"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/platform/version"
	"github.com/pyvvo/funcd/internal/store"
)

// The join tokens' lifetime: defaultTokenTTL unless asked, maxTokenTTL at most.
const (
	defaultTokenTTL = time.Hour
	maxTokenTTL     = 24 * time.Hour
)

// Config configures the multi-node control plane.
type Config struct {
	Store      store.Store
	Logger     *slog.Logger
	Dir        string        // the cluster directory, which holds the CA
	ListenAddr string        // the node API's address
	Hosts      []string      // the names and addresses the node API's certificate covers
	LocalNode  v1.ObjectName // the control plane's own node
	LocalRuns  bool          // false on a control plane alone (funcd server), which runs no namespace
	Master     []byte        // the control plane's master secret, the root of the namespace keys; nil ⇒ none
	Version    string        // the control plane's release, which no node may be newer than; "" ⇒ this binary's
	NodeFields FieldVersions // the spec fields a newer node API brings; nil ⇒ this release's
	Now        func() time.Time
}

// Cluster is the control plane's side of multi-node: the node API, the CA, the placement and the liveness of nodes,
// and the node-call queues.
type Cluster struct {
	cfg       Config
	ca        *CA
	placement *Placement
	liveness  *Liveness
	calls     *Calls
	server    *Server
}

// New loads or makes the CA and binds the node API.
func New(cfg Config) (*Cluster, error) {
	const op = "multinode.New"
	if cfg.Store == nil || cfg.Dir == "" || cfg.ListenAddr == "" {
		return nil, fault.Invalidf(op, "a store, a cluster directory and a listen address are required")
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
	ca, err := LoadOrCreateCA(cfg.Dir, cfg.Now())
	if err != nil {
		return nil, err
	}
	c := &Cluster{cfg: cfg, ca: ca, placement: NewPlacement(cfg.Store, cfg.LocalNode, cfg.LocalRuns),
		liveness: NewLiveness(cfg.Store, cfg.Logger, cfg.Now, 0, 0), calls: NewCalls(cfg.Now)}
	var keys *NamespaceKeys
	if len(cfg.Master) > 0 {
		keys = NewNamespaceKeys(cfg.Store, cfg.Master)
	}
	c.server, err = NewServer(ServerConfig{Store: cfg.Store, CA: ca, Placement: c.placement, Liveness: c.liveness,
		Calls: c.calls, Keys: keys, Logger: cfg.Logger, Version: cfg.Version, Now: cfg.Now}, cfg.ListenAddr, cfg.Hosts)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// Run serves the node API and keeps the WorkerNodes' statuses until ctx ends.
func (c *Cluster) Run(ctx context.Context) error {
	go c.liveness.Run(ctx)
	c.cfg.Logger.InfoContext(ctx, "node API listening", "addr", c.server.Addr(), "caHash", c.ca.Hash())
	return c.server.Serve(ctx)
}

// Close stops the node API.
func (c *Cluster) Close() error { return c.server.Close() }

// Addr is the node API's bound address.
func (c *Cluster) Addr() string { return c.server.Addr() }

// CA is the cluster CA.
func (c *Cluster) CA() *CA { return c.ca }

// Skip reports whether this process leaves namespace ns to a worker node (the controller's and the GC's filter).
func (c *Cluster) Skip(ns v1.NamespaceName) bool { return c.placement.Skip(ns) }

// Logs routes a Function log read to the node that runs its namespace.
func (c *Cluster) Logs(local LogReader) LogReader {
	return LogRouter{router: router{placement: c.placement, calls: c.calls}, local: local}
}

// RunLogs routes a WorkflowRun log read to the node that runs its namespace.
func (c *Cluster) RunLogs(local RunLogReader) RunLogReader {
	return RunLogRouter{router: router{placement: c.placement, calls: c.calls}, local: local}
}

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
	case ttl < 0 || ttl > maxTokenTTL:
		return "", time.Time{}, fault.Invalidf(op, "a join token lives from a second to %s, not %s", maxTokenTTL, ttl)
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
