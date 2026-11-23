package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// ADR-0342 Decision 4: funcdctl logs reads a Function by a bare name or function/<name>, an engine by <kind>/<name>, its
// kind resolved as funcdctl get resolves one, and refuses another kind of funcd's before any call.
func TestLogsTargetForms(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		arg  string
		kind v1.Kind
		name v1.ObjectName
		err  string
	}{
		{arg: "echo", kind: v1.KindFunction, name: "echo"},
		{arg: "function/login", kind: v1.KindFunction, name: "login"},
		{arg: "lake/sales", kind: v1.KindLake, name: "sales"},
		{arg: "SQLDatabase/orders", kind: v1.KindSQLDatabase, name: "orders"},
		{arg: "IdentityProvider/login", kind: "IdentityProvider", name: "login"},
		{arg: "identityprovider/login", kind: "identityprovider", name: "login"},
		{arg: "bucket/b", err: "a Bucket runs no engine"},
		{arg: "workflowrun/r1", err: "funcdctl workflow logs r1"},
		{arg: "Not A Kind/x", err: "names no kind"},
	} {
		t.Run(tc.arg, func(t *testing.T) {
			kind, name, err := logsTarget(tc.arg)
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.kind, kind)
			require.Equal(t, tc.name, name)
		})
	}
}
