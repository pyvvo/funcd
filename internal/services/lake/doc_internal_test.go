package lake

import (
	"reflect"
	"testing"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/ownedstore"
	"github.com/pyvvo/funcd/internal/testkit/doctag"
)

// The Lake's field descriptions name these reasons, this environment key and the Secret read action, so a changed one
// fails here until they follow (ADR-0323 Decision 4).
func TestDocNamesTheConstant(t *testing.T) {
	spec := reflect.TypeFor[v1.LakeSpec]()
	doctag.Names(t, spec, "Location", reasonPrefixNotOwned)
	doctag.Names(t, spec, "Buckets", reasonStoreNotOwned)
	doctag.Names(t, spec, "ConfigMaps", reasonStoreNotOwned)
	doctag.Names(t, spec, "SecretDeclarations", ownedstore.ReasonSecretNotFound, ownedstore.ReasonSecretKeyMissing)
	bindings := reflect.TypeFor[v1.LakeBindings]()
	doctag.Names(t, bindings, "Config", quackTokenEnvKey)
	doctag.Names(t, bindings, "Secret", quackTokenEnvKey, string(auth.ActionSecretRead))
}
