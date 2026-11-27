package gc

import (
	"fmt"
	"regexp"
	"slices"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// Name is an object a spec names and the field that names it, such as spec.bindings.kv[0].store (ADR-0362 Decision 4).
type Name struct {
	Ref   v1.ObjectRef
	Field string
}

// Names lists the objects u's spec names, each once, in field order (ADR-0362 Decision 4): bindings and links, the
// ref of a root entry, a Lake's location, the Functions, Sites, Buckets and providers of routes and ingresses, a
// Site's runtime ConfigMap, an EventSource's sources, a Sensor's EventSources, Workflows and Functions, an add-on
// object's Functions, a backup scope's objects, and the owner of a SQLDatabase, a Bucket prefix or a KV table. The
// first field that names an object wins.
func Names(u v1.Object) []Name {
	n := &namer{ns: u.GetObjectMeta().Namespace, seen: map[v1.ObjectRef]bool{}}
	switch o := u.(type) {
	case *v1.Function:
		n.bindings("spec.bindings", o.Spec.Bindings)
		n.roots(o.Spec.KVStores, o.Spec.Buckets, o.Spec.SQLDatabases, o.Spec.Lakes, o.Spec.ConfigMaps)
		n.declared(o.Spec.SecretDeclarations)
		n.ingress("spec.ingress", o.Spec.Ingress)
	case *v1.Job:
		n.bindings("spec.bindings", o.Spec.Bindings)
		n.roots(o.Spec.KVStores, o.Spec.Buckets, o.Spec.SQLDatabases, o.Spec.Lakes, o.Spec.ConfigMaps)
		n.declared(o.Spec.SecretDeclarations)
	case *v1.Workflow:
		for i, s := range o.Spec.Steps {
			p := fmt.Sprintf("spec.steps[%d]", i)
			if s.Function != nil {
				n.add(v1.KindFunction, s.Function.Ref, p+".function.ref")
				n.step(p+".function.bindings", s.Function.Bindings)
			}
			if s.Workflow != nil {
				n.add(v1.KindWorkflow, s.Workflow.Ref, p+".workflow.ref")
			}
		}
		n.roots(o.Spec.KVStores, o.Spec.Buckets, o.Spec.SQLDatabases, o.Spec.Lakes, o.Spec.ConfigMaps)
		n.declared(o.Spec.SecretDeclarations)
	case *v1.Lake:
		n.blob("spec.bindings.blob", o.Spec.Bindings.Blob)
		n.list(v1.KindConfigMap, "spec.bindings.config", o.Spec.Bindings.Config)
		n.list(v1.KindSecret, "spec.bindings.secret", o.Spec.Bindings.Secret)
		n.add(v1.KindBucket, o.Spec.Location.Bucket, "spec.location.bucket")
		n.ingress("spec.ingress", o.Spec.Ingress)
		n.roots(nil, o.Spec.Buckets, nil, nil, o.Spec.ConfigMaps)
		n.declared(o.Spec.SecretDeclarations)
	case *v1.Site:
		for i, b := range o.Spec.Bindings.Blob {
			n.add(v1.KindBucket, b.Bucket, fmt.Sprintf("spec.bindings.blob[%d].bucket", i))
		}
		n.auth("spec.ingress.auth", o.Spec.Ingress.Auth)
		for i, r := range o.Spec.Ingress.Rules {
			n.add(v1.KindBucket, r.Bucket, fmt.Sprintf("spec.ingress.rules[%d].bucket", i))
		}
		if rc := o.Spec.RuntimeConfig; rc != nil {
			n.add(v1.KindConfigMap, rc.ConfigMap, "spec.runtimeConfig.configMap")
		}
		n.roots(nil, o.Spec.Buckets, nil, nil, nil)
	case *v1.Route:
		n.auth("spec.auth", o.Spec.Auth)
		for i, r := range o.Spec.Rules {
			p := fmt.Sprintf("spec.rules[%d]", i)
			n.add(v1.KindFunction, r.Backend.Function, p+".backend.function")
			n.add(v1.KindSite, r.Backend.Site, p+".backend.site")
			if r.Backend.Static != nil {
				n.add(v1.KindBucket, r.Backend.Static.Bucket, p+".backend.static.bucket")
			}
			n.auth(p+".auth", r.Auth)
		}
	case *v1.EventSource:
		if b := o.Spec.Blob; b != nil {
			n.add(v1.KindBucket, b.Bucket, "spec.blob.bucket")
		}
		if q := o.Spec.SQL; q != nil {
			n.add(v1.KindSQLDatabase, q.Database, "spec.sql.database")
		}
	case *v1.Sensor:
		for i, d := range o.Spec.On {
			n.add(v1.KindEventSource, d.Source, fmt.Sprintf("spec.on[%d].source", i))
		}
		for i, a := range o.Spec.Do {
			n.add(v1.KindWorkflow, a.Workflow, fmt.Sprintf("spec.do[%d].workflow", i))
			n.add(v1.KindFunction, a.Function, fmt.Sprintf("spec.do[%d].function", i))
		}
	case *v1.AddonObject:
		n.list(v1.KindSecret, "spec.bindings.secret", o.Spec.Bindings.Secret)
		n.jobs("spec.bindings.job", o.Spec.Bindings.Job)
		for i, f := range o.Spec.Functions {
			n.add(v1.KindFunction, f.Ref, fmt.Sprintf("spec.functions[%d].ref", i))
		}
		n.ingress("spec.ingress", o.Spec.Ingress)
	case *v1.BackupSchedule:
		s := o.Spec.Scope
		n.list(v1.KindKVStore, "spec.scope.kvStores", s.KVStores)
		n.list(v1.KindBucket, "spec.scope.buckets", s.Buckets)
		n.list(v1.KindLake, "spec.scope.lakes", s.Lakes)
		n.list(v1.KindSQLDatabase, "spec.scope.databases", s.Databases)
		n.list(v1.KindAddonObject, "spec.scope.addons", s.Addons)
		n.add(v1.KindApp, s.App, "spec.scope.app")
		n.add(v1.KindSecret, o.Spec.Target.CredentialsSecret, "spec.target.credentialsSecret")
	case *v1.SQLDatabase:
		n.add(o.Spec.Owner.Kind, o.Spec.Owner.Name, "spec.owner.name")
	case *v1.Bucket:
		for i, pr := range o.Spec.Prefixes {
			n.add(pr.Owner.Kind, pr.Owner.Name, fmt.Sprintf("spec.prefixes[%d].owner.name", i))
		}
	case *v1.KVStore:
		for i, tb := range o.Spec.Tables {
			n.add(tb.Owner.Kind, tb.Owner.Name, fmt.Sprintf("spec.tables[%d].owner.name", i))
		}
	}
	return n.out
}

// namer collects Names in field order, each object once.
type namer struct {
	ns   v1.NamespaceName
	seen map[v1.ObjectRef]bool
	out  []Name
}

func (n *namer) add(kind v1.Kind, name v1.ObjectName, field string) {
	ref := v1.ObjectRef{Kind: kind, Namespace: n.ns, Name: name}
	if name == "" || kind == "" || n.seen[ref] {
		return
	}
	n.seen[ref] = true
	n.out = append(n.out, Name{Ref: ref, Field: field})
}

func (n *namer) list(kind v1.Kind, field string, names []v1.ObjectName) {
	for i, name := range names {
		n.add(kind, name, fmt.Sprintf("%s[%d]", field, i))
	}
}

func (n *namer) blob(field string, bs []v1.FunctionBlob) {
	for i, b := range bs {
		n.add(v1.KindBucket, b.Bucket, fmt.Sprintf("%s[%d].bucket", field, i))
	}
}

func (n *namer) jobs(field string, js []v1.FunctionJob) {
	for i, j := range js {
		n.add(v1.KindJob, j.Job, fmt.Sprintf("%s[%d].job", field, i))
	}
}

// bindings names what a Function's or a Job's bindings bind: an addon entry names its AddonObject by name.
func (n *namer) bindings(p string, b v1.FunctionBindings) {
	for i, k := range b.KV {
		n.add(v1.KindKVStore, k.Store, fmt.Sprintf("%s.kv[%d].store", p, i))
	}
	n.blob(p+".blob", b.Blob)
	for i, s := range b.SQL {
		n.add(v1.KindSQLDatabase, s.Database, fmt.Sprintf("%s.sql[%d].database", p, i))
	}
	for i, a := range b.Addon {
		n.add(v1.KindAddonObject, a.Object, fmt.Sprintf("%s.addon[%d].object", p, i))
	}
	for i, l := range b.Lake {
		n.add(v1.KindLake, l.Lake, fmt.Sprintf("%s.lake[%d].lake", p, i))
	}
	for i, l := range b.Links {
		n.add(v1.KindFunction, l.Target, fmt.Sprintf("%s.links[%d].target", p, i))
	}
	n.jobs(p+".job", b.Job)
	n.list(v1.KindConfigMap, p+".config", b.Config)
	n.list(v1.KindSecret, p+".secret", b.Secret)
}

// step names what a Workflow step's bindings bind.
func (n *namer) step(p string, b v1.StepBindings) {
	n.bindings(p, v1.FunctionBindings{KV: b.KV, Blob: b.Blob, SQL: b.SQL, Lake: b.Lake, Config: b.Config, Secret: b.Secret})
}

// roots names the ref of each root entry; an entry by name is the object's own child and names nothing else.
func (n *namer) roots(kv []v1.KVStoreEntry, buckets []v1.BucketEntry, sql []v1.SQLDatabaseEntry, lakes []v1.LakeEntry,
	configMaps []v1.ConfigMapEntry) {
	for i, e := range kv {
		n.add(v1.KindKVStore, e.Ref, fmt.Sprintf("spec.kv[%d].ref", i))
	}
	for i, e := range buckets {
		n.add(v1.KindBucket, e.Ref, fmt.Sprintf("spec.buckets[%d].ref", i))
	}
	for i, e := range sql {
		n.add(v1.KindSQLDatabase, e.Ref, fmt.Sprintf("spec.sql[%d].ref", i))
	}
	for i, e := range lakes {
		n.add(v1.KindLake, e.Ref, fmt.Sprintf("spec.lakes[%d].ref", i))
	}
	for i, e := range configMaps {
		n.add(v1.KindConfigMap, e.Ref, fmt.Sprintf("spec.configMaps[%d].ref", i))
	}
}

// declared names each Secret a spec declares.
func (n *namer) declared(secrets []v1.SecretDeclaration) {
	for i, s := range secrets {
		n.add(v1.KindSecret, s.Name, fmt.Sprintf("spec.secrets[%d].name", i))
	}
}

func (n *namer) auth(field string, a *v1.RouteAuth) {
	if a != nil {
		n.add(v1.KindOIDCProvider, a.Provider, field+".provider")
	}
}

func (n *namer) ingress(field string, in *v1.Ingress) {
	if in != nil {
		n.auth(field+".auth", in.Auth)
	}
}

// index is a field's indexes, which a pattern's [] matches.
var index = regexp.MustCompile(`\[\d+\]`)

// matches reports whether field is pattern, a [] in the pattern standing for any index.
func matches(pattern, field string) bool {
	return pattern == field || pattern == index.ReplaceAllString(field, "[]")
}

// protected is the fields of Names that InUse reads for a user of the kind user (ADR-0199 Decision 6): of a Site, only
// its first blob binding, its served Bucket.
func protected(user v1.Kind) []string {
	switch user {
	case v1.KindFunction, v1.KindJob:
		return []string{"spec.bindings.links[].target", "spec.bindings.kv[].store", "spec.bindings.sql[].database",
			"spec.bindings.addon[].object", "spec.bindings.blob[].bucket", "spec.bindings.config[]"}
	case v1.KindLake:
		return []string{"spec.bindings.blob[].bucket", "spec.bindings.config[]"}
	case v1.KindEventSource:
		return []string{"spec.blob.bucket"}
	case v1.KindRoute:
		return []string{"spec.rules[].backend.static.bucket"}
	case v1.KindSite:
		return []string{"spec.bindings.blob[0].bucket", "spec.runtimeConfig.configMap"}
	}
	return nil
}

// uses reports whether u names obj in one of the fields InUse reads; a Function's link to itself is no use.
func uses(u, obj v1.Object) bool {
	kind, name, user := storedKind(obj), obj.GetObjectMeta().Name, storedKind(u)
	if kind == v1.KindFunction && user == v1.KindFunction && u.GetName() == name {
		return false
	}
	fields := protected(user)
	return slices.ContainsFunc(Names(u), func(n Name) bool {
		return n.Ref.Kind == kind && n.Ref.Name == name && slices.ContainsFunc(fields, func(p string) bool { return matches(p, n.Field) })
	})
}

// storedKind is obj's stored kind: an add-on object is an AddonObject, whatever add-on kind a client's copy carries.
func storedKind(obj v1.Object) v1.Kind {
	if _, ok := obj.(*v1.AddonObject); ok {
		return v1.KindAddonObject
	}
	return obj.GroupVersionKind().Kind
}
