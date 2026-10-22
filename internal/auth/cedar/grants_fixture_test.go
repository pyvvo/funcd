package cedar

import v1 "github.com/pyvvo/funcd/api/types/v1alpha1"

// grantFixtures are the principals the grants-unchanged golden covers (ADR-0246): a Function with one binding of every
// kind, and a Lake with its blob binding. Only how they are written may change; the golden may not.
func grantFixtures() map[string]PrincipalObject {
	fn := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}}
	fn.Name, fn.Namespace = "api", "team"
	fn.Spec.Bindings.KV = []v1.FunctionKV{{Alias: "counters", Store: "counters", Table: "counts"}, {Alias: "cache", Store: "cache", Table: "hot"}}
	fn.Spec.Bindings.Blob = []v1.FunctionBlob{{Alias: "files", Bucket: "files", Prefix: "uploads"}}
	fn.Spec.Bindings.SQL = []v1.FunctionSQL{{Alias: "db", Database: "todo-db"}}
	fn.Spec.Bindings.Lake = []v1.FunctionLake{{Alias: "lake", Lake: "lake"}}
	fn.Spec.Bindings.Links = []v1.FunctionLink{{Alias: "billing", Target: "billing"}}
	fn.Spec.Bindings.Config = []v1.ObjectName{"settings"}
	fn.Spec.Bindings.Secret = []v1.ObjectName{"api-keys"}

	cs := &v1.Lake{TypeMeta: v1.TypeMeta{APIVersion: v1.KindLake.GVK().APIVersion(), Kind: v1.KindLake}}
	cs.Name, cs.Namespace = "lake", "team"
	cs.Spec.Bindings.Blob = []v1.FunctionBlob{{Alias: "lake", Bucket: "lakehouse", Prefix: "gold"}, {Alias: "raw", Bucket: "lakehouse", Prefix: "bronze"}}
	cs.Spec.Bindings.Config = []v1.ObjectName{"engine-config"}
	cs.Spec.Bindings.Secret = []v1.ObjectName{"quack-token"}
	return map[string]PrincipalObject{"Function": fn, "Lake": cs}
}
