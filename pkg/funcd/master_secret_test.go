package funcd_test

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// ADR-0204: a master handed in with WithMasterSecret replaces WithMasterLocation's load, so nothing is created under
// the data dir; an empty one is refused.
func TestWithMasterSecretReplacesTheLoad(t *testing.T) {
	dataDir := t.TempDir()
	p, err := funcd.New(funcd.InMemory(), funcd.WithMasterLocation("", dataDir), funcd.WithMasterSecret(bytes.Repeat([]byte{1}, 32)))
	require.NoError(t, err)
	require.NoError(t, p.Shutdown(context.Background()))
	entries, err := os.ReadDir(dataDir)
	require.NoError(t, err)
	require.Empty(t, entries)

	_, err = funcd.New(funcd.InMemory(), funcd.WithMasterSecret(nil))
	require.Equal(t, fault.Invalid, fault.KindOf(err))
}
