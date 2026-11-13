package function

import (
	"reflect"
	"strconv"
	"testing"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/ownedstore"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/testkit/doctag"
)

// The Function's field descriptions and the runtime and volume config keys name these reasons, conditions, defaults,
// the pool cap and the liveness path, so a changed one fails here until they follow (ADR-0323 Decision 4).
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
	doctag.Names(t, reflect.TypeFor[v1.NamespaceSpec](), "VolumeBudget", reasonVolumeBudget)
	cfg := reflect.TypeFor[config.Config]()
	doctag.NamesAt(t, cfg, "runtime.bootBackoffMax", v1.Duration(defaultBootBackoffMax).String())
	doctag.NamesAt(t, cfg, "runtime.livenessTimeout", livenessPath)
	doctag.NamesAt(t, cfg, "runtime.readinessTimeout", v1.Duration(defaultReadinessTimeout).String(), reasonNotAnswering)
	doctag.NamesAt(t, cfg, "volumes.capacityBytes", reasonVolumesOff)
}
