package admission_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
)

type sqlReader struct{ dbs []*v1.SQLDatabase }

func (r sqlReader) List(_ context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName) ([]v1.Object, error) {
	var out []v1.Object
	if gvk.Kind == v1.KindSQLDatabase {
		for _, d := range r.dbs {
			if d.Namespace == ns {
				out = append(out, d)
			}
		}
	}
	return out, nil
}

type sqlProber struct {
	has bool
	err error
}

func (p sqlProber) HasTables(context.Context, v1.NamespaceName, v1.ObjectName) (bool, error) {
	return p.has, p.err
}

func mkDB(name string) *v1.SQLDatabase {
	d := &v1.SQLDatabase{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSQLDatabase.GVK().APIVersion(), Kind: v1.KindSQLDatabase}}
	d.Name, d.Namespace = v1.ObjectName(name), "default"
	return d
}

func TestSQLDatabaseQuota(t *testing.T) {
	gvk := v1.KindSQLDatabase.GVK()
	full := admission.NewSQLDatabaseQuotaAdmission(sqlReader{dbs: []*v1.SQLDatabase{mkDB("a"), mkDB("b")}}, 2)
	require.True(t, full.Handles(gvk, admission.Create))
	require.False(t, full.Handles(gvk, admission.Delete))
	_, err := full.Admit(context.Background(), admission.Request{Operation: admission.Create, GVK: gvk, Object: mkDB("c")})
	require.Equal(t, fault.Invalid, fault.KindOf(err))

	under := admission.NewSQLDatabaseQuotaAdmission(sqlReader{dbs: []*v1.SQLDatabase{mkDB("a")}}, 2)
	_, err = under.Admit(context.Background(), admission.Request{Operation: admission.Create, GVK: gvk, Object: mkDB("c")})
	require.NoError(t, err)

	off := admission.NewSQLDatabaseQuotaAdmission(sqlReader{dbs: []*v1.SQLDatabase{mkDB("a"), mkDB("b")}}, -1)
	require.False(t, off.(interface{ ReadsNamespace() bool }).ReadsNamespace())
	_, err = off.Admit(context.Background(), admission.Request{Operation: admission.Create, GVK: gvk, Object: mkDB("c")})
	require.NoError(t, err)
}

func TestSQLDatabaseDeletionProtection(t *testing.T) {
	gvk := v1.KindSQLDatabase.GVK()
	del := func(p admission.SQLProber) error {
		_, err := admission.NewSQLDatabaseDeletionProtectionAdmission(sqlReader{}, p).Admit(context.Background(),
			admission.Request{Operation: admission.Delete, GVK: gvk, Old: mkDB("todo-db")})
		return err
	}
	require.True(t, admission.NewSQLDatabaseDeletionProtectionAdmission(sqlReader{}, sqlProber{}).Handles(gvk, admission.Delete))
	require.NoError(t, del(sqlProber{}), "no table ⇒ deletable")
	require.Equal(t, fault.Conflict, fault.KindOf(del(sqlProber{has: true})), "a table ⇒ 409")
	err := del(sqlProber{err: fault.Unavailablef("probe", "the engine is not Ready")})
	require.Equal(t, fault.Conflict, fault.KindOf(err), "data that cannot be checked ⇒ 409 (fail closed)")
	require.Contains(t, err.Error(), "not Ready")
	require.Equal(t, fault.Internal, fault.KindOf(del(sqlProber{err: fault.Internalf("probe", "odd answer")})))
}

// ADR-0233 Decision 7: an API delete of a database a Function binds is 409 naming the Function.
func TestSQLDatabaseDeleteRefusedWhileBound(t *testing.T) {
	gvk := v1.KindSQLDatabase.GVK()
	fn := mkFn("todo-api")
	fn.Spec.SQL = []v1.FunctionSQL{{Alias: "app", Database: "todo-db"}}
	adm := admission.NewSQLDatabaseDeletionProtectionAdmission(kvReader{fns: []*v1.Function{fn}}, sqlProber{})
	_, err := adm.Admit(context.Background(), admission.Request{Operation: admission.Delete, GVK: gvk, Old: mkDB("todo-db")})
	require.Equal(t, fault.Conflict, fault.KindOf(err))
	require.Contains(t, err.Error(), "todo-api")
	_, err = adm.Admit(context.Background(), admission.Request{Operation: admission.Delete, GVK: gvk, Old: mkDB("audit-db")})
	require.NoError(t, err, "a database no Function binds")
}
