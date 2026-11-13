package s3gateway

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/testkit/doctag"
)

// The s3gateway keys' descriptions copy the upload cap, the multipart budget's factor and the master's path, so a
// changed one fails here until they follow (ADR-0323 Decision 4).
func TestDocNamesTheConstant(t *testing.T) {
	cfg := reflect.TypeFor[config.Config]()
	// The tag writes the factor as a word.
	times, ok := map[int]string{2: "twice", 3: "three times"}[budgetMultipartFactor]
	require.True(t, ok, "budgetMultipartFactor %d has no word", budgetMultipartFactor)
	doctag.NamesAt(t, cfg, "s3gateway.maxUploadBytes", strconv.FormatFloat(float64(defaultMaxUpload)/(1<<30), 'f', -1, 64)+" GiB", times)
	doctag.NamesAt(t, cfg, "s3gateway.masterSecretFile", masterRel)
}
