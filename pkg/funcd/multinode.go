package funcd

import (
	"bytes"
	"context"
	"net/http"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup/runner"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/eventing/deadletter"
	"github.com/pyvvo/funcd/internal/multinode"
	"github.com/pyvvo/funcd/internal/snapshot"
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
		hold := r.Method == http.MethodPost && (r.URL.Path == holdReleasePath || r.URL.Path == quarantineReturnPath)
		switch {
		case hold, r.Method == http.MethodGet, r.Method == http.MethodHead, r.Method == http.MethodOptions:
			next.ServeHTTP(w, r)
		default:
			http.Error(w, "this is a worker node: its namespaces are written on the control plane", http.StatusMethodNotAllowed)
		}
	})
}

// withoutSecrets is a worker node's metastore as its platform backup reads it (FEAT-0002 F251): every record but the
// Secrets. They are copies the control plane holds, sealed with the node's own key, so a node restored on a new
// machine takes them again from the control plane instead of from a backup it could not open.
type withoutSecrets struct{ snapshot.Source }

// secretRecords is the start of every Secret's record key: its bucket, then NUL (ADR-0202).
var secretRecords = []byte(v1.KindSecret.GVK().String() + "\x00") //nolint:gochecknoglobals // a fixed key prefix

func (w withoutSecrets) Snapshot(ctx context.Context, emit func(snapshot.Record) error) (string, error) {
	return w.Source.Snapshot(ctx, func(r snapshot.Record) error {
		if bytes.HasPrefix(r.Key, secretRecords) {
			return nil
		}
		return emit(r)
	})
}

// nodeBackup reports a worker node's platform backup to its control plane (FEAT-0002 F251).
func nodeBackup(r *runner.Runner) func(context.Context) (v1.NodeBackup, error) {
	return func(ctx context.Context) (v1.NodeBackup, error) {
		st, err := r.Status(ctx)
		if err != nil {
			return v1.NodeBackup{}, err
		}
		out := v1.NodeBackup{Held: st.Held, RPORisk: st.RPORisk, LastSuccessTime: st.LastSuccessTime, NextRunTime: st.NextRunTime}
		if st.LastFailure != nil {
			out.LastFailure = st.LastFailure.Error
		}
		return out, nil
	}
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
	joinTokens  controlplane.JoinTokenMinter
}

// routeNodeParts gives own, this process's parts, to a worker node's agent, which answers the node calls with them and
// with checks, the node's own checks before a write. On a control plane it wraps them in the routers that send a
// namespace's call to the node that runs it.
func (p *Platform) routeNodeParts(own nodeParts, holdSvc *controlplane.HoldService, checks []admission.Admission) nodeParts {
	c := p.cfg
	if p.agent != nil {
		p.agent.SetLogs(own.logs, own.runLogs)
		cmds := multinode.Commands{DeadLetters: own.deadLetters, Apps: own.apps, Volumes: own.volumes, Admissions: checks,
			Hold: holdSvc}
		if p.eventing.sensorReconciler != nil {
			cmds.Replayer = p.eventing.sensorReconciler
		}
		if c.backupRunner != nil {
			cmds.Backup = nodeBackup(c.backupRunner)
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
	out.joinTokens = p.cluster
	return out
}

// ClusterAddr is the node API's bound address on a control plane that worker nodes join (WithCluster); "" ⇒ none.
func (p *Platform) ClusterAddr() string {
	if p.cluster == nil {
		return ""
	}
	return p.cluster.Addr()
}
