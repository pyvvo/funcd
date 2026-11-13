package function

import (
	"reflect"
	"strconv"
	"testing"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/ownedstore"
	"github.com/pyvvo/funcd/internal/testkit/doctag"
)

// The Function's field descriptions name these reasons, conditions and the pool cap, so a changed one fails here
// until they follow (ADR-0323 Decision 4).
func TestDocNamesTheConstant(t *testing.T) {
	spec := reflect.TypeFor[v1.FunctionSpec]()
	for _, field := range []string{"KVStores", "Buckets", "SQLDatabases"} {
		doctag.Names(t, spec, field, reasonStoreNotOwned)
	}
	doctag.Names(t, spec, "Runtime", reasonRuntimeUnavailable)
	doctag.Names(t, spec, "Ingress", string(condIngressReady))
	doctag.Names(t, spec, "SecretDeclarations", ownedstore.ReasonSecretNotFound, ownedstore.ReasonSecretKeyMissing)
	doctag.Names(t, reflect.TypeFor[v1.Pooling](), "Worker", string(condPoolFull), strconv.Itoa(defaultPoolLimit))
	doctag.Names(t, reflect.TypeFor[v1.VolumeEntry](), "Ref", reasonVolumeNotFound)
}
