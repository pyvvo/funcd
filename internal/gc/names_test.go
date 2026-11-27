package gc

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// partKinds are the kinds whose specs Names reads, with an empty object of each.
func partKinds() map[v1.Kind]v1.Object {
	return map[v1.Kind]v1.Object{
		v1.KindFunction: &v1.Function{}, v1.KindJob: &v1.Job{}, v1.KindWorkflow: &v1.Workflow{}, v1.KindLake: &v1.Lake{},
		v1.KindSite: &v1.Site{}, v1.KindRoute: &v1.Route{}, v1.KindEventSource: &v1.EventSource{}, v1.KindSensor: &v1.Sensor{},
		v1.KindAddonObject: &v1.AddonObject{}, v1.KindBackupSchedule: &v1.BackupSchedule{},
		v1.KindSQLDatabase: &v1.SQLDatabase{}, v1.KindKVStore: &v1.KVStore{}, v1.KindBucket: &v1.Bucket{},
		v1.KindConfigMap: &v1.ConfigMap{},
	}
}

// notNames are the ObjectName fields that name no object of the namespace, each with why; a trailing * covers a
// subtree: a root entry by name is the object's own child, and so is the spec inlined beside it.
func notNames() map[string]string {
	return map[string]string{
		"spec.kv[].name": "own child", "spec.buckets[].name": "own child", "spec.sql[].name": "own child",
		"spec.lakes[].name": "own child", "spec.configMaps[].name": "own child", "spec.lakes[].*": "an own Lake's spec",
		"spec.kv[].*": "an own store's spec", "spec.buckets[].*": "an own Bucket's spec", "spec.sql[].*": "an own database's spec",
		"spec.volumes[].name": "a volume, no object", "spec.volumes[].ref": "a volume, no object",
		"spec.steps[].name": "a step of this Workflow", "spec.steps[].dependsOn[]": "a step of this Workflow",
		"spec.onFailure": "a step of this Workflow", "spec.on[].name": "a dependency of this Sensor",
		"spec.on[].event": "an event of the EventSource", "spec.do[].name": "an action of this Sensor",
		"spec.do[].on": "a dependency of this Sensor", "spec.timer.events[].name": "an event of this EventSource",
		"spec.blob.events[].name": "an event of this EventSource", "spec.sql.events[].name": "an event of this EventSource",
		"spec.owner.name": "the workload the store serves", "spec.prefixes[].owner.name": "the workload the prefix serves",
		"spec.tables[].owner.name": "the workload the table serves",
	}
}

// TestNamesCoversEveryField fills every ObjectName field of each part kind's spec, by reflection, and checks that
// Names reads each one but those notNames lists (ADR-0362 Decision 4): a new field fails here until it is sorted.
func TestNamesCoversEveryField(t *testing.T) {
	t.Parallel()
	for kind, obj := range partKinds() {
		spec := reflect.ValueOf(obj).Elem().FieldByName("Spec")
		var fields []string
		fill(spec, "spec", &fields, 0)
		obj.GetObjectMeta().Namespace = "ns"
		read := map[string]bool{}
		for _, n := range Names(obj) {
			read[index.ReplaceAllString(n.Field, "[]")] = true
		}
		for _, f := range fields {
			_, listed := notNames()[f]
			require.True(t, read[f] || listed || underSubtree(f), "%s: %s is an ObjectName that Names neither reads nor lists in notNames", kind, f)
			require.False(t, read[f] && listed, "%s: %s is read and listed in notNames", kind, f)
		}
		for f := range read {
			require.Contains(t, fields, f, "%s: Names reads %s, which is no ObjectName field", kind, f)
		}
		for _, n := range Names(obj) {
			require.Equal(t, kindOfField(index.ReplaceAllString(n.Field, "[]")), n.Ref.Kind, "%s: %s", kind, n.Field)
		}
	}
}

// underSubtree reports whether field lies under a notNames entry that ends with *.
func underSubtree(field string) bool {
	for p := range notNames() {
		if prefix, ok := strings.CutSuffix(p, "*"); ok && strings.HasPrefix(field, prefix) {
			return true
		}
	}
	return false
}

// fill gives v one element per slice, a value per pointer and a distinct name per ObjectName field, and appends the
// path of each ObjectName field, its indexes as [].
func fill(v reflect.Value, path string, fields *[]string, depth int) {
	if depth > 12 {
		return
	}
	switch {
	case v.Type() == reflect.TypeFor[v1.ObjectName]():
		v.SetString(fmt.Sprintf("n%d", len(*fields)))
		*fields = append(*fields, path)
	case v.Type() == reflect.TypeFor[v1.Kind]():
		v.SetString(string(v1.KindFunction))
	case v.Type() == reflect.TypeFor[json.RawMessage]():
	case v.Kind() == reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem(), path, fields, depth+1)
	case v.Kind() == reflect.Slice && v.Type().Elem().Kind() != reflect.Uint8:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fill(v.Index(0), path+"[]", fields, depth+1)
	case v.Kind() == reflect.Struct:
		for i := range v.NumField() {
			f := v.Type().Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if !f.IsExported() || name == "-" {
				continue
			}
			sub := path
			if name != "" {
				sub = path + "." + name
			}
			fill(v.Field(i), sub, fields, depth+1)
		}
	}
}

