package cedar_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
)

// lakeS3Meta is one namespace where CatalogService "lake" binds lakehouse/raw and, when withFn, Function "lake"
// binds fnBlob. The raw prefix's owner is the name "lake".
func lakeS3Meta(withFn bool, fnBlob ...v1.FunctionBlob) s3Meta {
	m := s3Meta{
		fns: map[string]*v1.Function{},
		css: map[string]*v1.CatalogService{
			"default/lake": {
				ObjectMeta: v1.ObjectMeta{Name: "lake", Namespace: "default", ResourceGroup: "rg1"},
				Spec:       v1.CatalogServiceSpec{Blob: []v1.FunctionBlob{{Alias: "raw", Bucket: "lakehouse", Prefix: "raw"}}},
			},
		},
		buckets: map[string]*v1.Bucket{
			"default/lakehouse": {
				ObjectMeta: v1.ObjectMeta{Name: "lakehouse", Namespace: "default", ResourceGroup: "rg1"},
				Spec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{
					{Name: "raw", Owner: "lake"},
					{Name: "a"},
				}},
			},
		},
	}
	if withFn {
		m.fns["default/lake"] = &v1.Function{
			ObjectMeta: v1.ObjectMeta{Name: "lake", Namespace: "default", ResourceGroup: "rg1"},
			Spec:       v1.FunctionSpec{Blob: fnBlob},
		}
	}
	return m
}

// scenario: engine-s3-bindings-are-its-own — with Function lake (no spec.blob) beside CatalogService lake bound to
// raw/, the engine reads raw/ and the Function does not.
func TestScenarioEngineS3BindingsAreItsOwn(t *testing.T) {
	t.Parallel()
	d := s3Driver(t, lakeS3Meta(true), fixedPolicies{rev: "0"})

	engine := authorize(t, d, csPrincipal("default", "lake"), auth.ActionS3Read, prefixResource("default", "lakehouse", "raw"))
	require.True(t, engine.Allowed, "the engine reads with its own spec.blob: %s", engine.Reason)
	fn := authorize(t, d, fnPrincipal("default", "lake"), auth.ActionS3Read, prefixResource("default", "lakehouse", "raw"))
	require.False(t, fn.Allowed, "the Function does not read with the engine's bindings")
}

// scenario: function-s3-bindings-are-its-own — with Function lake bound to a/ and CatalogService lake bound only
// to raw/, the engine does not read a/ and the Function does.
func TestScenarioFunctionS3BindingsAreItsOwn(t *testing.T) {
	t.Parallel()
	d := s3Driver(t, lakeS3Meta(true, v1.FunctionBlob{Alias: "a", Bucket: "lakehouse", Prefix: "a"}), fixedPolicies{rev: "0"})

	engine := authorize(t, d, csPrincipal("default", "lake"), auth.ActionS3Read, prefixResource("default", "lakehouse", "a"))
	require.False(t, engine.Allowed, "the engine does not read with the Function's bindings")
	fn := authorize(t, d, fnPrincipal("default", "lake"), auth.ActionS3Read, prefixResource("default", "lakehouse", "a"))
	require.True(t, fn.Allowed, "the Function reads with its own spec.blob: %s", fn.Reason)
}

// scenario: engine-writes-owned-prefix — the engine writes a prefix owned by its name only while it binds that
// prefix and no Function holds the name; a Function lookup error adds no engine writer.
func TestScenarioEngineWritesOwnedPrefix(t *testing.T) {
	t.Parallel()
	write := func(t *testing.T, m s3Meta, principal *auth.EntityRef, policies fixedPolicies) bool {
		t.Helper()
		return authorize(t, s3Driver(t, m, policies), principal, auth.ActionS3Write, prefixResource("default", "lakehouse", "raw")).Allowed
	}
	engine := csPrincipal("default", "lake")

	t.Run("bound and no Function writes", func(t *testing.T) {
		t.Parallel()
		require.True(t, write(t, lakeS3Meta(false), engine, fixedPolicies{rev: "0"}))
	})
	t.Run("unbound prefix is denied", func(t *testing.T) {
		t.Parallel()
		m := lakeS3Meta(false)
		m.css["default/lake"].Spec.Blob = []v1.FunctionBlob{{Alias: "a", Bucket: "lakehouse", Prefix: "a"}}
		require.False(t, write(t, m, engine, fixedPolicies{rev: "0"}))
	})
	t.Run("a same-named Function makes the Function the owner", func(t *testing.T) {
		t.Parallel()
		m := lakeS3Meta(true)
		require.False(t, write(t, m, engine, fixedPolicies{rev: "0"}))
		require.True(t, write(t, m, fnPrincipal("default", "lake"), fixedPolicies{rev: "0"}), "the Function owns the prefix")
	})
	t.Run("deleting the Function gives the engine write", func(t *testing.T) {
		t.Parallel()
		m := lakeS3Meta(true)
		require.False(t, write(t, m, engine, fixedPolicies{rev: "0"}))
		delete(m.fns, "default/lake")
		require.True(t, write(t, m, engine, fixedPolicies{rev: "0"}))
	})
	t.Run("a Function lookup error adds no engine writer", func(t *testing.T) {
		t.Parallel()
		m := lakeS3Meta(false)
		m.fnErr = fault.Unavailablef("s3Meta.Get", "store down")
		require.False(t, write(t, m, engine, fixedPolicies{rev: "0"}))
	})
	t.Run("a policy on resource.owner does not match the engine", func(t *testing.T) {
		t.Parallel()
		ownerOnly := fixedPolicies{rev: "1", policies: []v1.Policy{{
			ObjectMeta: v1.ObjectMeta{Name: "owner-only", Namespace: "default"},
			Spec: v1.PolicySpec{Cedar: `forbid(principal, action == Action::"s3::write", resource is BlobPrefix)
  unless { resource has owner && principal == resource.owner };`},
		}}}
		require.False(t, write(t, lakeS3Meta(false), engine, ownerOnly), "resource.owner stays the Function UID")
	})
}
