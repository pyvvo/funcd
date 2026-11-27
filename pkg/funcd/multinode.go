package funcd

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/eventing/deadletter"
	"github.com/pyvvo/funcd/internal/multinode"
	"github.com/pyvvo/funcd/internal/multinode/agent"
	"github.com/pyvvo/funcd/internal/nodeapi"
	"github.com/pyvvo/funcd/internal/nodeapi/longpoll"
	natsnode "github.com/pyvvo/funcd/internal/nodeapi/nats"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/store"
)

// The writes a worker node's own API answers: the release of the node's own hold and the return of its quarantine
// (ADR-0206), which act on the node alone, as on one host, and which a node restored from its backup needs.
const (
	holdReleasePath      = "/apis/funcd.io/v1alpha1/hold/release"
	quarantineReturnPath = "/apis/funcd.io/v1alpha1/quarantine/return"
)

// readOnly answers only the reads of a worker node's own API (FEAT-0002), and its hold actions: a node's namespaces are
// written on the control plane, and a write here would be undone by the next change the node follows.
func readOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		holdPath := r.URL.Path == holdReleasePath || r.URL.Path == quarantineReturnPath
		switch {
		case holdPath && r.Method == http.MethodPost, r.Method == http.MethodGet, r.Method == http.MethodHead,
			r.Method == http.MethodOptions:
			next.ServeHTTP(w, r)
		default:
			// The problem document the API's own router answers a method it does not allow with (installRouterErrors),
			// and the methods this path takes.
			allow := "GET, HEAD, OPTIONS"
			if holdPath {
				allow += ", POST"
			}
			w.Header().Set("Allow", allow)
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusMethodNotAllowed)
			_ = json.NewEncoder(w).Encode(fault.Problem{Type: "about:blank", Title: http.StatusText(http.StatusMethodNotAllowed),
				Status: http.StatusMethodNotAllowed, Detail: "this is a worker node: its namespaces are written on the control plane"})
		}
	})
}

// nodeParts are the parts of the control plane's API that act on a node's state (FEAT-0002 F250), with the join-token
// minter.
type nodeParts struct {
	logs        controlplane.LogQuerier
	runLogs     controlplane.WorkflowRunLogQuerier
	deadLetters deadletter.Store
	replayer    controlplane.Replayer
	apps        controlplane.AppRetrier
	volumes     controlplane.VolumeDeleter
	hold        controlplane.Holder
	backup      controlplane.BackupStatuser
	joinTokens  controlplane.JoinTokenMinter
	nodes       controlplane.Nodes
}

// routeNodeParts gives own, this process's parts, to a worker node's agent, which answers the node calls with them and
// with checks, the node's own checks before a write. On a control plane it wraps them in the routers that send a
// namespace's call to the node that runs it.
func (p *Platform) routeNodeParts(own nodeParts, holdSvc *controlplane.HoldService, checks []admission.Admission) nodeParts {
	if p.agent != nil {
		p.agent.SetLogs(own.logs, own.runLogs)
		cmds := multinode.Commands{DeadLetters: own.deadLetters, Apps: own.apps, Volumes: own.volumes, Admissions: checks,
			Hold: holdSvc, BlobStore: p.cfg.blobStoreURL}
		if p.eventing.sensorReconciler != nil {
			cmds.Replayer = p.eventing.sensorReconciler
		}
		if own.backup != nil {
			cmds.Backup = own.backup
		}
		p.agent.SetCommands(cmds)
	}
	if p.cluster == nil {
		return own
	}
	out := own
	out.logs, out.runLogs = p.cluster.Logs(own.logs), p.cluster.RunLogs(own.runLogs)
	out.deadLetters, out.replayer = p.cluster.DeadLetters(own.deadLetters), p.cluster.Replayer(own.replayer)
	out.apps, out.volumes, out.hold = p.cluster.AppRetrier(own.apps), p.cluster.Volumes(own.volumes), p.cluster.Hold(holdSvc)
	out.joinTokens, out.nodes = p.cluster, p.cluster.Nodes(holdSvc, own.backup)
	return out
}

// The node API's drivers (ClusterOptions.Driver).
const (
	driverHTTPS = "https"
	driverNATS  = "nats"
)

// nodeConnector is a worker node's side of the node API: the driver its control plane's URL names by its scheme.
func nodeConnector() nodeapi.Connector {
	return nodeapi.Schemes{driverHTTPS: longpoll.NewConnector(), driverNATS: natsnode.NewConnector()}
}

// newCluster serves the node API of a control plane that worker nodes join (WithCluster) over the driver o names, the
// HTTPS long poll unless it names nats, under a certificate the cluster CA signs: the CA is made on the first start, under
// o.Dir.
func (p *Platform) newCluster(o *ClusterOptions, st store.Store, local v1.ObjectName, master []byte) (*multinode.Cluster, error) {
	ca, err := multinode.LoadOrCreateCA(o.Dir, time.Now())
	if err != nil {
		return nil, err
	}
	tlsCfg, err := ca.ServerTLS(o.Hosts, time.Now)
	if err != nil {
		return nil, err
	}
	var api nodeapi.Server
	if o.Driver == driverNATS {
		api, err = natsnode.Listen(natsnode.Config{Addr: o.ListenAddr, TLS: tlsCfg, Logger: p.logger})
	} else {
		api, err = longpoll.Listen(longpoll.Config{Addr: o.ListenAddr, TLS: tlsCfg, Logger: p.logger})
	}
	if err != nil {
		return nil, err
	}
	cl, err := multinode.New(multinode.Config{Store: st, Logger: p.logger, CA: ca, API: api, LocalNode: local,
		LocalRuns: !o.Alone, Master: master, Secrets: p.secrets.facade, Held: p.heldByRestore})
	if err != nil {
		_ = api.Close()
		return nil, err
	}
	return cl, nil
}

// ErrControlPlaneRestored is Run's error on a worker node whose control plane was restored from a backup (FEAT-0002 F251):
// the caller runs the platform again, which takes the restored state before it runs anything.
var ErrControlPlaneRestored = agent.ErrRestored

// heldByRestore reports whether this platform is held after its restore, so its worker nodes wait for the release.
func (p *Platform) heldByRestore() bool {
	m, held := p.heldMarker()
	return held && m.Reason == hold.ReasonRestore
}

// controlPlaneAlone reports whether this process is the control plane of worker nodes and runs no namespace itself.
func (p *Platform) controlPlaneAlone() bool { return p.cfg.cluster != nil && p.cfg.cluster.Alone }

// elsewhere reports whether a worker node runs namespace ns (FEAT-0002): a control plane that worker nodes join leaves the
// namespace's objects, its runs, its Routes and its children to that node.
func (p *Platform) elsewhere(ns v1.NamespaceName) bool { return p.cluster != nil && p.cluster.Skip(ns) }

// ClusterAddr is the node API's bound address on a control plane that worker nodes join (WithCluster); "" ⇒ none.
func (p *Platform) ClusterAddr() string {
	if p.cluster == nil {
		return ""
	}
	return p.cluster.Addr()
}
