package services_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/services"
	"github.com/pyvvo/funcd/internal/services/kv"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

func createService(t *testing.T, st store.Store, name string, typ v1.ServiceType, binding string) {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindService)
	require.True(t, ok)
	svc := obj.(*v1.Service)
	svc.Name = v1.ObjectName(name)
	svc.Namespace = "default"
	svc.ResourceGroup = "rg1"
	svc.Spec.Type = typ
	switch typ {
	case v1.ServiceTypeKV:
		if binding != "" {
			svc.Spec.KV = &v1.KVServiceSpec{Binding: binding}
		}
	case v1.ServiceTypeBlob:
		if binding != "" {
			svc.Spec.Blob = &v1.BlobServiceSpec{Binding: binding}
		}
	}
	_, err := st.Create(context.Background(), svc)
	require.NoError(t, err)
}

func phaseOf(t *testing.T, st store.Store, name string) v1.Phase {
	t.Helper()
	obj, err := st.Get(context.Background(), v1.KindService.GVK(), "default", v1.ObjectName(name))
	require.NoError(t, err)
	return obj.(*v1.Service).Status.Phase
}

func req(name string) controller.Request {
	return controller.Request{GVK: v1.KindService.GVK(), Namespace: "default", Name: v1.ObjectName(name)}
}

// scenario: service-reconciles-to-ready — a Service{type:kv} is driven to Ready via the dispatcher + KV handler.
func TestScenarioServiceReconcilesToReady(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	createService(t, st, "mykv", v1.ServiceTypeKV, "cache")
	d, err := services.NewDispatcher(st, nil, kv.NewHandler())
	require.NoError(t, err)

	_, err = d.Reconcile(context.Background(), req("mykv"))
	require.NoError(t, err)
	require.Equal(t, v1.PhaseReady, phaseOf(t, st, "mykv"))
}

// scenario: service-reconciler-ignores-other-types — an unregistered service type is a no-op.
func TestScenarioServiceReconcilerIgnoresOtherTypes(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	createService(t, st, "other", v1.ServiceTypeBlob, "ob")    // valid blob service; no handler registered for it
	d, err := services.NewDispatcher(st, nil, kv.NewHandler()) // only KV registered
	require.NoError(t, err)

	_, err = d.Reconcile(context.Background(), req("other"))
	require.NoError(t, err)
	require.NotEqual(t, v1.PhaseReady, phaseOf(t, st, "other"), "unregistered type is a no-op (no status change)")
}
