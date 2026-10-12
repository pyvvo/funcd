package v1alpha1_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

func TestSQLDatabaseValidate(t *testing.T) {
	db := func(cpu, memory string) *v1.SQLDatabase {
		d := &v1.SQLDatabase{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSQLDatabase.GVK().APIVersion(), Kind: v1.KindSQLDatabase}}
		d.Name, d.Namespace, d.ResourceGroup = "todo-db", "default", "rg1"
		d.Spec.Resources = v1.ResourceSpec{CPU: cpu, Memory: memory}
		return d
	}
	for _, ok := range [][2]string{{"", ""}, {"0.5", "256Mi"}, {"2", "1Gi"}, {"", "1048576"}} {
		require.NoError(t, db(ok[0], ok[1]).Validate(), "%v", ok)
	}
	for _, bad := range [][2]string{{"", "256MB"}, {"", "lots"}, {"-1", ""}, {"0", ""}, {"half", ""}} {
		require.Equal(t, fault.Invalid, fault.KindOf(db(bad[0], bad[1]).Validate()), "%v", bad)
	}
}
