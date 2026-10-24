package s3gateway

import (
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/durable"
)

// DecodeAccess exposes decodeAccess to the external test package.
func DecodeAccess(access string) (kind v1.Kind, ns, name string, ok bool) {
	return decodeAccess(access)
}

// WriteMasterTraced writes the master secret as LoadOrCreateMaster and WriteMaster do, reporting each durable step to
// trace (#1244).
func WriteMasterTraced(path string, secret []byte, trace func(durable.Op, string)) error {
	return writeMaster(durable.Disk{Trace: trace}, path, secret)
}
