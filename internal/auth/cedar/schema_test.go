package cedar_test

import (
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
	require.False(t, cedar.KnownEntityType("Secret"))
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
