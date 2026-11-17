package sql_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	sqlsvc "github.com/pyvvo/funcd/internal/services/sql"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

func TestAWorkerNodeDerivesTheEnginePasswordFromTheNamespacesMaster(t *testing.T) {
	nsMaster := []byte("the master of the database's namespace")
	held := false
	rt := newRuntime()
	st := store.New(memory.New())
	r, err := sqlsvc.NewReconciler(sqlsvc.ReconcilerDeps{Store: st, Provider: rt, Engines: sqlsvc.NewEngines(),
		Master: []byte(masterKey), Hold: gate{false}, NamespaceMasters: func(ns v1.NamespaceName) ([]byte, error) {
			if held {
				return nsMaster, nil
			}
			return nil, fault.Unavailablef("test", "no master for namespace %s", ns)
		}})
	require.NoError(t, err)
	create(t, st, "")
	_, err = r.Reconcile(context.Background(), controller.Request{GVK: v1.KindSQLDatabase.GVK(), Namespace: todoDB().Namespace, Name: todoDB().Name})
	require.Error(t, err, "no engine starts before the namespace's master reaches the node")
	require.Empty(t, rt.last.Env)

	held = true
	reconcile(t, r)
	require.Equal(t, sqlsvc.EnginePassword(nsMaster, todoDB().Namespace, todoDB().Name), rt.last.Env["FUNCD_SQL_PASSWORD"])
}
