package agent

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/multinode"
	"github.com/pyvvo/funcd/internal/secrets"
)

// The wait for a Secret's write to come back to the node's copy, so the workload that made it reads it.
const (
	secretEcho     = 2 * time.Second
	secretEchoPoll = 5 * time.Millisecond
)

// WriteSecret sets key of Secret binding to value as workload fn of ns, or removes it when value is nil, on the control
// plane, which holds the Secret (ADR-0327). It returns once the node's copy shows the write, or after secretEcho: the
// write is made by then, and the feed brings it.
func (a *Agent) WriteSecret(ctx context.Context, ns v1.NamespaceName, fn v1.WorkloadRef, binding, key string, value *string) error {
	const op = "agent.WriteSecret"
	var out multinode.SecretAnswer
	in := multinode.SecretWrite{Namespace: ns, Workload: fn, Secret: v1.ObjectName(binding), Key: key, Value: value}
	if _, err := a.do(ctx, http.MethodPost, multinode.PathSecrets, in, &out); err != nil {
		return secrets.ErrControlPlaneAway.Wrapf(err, op, "write secret %q", binding)
	}
	if out.Error != "" {
		return fault.Wrapf(errors.New(out.Error), out.Fault, op, "write secret %q on the control plane", binding)
	}
	ctx, cancel := context.WithTimeout(ctx, secretEcho)
	defer cancel()
	for ctx.Err() == nil {
		obj, err := a.cfg.Store.Get(ctx, v1.KindSecret.GVK(), ns, v1.ObjectName(binding))
		if err != nil || obj.GetObjectMeta().Generation >= out.Generation {
			return nil
		}
		sleep(ctx, secretEchoPoll)
	}
	return nil
}
