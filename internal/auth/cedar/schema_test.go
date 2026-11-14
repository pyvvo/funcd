package cedar_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/auth/cedar"
)

// scenario: policy-validity (the curated-schema half) — valid Cedar over the curated actions +
// entity types passes; un-parseable text or an off-schema action/entity-type is fault.Invalid.
func TestScenarioPolicyValidityCuratedSchema(t *testing.T) {
	t.Parallel()

	require.NoError(t, cedar.ValidateCedar(`permit(principal == Function::"default/reporting", action == Action::"kv::read", resource in KVStore::"default/orders");`),
		"a well-formed kv::read Policy over curated types is valid")
	require.NoError(t, cedar.ValidateCedar(`permit(principal, action == Action::"kv::write", resource);`),
		"a kv::write Policy is valid")

	cases := map[string]string{
		"unparseable":         `permit(principal, action ==`,
		"empty":               `   `,
		"unknown action":      `permit(principal, action == Action::"kv::frobnicate", resource);`,
		"unknown entity type": `permit(principal == Mystery::"x", action == Action::"kv::read", resource);`,
		"no named action":     `permit(principal, action, resource);`,
	}
	for name, text := range cases {
		text := text
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := cedar.ValidateCedar(text)
			require.Error(t, err, "%s must be rejected", name)
			require.Equal(t, fault.Invalid, fault.KindOf(err), "rejection is fault.Invalid")
		})
	}
}

// KnownAction / KnownEntityType expose the curated schema (ADR-0074).
func TestCuratedSchemaVocabulary(t *testing.T) {
	t.Parallel()
	require.True(t, cedar.KnownAction("kv::read"))
	require.True(t, cedar.KnownAction("kv::write"))
	require.True(t, cedar.KnownAction("link::invoke")) // ADR-0075 added the invoke action
	require.False(t, cedar.KnownAction("egress::send"))
	require.True(t, cedar.KnownEntityType("Function"))
	require.True(t, cedar.KnownEntityType("KVStore"))
	require.True(t, cedar.KnownEntityType("KVTable"))
	require.True(t, cedar.KnownEntityType("Secret")) // secret::read's resource (ADR-0321)
	require.False(t, cedar.KnownEntityType("ConfigMap"))
}

// The entity of an `is … in` scope is type-checked like an `==` or `in` scope's entity.
func TestIssue676_IsInScopeUnknownEntityTypeRejected(t *testing.T) {
	t.Parallel()
	require.NoError(t, cedar.ValidateCedar(`permit(principal, action == Action::"kv::read", resource is KVTable in KVStore::"team-b/x");`))

	cases := map[string]string{
		"resource":  `permit(principal, action == Action::"kv::read", resource is KVTable in Foo::"team-b/x");`,
		"principal": `permit(principal is Function in Foo::"team-b/x", action == Action::"kv::read", resource);`,
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := cedar.ValidateCedar(text)
			require.Error(t, err)
			require.Equal(t, fault.Invalid, fault.KindOf(err))
			require.ErrorContains(t, err, `unknown entity type "Foo"`)
		})
	}
}

// The refusal of an unknown action or entity type lists the vocabulary funcd accepts, which every capability adds to
// (ADR-0116), so an action such as secret::read or a type such as Secret appears there as soon as it is known.
func TestUnknownActionAndTypeRefusalsListTheVocabulary(t *testing.T) {
	t.Parallel()
	actionErr := cedar.ValidateCedar(`permit(principal, action == Action::"s3::delete", resource);`)
	require.ErrorContains(t, actionErr, `unknown action "s3::delete"`)
	typeErr := cedar.ValidateCedar(`permit(principal, action == Action::"kv::read", resource in Foo::"default/x");`)
	require.ErrorContains(t, typeErr, `unknown entity type "Foo"`)
	actions, types := listedIn(t, actionErr), listedIn(t, typeErr)
	for _, c := range []cedar.Capability{cedar.KVCapability(), cedar.InvokeCapability(), cedar.S3Capability(),
		cedar.EgressCapability(), cedar.LakeCapability(), cedar.SQLCapability(), cedar.SecretCapability()} {
		for _, a := range c.Actions {
			require.True(t, cedar.KnownAction(string(a)), "%s is in the vocabulary", a)
			require.Contains(t, actions, string(a), "the action refusal lists %s", a)
		}
		want := []string{string(c.EmitsEntityType)}
		for _, et := range c.SupportingEntityTypes {
			want = append(want, string(et))
		}
		for _, et := range want {
			require.True(t, cedar.KnownEntityType(et), "%s is in the vocabulary", et)
			require.Contains(t, types, et, "the entity type refusal lists %s", et)
		}
	}
}

// listedIn returns the items of the "(curated: a, b)" list that ends a refusal.
func listedIn(t *testing.T, err error) []string {
	t.Helper()
	m := regexp.MustCompile(`\(curated: ([^)]*)\)$`).FindStringSubmatch(err.Error())
	require.Len(t, m, 2, "the refusal ends with its list: %v", err)
	return strings.Split(m[1], ", ")
}
