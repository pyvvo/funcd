package v1alpha1

import (
	"cmp"
	"fmt"

	"github.com/pyvvo/funcd/api/fault"
)

// rootSections are the entries a Function or a Workflow declares at its root (ADR-0248 Decision 1), in the App's
// shapes; a Workflow sets only KV and Buckets.
type rootSections struct {
	ConfigMaps []ConfigMapEntry
	KV         []KVStoreEntry
	Buckets    []BucketEntry
	SQL        []SQLDatabaseEntry
	Lakes      []LakeEntry
	Secrets    []SecretDeclaration
}

// entries are the sections' entries as an App's, built in meta's namespace and resource group.
func (r rootSections) entries(meta ObjectMeta) []appEntry {
	at := func(n ObjectName) ObjectMeta {
		return ObjectMeta{Name: n, Namespace: meta.Namespace, ResourceGroup: meta.ResourceGroup}
	}
	var out []appEntry
	for i, e := range r.ConfigMaps {
		name := e.Name
		if e.Ref == "" && e.Name != "" {
			name = AppConfigMapName(e.Name, e.ConfigMapSpec)
		}
		out = append(out, newEntry("configMaps", i, entryHead{Name: e.Name, Ref: e.Ref}, e.ConfigMapSpec,
			&ConfigMap{TypeMeta: typeMetaFor(KindConfigMap), ObjectMeta: at(name), Spec: e.ConfigMapSpec}))
	}
	for i, e := range r.KV {
		out = append(out, newEntry("kv", i, entryHead{e.Name, e.Ref, e.Deletion}, e.KVStoreSpec,
			&KVStore{TypeMeta: typeMetaFor(KindKVStore), ObjectMeta: at(e.Name), Spec: e.KVStoreSpec}))
	}
	for i, e := range r.Buckets {
		out = append(out, newEntry("buckets", i, entryHead{e.Name, e.Ref, e.Deletion}, e.BucketSpec,
			&Bucket{TypeMeta: typeMetaFor(KindBucket), ObjectMeta: at(e.Name), Spec: e.BucketSpec}))
	}
	for i, e := range r.SQL {
		out = append(out, newEntry("sql", i, entryHead{e.Name, e.Ref, e.Deletion}, e.SQLDatabaseSpec,
			&SQLDatabase{TypeMeta: typeMetaFor(KindSQLDatabase), ObjectMeta: at(e.Name), Spec: e.SQLDatabaseSpec}))
	}
	for i, e := range r.Lakes {
		out = append(out, newEntry("lakes", i, entryHead{Name: e.Name, Ref: e.Ref}, e.LakeSpec,
			&Lake{TypeMeta: typeMetaFor(KindLake), ObjectMeta: at(e.Name), Spec: e.LakeSpec}))
	}
	return out
}

// validate checks each entry as an App checks its own: a name or only a ref, no name used twice in a section, a
// defined ConfigMap's name short enough for its stored name, and each defined object valid as its kind; then the
// Secret declarations.
func (r rootSections) validate(op string, meta ObjectMeta) error {
	seen := make(map[ObjectRef]string)
	for _, e := range r.entries(meta) {
		if err := e.validate(op); err != nil {
			return err
		}
		id := ObjectRef{Kind: e.kind, Name: cmp.Or(e.head.Name, e.head.Ref)}
		if prev, dup := seen[id]; dup {
			return fault.Invalidf(op, "%s repeats the name %q of %s", e.path, id.Name, prev)
		}
		seen[id] = e.path
		if e.part == nil {
			continue
		}
		if e.kind == KindConfigMap && len(e.head.Name) > maxAppConfigMapNameLength {
			return fault.Invalidf(op, "%s.name %q is longer than %d characters", e.path, e.head.Name, maxAppConfigMapNameLength)
		}
		if err := e.part.Validate(); err != nil {
			return partError(op, e.path, err)
		}
	}
	return validateSecretDeclarations(op, r.Secrets)
}

// validateSecretDeclarations refuses a declaration whose name is not a DNS label or repeats another's, or whose keys
// are empty, not env-var names, or repeated (ADR-0213 Decision 6).
func validateSecretDeclarations(op string, secrets []SecretDeclaration) error {
	names := make(map[ObjectName]string, len(secrets))
	for i, s := range secrets {
		path := fmt.Sprintf("spec.secrets[%d]", i)
		if !dnsLabel.MatchString(string(s.Name)) {
			return fault.Invalidf(op, "%s.name %q is not a valid DNS-1123 label", path, s.Name)
		}
		if prev, dup := names[s.Name]; dup {
			return fault.Invalidf(op, "%s repeats the name %q of %s", path, s.Name, prev)
		}
		names[s.Name] = path
		if len(s.Keys) == 0 {
			return fault.Invalidf(op, "%s.keys is empty: declare at least one key the parts read", path)
		}
		keys := make(map[string]string, len(s.Keys))
		for j, k := range s.Keys {
			field := fmt.Sprintf("%s.keys[%d]", path, j)
			if err := validateEnvKey(op, field, k); err != nil {
				return err
			}
			if prev, dup := keys[k]; dup {
				return fault.Invalidf(op, "%s repeats the key %q of %s", field, k, prev)
			}
			keys[k] = field
		}
	}
	return nil
}
