package identity

import (
	"reflect"
	"testing"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/testkit/doctag"
)

// An Identity's descriptions name the keys of the Secret funcd writes, so a renamed one fails here until they follow
// (ADR-0323 Decision 4).
func TestDocNamesTheConstant(t *testing.T) {
	spec := reflect.TypeFor[v1.IdentitySpec]()
	doctag.Names(t, spec, "CredentialSecretName", secretKeyAccessKeyID, secretKeySecretAccessKey, secretKeyLakeToken)
	doctag.Names(t, spec, "Rotate", secretKeySecretAccessKey, secretKeyLakeToken)
}