// TestNamesInFieldOrder reads a Function and a Sensor whose fields name objects, the first field of a repeated name
// winning.
func TestNamesInFieldOrder(t *testing.T) {
	t.Parallel()
	fn := &v1.Function{ObjectMeta: v1.ObjectMeta{Namespace: "ns", Name: "api"}, Spec: v1.FunctionSpec{
		Bindings: v1.FunctionBindings{
			KV:   []v1.FunctionKV{{Store: "cart"}},
			Lake: []v1.FunctionLake{{Lake: "events"}},
			Job:  []v1.FunctionJob{{Job: "migrate"}},
		},
		Buckets:  []v1.BucketEntry{{Ref: "assets"}},
		KVStores: []v1.KVStoreEntry{{Ref: "cart"}},
	}}
	require.Equal(t, []Name{
		{Ref: v1.ObjectRef{Kind: v1.KindKVStore, Namespace: "ns", Name: "cart"}, Field: "spec.bindings.kv[0].store"},
		{Ref: v1.ObjectRef{Kind: v1.KindLake, Namespace: "ns", Name: "events"}, Field: "spec.bindings.lake[0].lake"},
		{Ref: v1.ObjectRef{Kind: v1.KindJob, Namespace: "ns", Name: "migrate"}, Field: "spec.bindings.job[0].job"},
		{Ref: v1.ObjectRef{Kind: v1.KindBucket, Namespace: "ns", Name: "assets"}, Field: "spec.buckets[0].ref"},
	}, Names(fn))

	sensor := &v1.Sensor{ObjectMeta: v1.ObjectMeta{Namespace: "ns"}, Spec: v1.SensorSpec{
		On: []v1.Dependency{{Name: "new", Source: "orders", Event: "insert"}},
		Do: []v1.Action{{Name: "run", On: "new", Workflow: "ship"}},
	}}
	require.Equal(t, []Name{
		{Ref: v1.ObjectRef{Kind: v1.KindEventSource, Namespace: "ns", Name: "orders"}, Field: "spec.on[0].source"},
		{Ref: v1.ObjectRef{Kind: v1.KindWorkflow, Namespace: "ns", Name: "ship"}, Field: "spec.do[0].workflow"},
	}, Names(sensor))
}

