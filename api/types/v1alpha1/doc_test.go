package v1alpha1

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// scenario: doc-names-the-constant — a doc or maximum tag that copies a named constant changes with it (ADR-0323
// Decision 4). The duration tags have their own pin, TestDurationFieldDocNamesBounds, and maxKeyBytes's maximum its pin
// in kvstore_test.go.
func TestScenarioDocNamesTheConstant(t *testing.T) {
	for _, c := range []struct {
		field, tag, want string
	}{
		{"MaxValueBytes", "doc", strconv.FormatInt(DefaultMaxValueBytes, 10)},
		{"MaxKeyBytes", "doc", strconv.Itoa(DefaultMaxKeyBytes)},
		{"MaxValueBytes", "maximum", strconv.FormatInt(MaxValueBytesLimit, 10)},
	} {
		f, ok := reflect.TypeFor[KVStoreQuota]().FieldByName(c.field)
		require.True(t, ok, c.field)
		if got := f.Tag.Get(c.tag); c.tag == "doc" {
			require.Contains(t, got, c.want, "KVStoreQuota.%s doc", c.field)
		} else {
			require.Equal(t, c.want, got, "KVStoreQuota.%s %s", c.field, c.tag)
		}
	}
}
