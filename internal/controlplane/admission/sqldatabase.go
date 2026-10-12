package admission

import (
	"context"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// SQLProber reports whether a SQLDatabase's engine holds a user table (ADR-0232 Decision 7). It fails Unavailable
// when the engine cannot answer but the database has data on disk.
type SQLProber interface {
	HasTables(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (bool, error)
}

// --- database-count quota (ADR-0232 Decision 8): Create on SQLDatabase ------------------------

type sqlDatabaseQuota struct {
	r               StoreReader
	maxPerNamespace int
}

// NewSQLDatabaseQuotaAdmission returns the Validating admission that rejects creating a SQLDatabase when its
// namespace already holds maxPerNamespace databases. maxPerNamespace ≤ 0 disables the quota.
func NewSQLDatabaseQuotaAdmission(r StoreReader, maxPerNamespace int) Admission {
	return sqlDatabaseQuota{r: r, maxPerNamespace: maxPerNamespace}
}

func (sqlDatabaseQuota) Name() string { return "sqldatabase-quota" }
func (sqlDatabaseQuota) Phase() Phase { return Validating }

// ReadsNamespace: the count check reads the namespace's SQLDatabases, only while the quota is enabled (ADR-0147).
func (a sqlDatabaseQuota) ReadsNamespace() bool { return a.maxPerNamespace > 0 }

func (sqlDatabaseQuota) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindSQLDatabase.GVK() && op == Create
}

func (a sqlDatabaseQuota) Admit(ctx context.Context, req Request) (v1.Object, error) {
	const op = "admission.sqldatabase-quota"
	if a.maxPerNamespace <= 0 {
		return req.Object, nil
	}
	db, ok := req.Object.(*v1.SQLDatabase)
	if !ok {
		return req.Object, nil
	}
	existing, err := a.r.List(ctx, v1.KindSQLDatabase.GVK(), db.Namespace)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "list sqldatabases in %q", db.Namespace)
	}
	if len(existing) >= a.maxPerNamespace {
		return nil, fault.Invalidf(op, "namespace %q already holds the maximum %d SQLDatabases", db.Namespace, a.maxPerNamespace)
	}
	return req.Object, nil
}

// --- sqldatabase-deletion-protection (ADR-0232 Decision 7): Delete on SQLDatabase --------------

type sqlDatabaseDeletionProtection struct {
	r StoreReader
	p SQLProber
}

// NewSQLDatabaseDeletionProtectionAdmission returns the Validating admission that refuses deleting a SQLDatabase that
// a Function's spec.sql binds (ADR-0233 Decision 7), or whose engine holds a user table, or whose data cannot be
// checked (fail closed). A controller's delete through the store is not checked.
func NewSQLDatabaseDeletionProtectionAdmission(r StoreReader, p SQLProber) Admission {
	return sqlDatabaseDeletionProtection{r: r, p: p}
}

func (sqlDatabaseDeletionProtection) Name() string { return "sqldatabase-deletion-protection" }
func (sqlDatabaseDeletionProtection) Phase() Phase { return Validating }

func (sqlDatabaseDeletionProtection) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindSQLDatabase.GVK() && op == Delete
}

func (a sqlDatabaseDeletionProtection) Admit(ctx context.Context, req Request) (v1.Object, error) {
	const op = "admission.sqldatabase-deletion-protection"
	if req.Old == nil {
		return req.Old, nil
	}
	meta := req.Old.GetObjectMeta()
	if a.r != nil {
		fns, lerr := a.r.List(ctx, v1.KindFunction.GVK(), meta.Namespace)
		if lerr != nil {
			return nil, fault.Wrapf(lerr, fault.Internal, op, "list functions in %q", meta.Namespace)
		}
		for _, o := range fns {
			f, ok := o.(*v1.Function)
			if !ok {
				continue
			}
			for _, b := range f.Spec.SQL {
				if b.Database == meta.Name {
					return nil, fault.Conflictf(op, "SQLDatabase %q is bound by function %q (alias %q); remove the binding first", meta.Name, f.Name, b.Alias)
				}
			}
		}
	}
	if a.p == nil {
		return req.Old, nil
	}
	has, err := a.p.HasTables(ctx, meta.Namespace, meta.Name)
	switch {
	case fault.KindOf(err) == fault.Unavailable:
		return nil, fault.Conflictf(op, "SQLDatabase %q cannot be deleted: %v; retry once its engine is Ready", meta.Name, err)
	case err != nil:
		return nil, fault.Wrapf(err, fault.Internal, op, "probe sqldatabase %q for tables", meta.Name)
	case has:
		return nil, fault.Conflictf(op, "SQLDatabase %q still holds tables; drop them first", meta.Name)
	}
	return req.Old, nil
}
