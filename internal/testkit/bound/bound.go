// Package bound declares at a test Function's root what its bindings name, for tests whose fixtures create the
// stores, lakes, ConfigMaps and Secrets themselves (ADR-0248 Decision 2).
package bound

import (
	"slices"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// Declare adds a ref entry for each store, bucket, database, lake and ConfigMap fn binds that its root does not
// declare yet, and the secrets declarations, which name the keys the fixture's Secrets hold.
func Declare(fn *v1.Function, secrets ...v1.SecretDeclaration) {
	s := &fn.Spec
	for _, b := range s.Bindings.KV {
		if !slices.ContainsFunc(s.KVStores, func(e v1.KVStoreEntry) bool { return e.Name == b.Store || e.Ref == b.Store }) {
			s.KVStores = append(s.KVStores, v1.KVStoreEntry{Ref: b.Store})
		}
	}
	for _, b := range s.Bindings.Blob {
		if !slices.ContainsFunc(s.Buckets, func(e v1.BucketEntry) bool { return e.Name == b.Bucket || e.Ref == b.Bucket }) {
			s.Buckets = append(s.Buckets, v1.BucketEntry{Ref: b.Bucket})
		}
	}
	for _, b := range s.Bindings.SQL {
		if !slices.ContainsFunc(s.SQLDatabases, func(e v1.SQLDatabaseEntry) bool { return e.Name == b.Database || e.Ref == b.Database }) {
			s.SQLDatabases = append(s.SQLDatabases, v1.SQLDatabaseEntry{Ref: b.Database})
		}
	}
	for _, b := range s.Bindings.Lake {
		if !slices.ContainsFunc(s.Lakes, func(e v1.LakeEntry) bool { return e.Name == b.Lake || e.Ref == b.Lake }) {
			s.Lakes = append(s.Lakes, v1.LakeEntry{Ref: b.Lake})
		}
	}
	for _, n := range s.Bindings.Config {
		if !slices.ContainsFunc(s.ConfigMaps, func(e v1.ConfigMapEntry) bool { return e.Name == n || e.Ref == n }) {
			s.ConfigMaps = append(s.ConfigMaps, v1.ConfigMapEntry{Ref: n})
		}
	}
	s.SecretDeclarations = append(s.SecretDeclarations, secrets...)
}
