package v1alpha1

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/testkit/doctag"
)

// scenario: doc-names-the-constant — a doc or maximum tag that copies a named constant changes with it (ADR-0323
// Decision 4). The duration tags have their own pin, TestDurationFieldDocNamesBounds, and maxKeyBytes's maximum its pin
// in kvstore_test.go. A constant of another package is pinned by that package's TestDocNamesTheConstant.
func TestScenarioDocNamesTheConstant(t *testing.T) {
	quota := reflect.TypeFor[KVStoreQuota]()
	for _, c := range []struct {
		owner       reflect.Type
		field, want string
	}{
		{quota, "MaxValueBytes", strconv.FormatInt(DefaultMaxValueBytes, 10)},
		{quota, "MaxKeyBytes", strconv.Itoa(DefaultMaxKeyBytes)},
		{reflect.TypeFor[ResourceSpec](), "CPU", strconv.FormatFloat(MinCPU, 'f', -1, 64)},
		{reflect.TypeFor[ResourceSpec](), "Memory", strings.TrimSuffix(strings.TrimPrefix(ByteSizeGrammar, "a size: "), " (512Ki, 2GiB)")},
		{reflect.TypeFor[BackupScheduleSpec](), "TTL", DefaultBackupTTL.String()},
		{reflect.TypeFor[BackupSpec](), "TTL", DefaultBackupTTL.String()},
		{reflect.TypeFor[BackupStatus](), "Phase", ReasonFoundOnTarget},
		{reflect.TypeFor[BackupStatus](), "Recipients", ReasonFoundOnTarget},
		{reflect.TypeFor[EgressPolicySpec](), "Rules", strconv.Itoa(maxEgressRules)},
		{reflect.TypeFor[EgressRule](), "To", strconv.Itoa(maxEgressDests)},
		{reflect.TypeFor[EgressRule](), "Ports", strconv.Itoa(maxEgressPorts)},
		{reflect.TypeFor[SiteRuntimeConfig](), "Values", strconv.Itoa(SiteRuntimeConfigMaxBytes)},
	} {
		doctag.Names(t, c.owner, c.field, c.want)
	}
	for _, c := range []struct {
		owner reflect.Type
		field string
	}{
		{reflect.TypeFor[SiteRuntimeConfig](), "Values"},
		{reflect.TypeFor[SiteRuntimeConfig](), "ConfigMap"},
		{reflect.TypeFor[SiteRule](), "Path"},
		{reflect.TypeFor[BlobRef](), "Prefix"},
	} {
		doctag.Names(t, c.owner, c.field, SiteRuntimeConfigFile)
	}
	f, ok := quota.FieldByName("MaxValueBytes")
	require.True(t, ok)
	require.Equal(t, strconv.FormatInt(MaxValueBytesLimit, 10), f.Tag.Get("maximum"), "KVStoreQuota.MaxValueBytes maximum")
}
