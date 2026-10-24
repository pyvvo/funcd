package cedar_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
)

// lakeS3Meta is one namespace where Lake "lake" binds lakehouse/raw and, when withFn, Function "lake"
// binds fnBlob. The raw prefix's owner is Lake "lake" (ADR-0275).
func lakeS3Meta(withFn bool, fnBlob ...v1.FunctionBlob) s3Meta {
	m := s3Meta{
		fns: map[string]*v1.Function{},
		css: map[string]*v1.Lake{
			"default/lake": {
				ObjectMeta: v1.ObjectMeta{Name: "lake", Namespace: "default", ResourceGroup: "rg1"},
				Spec:       v1.LakeSpec{Bindings: v1.LakeBindings{Blob: []v1.FunctionBlob{{Alias: "raw", Bucket: "lakehouse", Prefix: "raw"}}}},
			},
		},
		buckets: map[string]*v1.Bucket{
			"default/lakehouse": {
				ObjectMeta: v1.ObjectMeta{Name: "lakehouse", Namespace: "default", ResourceGroup: "rg1"},
				Spec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{
					{Name: "raw", Owner: v1.LakeOwner("lake")},
					{Name: "a"},
				}},
			},
		},
	}
	if withFn {
		m.fns["default/lake"] = &v1.Function{
			ObjectMeta: v1.ObjectMeta{Name: "lake", Namespace: "default", ResourceGroup: "rg1"},
			Spec:       v1.FunctionSpec{Bindings: v1.FunctionBindings{Blob: fnBlob}},
		}
	}
	return m
}

// scenario: engine-s3-bindings-are-its-own — with Function lake (no spec.blob) beside Lake lake bound to
// raw/, the engine reads raw/ and the Function does not.
func TestScenarioEngineS3BindingsAreItsOwn(t *testing.T) {
	t.Parallel()
	d := s3Driver(t, lakeS3Meta(true), fixedPolicies{rev: "0"})

	engine := authorize(t, d, csPrincipal("default", "lake"), auth.ActionS3Read, prefixResource("default", "lakehouse", "raw"))
	require.True(t, engine.Allowed, "the engine reads with its own spec.blob: %s", engine.Reason)
	fn := authorize(t, d, fnPrincipal("default", "lake"), auth.ActionS3Read, prefixResource("default", "lakehouse", "raw"))
	require.False(t, fn.Allowed, "the Function does not read with the engine's bindings")
}

// scenario: function-s3-bindings-are-its-own — with Function lake bound to a/ and Lake lake bound only
// to raw/, the engine does not read a/ and the Function does.
func TestScenarioFunctionS3BindingsAreItsOwn(t *testing.T) {
	t.Parallel()
	d := s3Driver(t, lakeS3Meta(true, v1.FunctionBlob{Alias: "a", Bucket: "lakehouse", Prefix: "a"}), fixedPolicies{rev: "0"})

	engine := authorize(t, d, csPrincipal("default", "lake"), auth.ActionS3Read, prefixResource("default", "lakehouse", "a"))
	require.False(t, engine.Allowed, "the engine does not read with the Function's bindings")
	fn := authorize(t, d, fnPrincipal("default", "lake"), auth.ActionS3Read, prefixResource("default", "lakehouse", "a"))
	require.True(t, fn.Allowed, "the Function reads with its own spec.blob: %s", fn.Reason)
}

// scenario: same-name-function-keeps-off-a-lake-prefix (ADR-0275) — a prefix owned by Lake lake is written by its
// engine, with Function lake beside it or not, and never by Function lake; resource.owner is the Lake.
func TestScenarioSameNameFunctionKeepsOffALakePrefix(t *testing.T) {
	t.Parallel()
	engine := csPrincipal("default", "lake")
	require.True(t, s3Write(t, lakeS3Meta(true), engine, fixedPolicies{rev: "0"}), "the engine writes its Lake's prefix")
	require.False(t, s3Write(t, lakeS3Meta(true), fnPrincipal("default", "lake"), fixedPolicies{rev: "0"}),
		"a same-named Function does not take the Lake's prefix")
	ownerOnly := fixedPolicies{rev: "1", policies: []v1.Policy{{
		ObjectMeta: v1.ObjectMeta{Name: "owner-only", Namespace: "default"},
		Spec: v1.PolicySpec{Cedar: `forbid(principal, action == Action::"s3::write", resource is BlobPrefix)
  unless { resource has owner && principal == resource.owner };`},
	}}}
	require.True(t, s3Write(t, lakeS3Meta(true), engine, ownerOnly), "resource.owner is the Lake")
}

// scenario: deleted-lake-saves-on-its-stop (ADR-0275) — with no stored Lake, its engine, a bare principal, still
// writes the prefix the Bucket gives it, and reads nothing.
func TestScenarioDeletedLakeSavesOnItsStop(t *testing.T) {
	t.Parallel()
	m := lakeS3Meta(false)
	delete(m.css, "default/lake")
	d := s3Driver(t, m, fixedPolicies{rev: "0"})
	engine := csPrincipal("default", "lake")
	write := authorize(t, d, engine, auth.ActionS3Write, prefixResource("default", "lakehouse", "raw"))
	require.True(t, write.Allowed, "the deleted Lake's engine saves its catalog: %s", write.Reason)
	read := authorize(t, d, engine, auth.ActionS3Read, prefixResource("default", "lakehouse", "raw"))
	require.False(t, read.Allowed, "with no stored Lake the engine has no bindings to read with")
}

// A Lake owner writes on the Bucket's word alone (ADR-0275 Decision 2): no binding of the Lake's and no lookup in the
// store decides it.
func TestTypedLakeOwnerNeedsNoLookup(t *testing.T) {
	t.Parallel()
	engine := csPrincipal("default", "lake")
	t.Run("a prefix the Lake does not bind", func(t *testing.T) {
		t.Parallel()
		m := lakeS3Meta(false)
		m.css["default/lake"].Spec.Bindings.Blob = []v1.FunctionBlob{{Alias: "a", Bucket: "lakehouse", Prefix: "a"}}
		require.True(t, s3Write(t, m, engine, fixedPolicies{rev: "0"}))
	})
	t.Run("a store that cannot read Functions", func(t *testing.T) {
		t.Parallel()
		m := lakeS3Meta(false)
		m.fnErr = fault.Unavailablef("s3Meta.Get", "store down")
		require.True(t, s3Write(t, m, engine, fixedPolicies{rev: "0"}))
	})
}

// scenario: function-owner-unchanged (ADR-0275) — a prefix owned by Function lake is written by that Function only,
// not by the same-named Lake's engine.
func TestScenarioFunctionOwnerUnchanged(t *testing.T) {
	t.Parallel()
	m := lakeS3Meta(true)
	m.buckets["default/lakehouse"].Spec.Prefixes[0].Owner = v1.FunctionOwner("lake")
	require.True(t, s3Write(t, m, fnPrincipal("default", "lake"), fixedPolicies{rev: "0"}), "the Function owner writes")
	require.False(t, s3Write(t, m, csPrincipal("default", "lake"), fixedPolicies{rev: "0"}), "the same-named Lake does not")
	require.False(t, s3Write(t, m, fnPrincipal("default", "other"), fixedPolicies{rev: "0"}), "nor does another Function")
}

// s3Write reports whether principal may s3::write lakehouse/raw under m and policies.
func s3Write(t *testing.T, m s3Meta, principal *auth.EntityRef, policies fixedPolicies) bool {
	t.Helper()
	return authorize(t, s3Driver(t, m, policies), principal, auth.ActionS3Write, prefixResource("default", "lakehouse", "raw")).Allowed
}
