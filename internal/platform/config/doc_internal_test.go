package config

import (
	"reflect"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/testkit/doctag"
)

// The config keys' descriptions copy these constants of the config and of the API, so a changed one fails here until
// they follow (ADR-0323 Decision 4). A constant of another package is pinned by that package's TestDocNamesTheConstant.
func TestDocNamesTheConstant(t *testing.T) {
	cfg := reflect.TypeFor[Config]()
	dur := func(d time.Duration) string { return v1.Duration(d).String() }
	// count writes n bytes as a number of unit, with no rounding: 8 for 8<<20 in MiB.
	count := func(n, unit int64) string { return strconv.FormatFloat(float64(n)/float64(unit), 'f', -1, 64) }
	doctag.NamesAt(t, cfg, "storage.quarantineRetention", dur(MinQuarantineRetention), dur(DefaultQuarantineRetention))
	doctag.NamesAt(t, cfg, "storage.minFreeSize", DefaultStorageMinFreeSize)
	doctag.NamesAt(t, cfg, "kvstore.sweepInterval", dur(MinKVSweepInterval), dur(DefaultKVSweepInterval))
	doctag.NamesAt(t, cfg, "kvstore.backup.interval", dur(defaultKVBackupInterval))
	doctag.NamesAt(t, cfg, "kvstore.backup.rebaseline", dur(defaultKVRebaseline))
	doctag.NamesAt(t, cfg, "kvstore.backup.rebaselineRetry", dur(defaultKVRebaselineRetry))
	doctag.NamesAt(t, cfg, "kvstore.backup.chunkBytes", strconv.Itoa(kvChunkBytesMax), "("+count(kvChunkBytesMax, 1<<20)+" MiB)")
	doctag.NamesAt(t, cfg, "s3gateway.maxUploadBytes", count(defaultS3MaxUpload, 1<<30)+" GiB")
	doctag.NamesAt(t, cfg, "lake.saveInterval", dur(MinLakeSaveInterval), dur(MaxLakeSaveInterval))
	doctag.NamesAt(t, cfg, "lake.catalogMaxSize", DefaultLakeCatalogMaxSize)
	doctag.NamesAt(t, cfg, "lake.scratchSize", count(lakeScratchMargin, 1<<20)+"MiB", count(MinLakeScratchBytes, 1<<30)+"GiB")
	doctag.NamesAt(t, cfg, "functions.defaultResources.cpu", strconv.FormatFloat(v1.MinCPU, 'f', -1, 64))
	doctag.NamesAt(t, cfg, "invoke.defaultTimeout", strconv.FormatFloat(v1.DefaultInvokeTimeout.Seconds(), 'f', -1, 64)+"s",
		dur(v1.MaxInvokeTimeout))
	doctag.NamesAt(t, cfg, "workflow.defaultRetryBackoff", v1.MaxRetryBackoff.String())
	doctag.NamesAt(t, cfg, "workloadBackup.maxListedKeys", string(v1.ConditionTargetSynced), v1.ReasonListFailed)
	units := regexp.MustCompile(`digits and (.+) \(`).FindStringSubmatch(v1.ByteSizeGrammar)
	require.Len(t, units, 2, "v1.ByteSizeGrammar names no units: %q", v1.ByteSizeGrammar)
	doctag.NamesAt(t, cfg, "volumes.capacity", units[1])
}
