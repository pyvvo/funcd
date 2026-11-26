package multinode

import (
	"encoding/json"
	"flag"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/nodeapi"
)

var update = flag.Bool("update", false, "write the spec fields of this tree to testdata") //nolint:gochecknoglobals // a test flag

// specFields are the fields a node's kinds carry in their spec, each with the paths by kind it is found at.
func specFields() map[string]map[v1.Kind][]string {
	out := map[string]map[v1.Kind][]string{}
	marshaler := reflect.TypeFor[json.Marshaler]()
	var walk func(kind v1.Kind, t reflect.Type, path string, seen []reflect.Type)
	walk = func(kind v1.Kind, t reflect.Type, path string, seen []reflect.Type) {
		switch t.Kind() {
		case reflect.Pointer, reflect.Slice:
			walk(kind, t.Elem(), path, seen)
			return
		case reflect.Map:
			walk(kind, t.Elem(), path+".*", seen)
			return
		case reflect.Struct:
		default:
			return
		}
		if t.Implements(marshaler) || reflect.PointerTo(t).Implements(marshaler) || slices.Contains(seen, t) {
			return
		}
		for i := range t.NumField() {
			f := t.Field(i)
			name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
			switch {
			case name == "-" || !f.IsExported():
				continue
			case strings.Contains(opts, "inline"), f.Anonymous && name == "":
				walk(kind, f.Type, path, append(seen, t))
				continue
			}
			field := t.Name() + "." + name
			if out[field] == nil {
				out[field] = map[v1.Kind][]string{}
			}
			out[field][kind] = append(out[field][kind], path+"."+name)
			walk(kind, f.Type, path+"."+name, append(seen, t))
		}
	}
	for _, k := range v1.AllKinds() {
		obj, ok := v1.NewObject(k)
		if !ok || !k.Namespaced() {
			continue
		}
		if f, ok := reflect.TypeOf(obj).Elem().FieldByName("Spec"); ok {
			walk(k, f.Type, "spec", nil)
		}
	}
	return out
}

// A spec field that a release adds is one that a node on an older release drops without a word (decision 12): it needs a
// node API version of its own, named in nodeFields for each kind and path, unless an older node may ignore it.
func TestANewSpecFieldNamesTheNodeAPIVersionItNeeds(t *testing.T) {
	const golden = "testdata/node-api-fields.txt"
	fields := specFields()
	require.Contains(t, fields, "FunctionSpec.handler")
	if *update {
		require.NoError(t, os.MkdirAll(filepath.Dir(golden), 0o750))
		require.NoError(t, os.WriteFile(golden, []byte(strings.Join(slices.Sorted(maps.Keys(fields)), "\n")+"\n"), 0o600))
	}
	b, err := os.ReadFile(golden)
	require.NoError(t, err)
	known := map[string]bool{}
	for _, l := range strings.Fields(string(b)) {
		known[l] = true
	}
	named := nodeFields()
	for field, kinds := range fields {
		if known[field] {
			continue
		}
		for kind, paths := range kinds {
			for _, p := range paths {
				require.Greater(t, named[kind][p], 1, "%s is new, at %s of %s: a node on an older release would drop it. "+
					"Raise nodeapi.APIVersion and name it in nodeFields, or, when an older node may ignore it, run this test with "+
					"-update", field, p, kind)
			}
		}
	}
	for kind, paths := range named {
		for p, v := range paths {
			require.LessOrEqual(t, v, nodeapi.APIVersion, "%s %s", kind, p)
			require.True(t, slices.ContainsFunc(slices.Collect(maps.Values(fields)), func(at map[v1.Kind][]string) bool {
				return slices.Contains(at[kind], p)
			}), "%s has no field %s", kind, p)
		}
	}
}

func TestAFieldIsFoundInListsAndMapsToo(t *testing.T) {
	raw := json.RawMessage(`{"spec":{"do":[{"name":"a"},{"name":"b","retry":{"limit":3}}],"env":{"A":{"from":"x"}},"none":null}}`)
	for path, want := range map[string]bool{"spec.do.name": true, "spec.do.retry.limit": true, "spec.do.when": false,
		"spec.env.*.from": true, "spec.env.*.to": false, "spec.none": false, "spec": true} {
		require.Equal(t, want, fieldSet(raw, path), path)
	}
}
