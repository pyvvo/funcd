package edgetls_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	edgetls "github.com/pyvvo/funcd/internal/edge/tls"
)

// The modes that persist key material refuse an empty StorageDir rather than writing it relative to the
// working directory or a dir chosen for them.
func TestNewRequiresStorageDirForPersistingModes(t *testing.T) {
	t.Parallel()
	for _, spec := range []edgetls.Spec{
		{},
		{Mode: edgetls.ModeSelfSigned},
		{Mode: edgetls.ModeACME, Email: "ops@example.com"},
	} {
		p, err := edgetls.New(spec, nil)
		if p != nil {
			t.Cleanup(func() { _ = p.Close(t.Context()) })
		}
		require.Equal(t, fault.Invalid, fault.KindOf(err), "mode %q: %v", spec.Mode, err)
		require.ErrorContains(t, err, "storage dir")
	}
}
