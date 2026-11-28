package multinode

import (
	"context"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/funclog/logread"
	"github.com/pyvvo/funcd/internal/nodeapi"
)

// LogReader is the control plane's log read (controlplane.LogQuerier).
type LogReader interface {
	Read(ctx context.Context, q logread.Query) ([]logread.Line, error)
}

// RunLogReader is the control plane's run-log read (controlplane.WorkflowRunLogQuerier).
type RunLogReader interface {
	RunLogs(ctx context.Context, ns v1.NamespaceName, run v1.ObjectName, q logread.Query) ([]logread.Line, error)
}

// caller sends a node call: the node API's driver (nodeapi.Server).
type caller interface {
	Call(ctx context.Context, node v1.ObjectName, c nodeapi.Call) (nodeapi.Answer, error)
}

// router sends a call for a namespace that another node runs to that node.
type router struct {
	placement *Placement
	calls     caller
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
	a, err := r.calls.Call(ctx, node, nodeapi.Call{Kind: nodeapi.CallLogs, Namespace: v1.NamespaceName(q.Namespace), Query: q})
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
	a, err := r.calls.Call(ctx, node, nodeapi.Call{Kind: nodeapi.CallRunLogs, Namespace: ns, Run: run, Query: q})
	return a.Lines, err
}
