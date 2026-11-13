package funcd

import (
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	platformconfig "github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/testkit/doctag"
)

// The config keys' descriptions copy these defaults, bounds and the retention sweep's period, so a changed one fails
// here until they follow (ADR-0323 Decision 4).
func TestDocNamesTheConstant(t *testing.T) {
	cfg := reflect.TypeFor[platformconfig.Config]()
	doctag.NamesAt(t, cfg, "runtime.livenessTimeout", v1.Duration(minDefaultLivenessTimeout).String())
	doctag.NamesAt(t, cfg, "controller.retryBackoffMax", "at least "+v1.Duration(minRetryBackoffMax).String())
	doctag.NamesAt(t, cfg, "workflow.defaultRetryBackoff", "at most "+v1.Duration(maxDefaultRetryBackoff).String())
	doctag.NamesAt(t, cfg, "invoke.maxNestedInFlight", strconv.Itoa(defaultNestedInFlightCap))
	doctag.NamesAt(t, cfg, "kvstore.maxStoresPerNamespace", strconv.Itoa(defaultKVStoresPerNamespace))
	doctag.NamesAt(t, cfg, "sql.maxDatabasesPerNamespace", strconv.Itoa(defaultSQLDatabasesPerNamespace))
	doctag.NamesAt(t, cfg, "funclog.maxRecordBytes", strconv.Itoa(minRecordBytes))
	// The retention keys write the sweep's period as the name of its unit.
	unit, ok := map[time.Duration]string{time.Minute: "minute", time.Hour: "hour"}[maxRetentionSweep]
	require.True(t, ok, "maxRetentionSweep %s is neither a minute nor an hour", maxRetentionSweep)
	doctag.NamesAt(t, cfg, "workflow.retention", "every "+unit)
	doctag.NamesAt(t, cfg, "eventing.deadletter.retention", "every "+unit)
}
