package s3gateway

import v1 "github.com/pyvvo/funcd/api/types/v1alpha1"

// DecodeAccess exposes decodeAccess to the external test package.
func DecodeAccess(access string) (kind v1.Kind, ns, name string, ok bool) {
	return decodeAccess(access)
}
