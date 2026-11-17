package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
)

func TestAnAPITokenNamesItsCallerAsTheConfigsCredentialsDo(t *testing.T) {
	tok := func(name, role string, nss ...NamespaceName) *APIToken {
		t := &APIToken{TypeMeta: typeMetaFor(KindAPIToken)}
		t.Name, t.Spec.Role, t.Spec.Namespaces = ObjectName(name), role, nss
		return t
	}
	require.NoError(t, tok("ops", "admin").Validate())
	require.NoError(t, tok("ci", "developer", "shop").Validate())
	require.NoError(t, tok("audit", "viewer", "shop", "blog").Validate())
	for what, bad := range map[string]*APIToken{
		"an admin with namespaces":     tok("ops", "admin", "shop"),
		"a developer with none":        tok("ci", "developer"),
		"an unknown role":              tok("ci", "owner", "shop"),
		"a namespace that is no label": tok("ci", "viewer", "Shop_1"),
		"a name that is no label":      tok("CI.1", "admin"),
	} {
		require.Equal(t, fault.Invalid, fault.KindOf(bad.Validate()), what)
	}
}
