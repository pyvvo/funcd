package lake

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/ownedstore"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/testkit/doctag"
)

// The Lake's field descriptions name these reasons, this environment key and the Secret read action, and the config
// keys name its condition, its engine's runtime and the engine's stop wait, so a changed one fails here until they
// follow (ADR-0323 Decision 4).
func TestDocNamesTheConstant(t *testing.T) {
	spec := reflect.TypeFor[v1.LakeSpec]()
	doctag.Names(t, spec, "Location", reasonPrefixNotOwned)
	doctag.Names(t, spec, "Buckets", reasonStoreNotOwned)
	doctag.Names(t, spec, "ConfigMaps", reasonStoreNotOwned)
	doctag.Names(t, spec, "SecretDeclarations", ownedstore.ReasonSecretNotFound, ownedstore.ReasonSecretKeyMissing)
	bindings := reflect.TypeFor[v1.LakeBindings]()
	doctag.Names(t, bindings, "Config", quackTokenEnvKey)
	doctag.Names(t, bindings, "Secret", quackTokenEnvKey, string(auth.ActionSecretRead))
	cfg := reflect.TypeFor[config.Config]()
	doctag.NamesAt(t, cfg, "lake.catalogMaxSize", string(condCatalogSaved))
	doctag.NamesAt(t, cfg, "runtime.containerd.imageOverride", DuckDBRuntime)
	// The engine's stop wait is a constant of its shim.
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "images", "runtime", "duckdb", "shim.py"))
	require.NoError(t, err)
	m := regexp.MustCompile(`(?m)^STOP_WAIT_SECONDS = ([0-9.]+)$`).FindSubmatch(src)
	require.NotNil(t, m, "shim.py sets no STOP_WAIT_SECONDS")
	wait, err := strconv.ParseFloat(string(m[1]), 64)
	require.NoError(t, err)
	doctag.NamesAt(t, cfg, "runtime.containerd.stopGrace", v1.Duration(wait*float64(time.Second)).String())
}
