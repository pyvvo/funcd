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
	} {
		doctag.Names(t, c.owner, c.field, c.want)
	}
	f, ok := quota.FieldByName("MaxValueBytes")
	require.True(t, ok)
	require.Equal(t, strconv.FormatInt(MaxValueBytesLimit, 10), f.Tag.Get("maximum"), "KVStoreQuota.MaxValueBytes maximum")
}