// TestUsesReadsTheProtectedFields: a user uses an object only through the fields InUse read before Names, so InUse
// answers as before (ADR-0362 Decision 4): a Site's second blob binding, a Lake's location, a Route's Function and a
// root entry's ref protect nothing, and a Function's link to itself is no use.
func TestUsesReadsTheProtectedFields(t *testing.T) {
	t.Parallel()
	ns := v1.NamespaceName("ns")
	meta := func(name v1.ObjectName) v1.ObjectMeta { return v1.ObjectMeta{Namespace: ns, Name: name} }
	bucket := &v1.Bucket{ObjectMeta: meta("b")}
	cm := &v1.ConfigMap{ObjectMeta: meta("c")}
	kv := &v1.KVStore{ObjectMeta: meta("k")}
	fnObj := &v1.Function{ObjectMeta: meta("f")}
	addon := &v1.AddonObject{ObjectMeta: meta("a")}
	addon.Kind = "IdentityProvider"
	for name, tc := range map[string]struct {
		user v1.Object
		obj  v1.Object
		want bool
	}{
		"a Function's kv binding":                 {&v1.Function{Spec: v1.FunctionSpec{Bindings: v1.FunctionBindings{KV: []v1.FunctionKV{{Store: "k"}}}}}, kv, true},
		"a Function's root kv ref":                {&v1.Function{Spec: v1.FunctionSpec{KVStores: []v1.KVStoreEntry{{Ref: "k"}}}}, kv, false},
		"a Job's blob binding":                    {&v1.Job{Spec: v1.JobSpec{Bindings: v1.FunctionBindings{Blob: []v1.FunctionBlob{{Bucket: "b"}}}}}, bucket, true},
		"a Function's sql binding":                {&v1.Function{Spec: v1.FunctionSpec{Bindings: v1.FunctionBindings{SQL: []v1.FunctionSQL{{Database: "d"}}}}}, &v1.SQLDatabase{ObjectMeta: meta("d")}, true},
		"a Function's sql root ref":               {&v1.Function{Spec: v1.FunctionSpec{SQLDatabases: []v1.SQLDatabaseEntry{{Ref: "d"}}}}, &v1.SQLDatabase{ObjectMeta: meta("d")}, false},
		"a Function's link":                       {&v1.Function{ObjectMeta: meta("g"), Spec: v1.FunctionSpec{Bindings: v1.FunctionBindings{Links: []v1.FunctionLink{{Target: "f"}}}}}, fnObj, true},
		"a Function's own link":                   {&v1.Function{ObjectMeta: meta("f"), Spec: v1.FunctionSpec{Bindings: v1.FunctionBindings{Links: []v1.FunctionLink{{Target: "f"}}}}}, fnObj, false},
		"a Function's addon binding, client copy": {&v1.Function{Spec: v1.FunctionSpec{Bindings: v1.FunctionBindings{Addon: []v1.FunctionAddon{{Object: "a"}}}}}, addon, true},
		"a Lake's blob binding":                   {&v1.Lake{Spec: v1.LakeSpec{Bindings: v1.LakeBindings{Blob: []v1.FunctionBlob{{Bucket: "b"}}}}}, bucket, true},
		"a Lake's location":                       {&v1.Lake{Spec: v1.LakeSpec{Location: v1.LakeLocation{Bucket: "b"}}}, bucket, false},
		"a Lake's config":                         {&v1.Lake{Spec: v1.LakeSpec{Bindings: v1.LakeBindings{Config: []v1.ObjectName{"c"}}}}, cm, true},
		"a Site's served bucket":                  {&v1.Site{Spec: v1.SiteSpec{Bindings: v1.SiteBindings{Blob: []v1.BlobRef{{Bucket: "b"}}}}}, bucket, true},
		"a Site's second blob":                    {&v1.Site{Spec: v1.SiteSpec{Bindings: v1.SiteBindings{Blob: []v1.BlobRef{{Bucket: "x"}, {Bucket: "b"}}}}}, bucket, false},
		"a Site's rule bucket":                    {&v1.Site{Spec: v1.SiteSpec{Ingress: v1.SiteIngress{Rules: []v1.SiteRule{{Bucket: "b"}}}}}, bucket, false},
		"a Site's runtime config":                 {&v1.Site{Spec: v1.SiteSpec{RuntimeConfig: &v1.SiteRuntimeConfig{ConfigMap: "c"}}}, cm, true},
		"an EventSource's bucket":                 {&v1.EventSource{Spec: v1.EventSourceSpec{Blob: &v1.BlobSource{Bucket: "b"}}}, bucket, true},
		"a Route's static bucket":                 {&v1.Route{Spec: v1.RouteSpec{Rules: []v1.RouteRule{{Backend: v1.RouteBackend{Static: &v1.StaticBackend{Bucket: "b"}}}}}}, bucket, true},
		"a Route's Function":                      {&v1.Route{Spec: v1.RouteSpec{Rules: []v1.RouteRule{{Backend: v1.RouteBackend{Function: "f"}}}}}, fnObj, false},
		"a Sensor's Function":                     {&v1.Sensor{Spec: v1.SensorSpec{Do: []v1.Action{{Function: "f"}}}}, fnObj, false},
	} {
		tc.user.GetObjectMeta().Namespace = ns
		require.Equal(t, tc.want, Uses(tc.user, tc.obj), name)
	}
	require.True(t, slices.ContainsFunc(Names(&v1.Site{Spec: v1.SiteSpec{Ingress: v1.SiteIngress{Rules: []v1.SiteRule{{Bucket: "b"}}}}}),
		func(n Name) bool { return n.Field == "spec.ingress.rules[0].bucket" }), "Names still lists what uses leaves out")
}

// kindOfField is the kind a Names field names, by its path's end (ADR-0362 Decision 4).
func kindOfField(field string) v1.Kind {
	for _, r := range []struct {
		suffix string
		kind   v1.Kind
	}{
		{"kv[].store", v1.KindKVStore}, {"kv[].ref", v1.KindKVStore}, {"kvStores[]", v1.KindKVStore},
		{"bucket", v1.KindBucket}, {"buckets[].ref", v1.KindBucket}, {"buckets[]", v1.KindBucket},
		{"database", v1.KindSQLDatabase}, {"sql[].ref", v1.KindSQLDatabase}, {"databases[]", v1.KindSQLDatabase},
		{"addon[].object", v1.KindAddonObject}, {"addons[]", v1.KindAddonObject},
		{"lake[].lake", v1.KindLake}, {"lakes[].ref", v1.KindLake}, {"lakes[]", v1.KindLake},
		{"links[].target", v1.KindFunction}, {"function.ref", v1.KindFunction}, {"functions[].ref", v1.KindFunction},
		{".function", v1.KindFunction}, {"job[].job", v1.KindJob}, {"config[]", v1.KindConfigMap},
		{"configMaps[].ref", v1.KindConfigMap}, {"configMap", v1.KindConfigMap}, {"secret[]", v1.KindSecret},
		{"secrets[].name", v1.KindSecret}, {"credentialsSecret", v1.KindSecret}, {"provider", v1.KindOIDCProvider},
		{".site", v1.KindSite}, {".source", v1.KindEventSource}, {"workflow.ref", v1.KindWorkflow},
		{".workflow", v1.KindWorkflow}, {"scope.app", v1.KindApp},
	} {
		if strings.HasSuffix(field, r.suffix) {
			return r.kind
		}
	}
	return ""
}
