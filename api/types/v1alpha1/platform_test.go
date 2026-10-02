package v1alpha1_test

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

func TestOCIPlatformValidate(t *testing.T) {
	require.NoError(t, v1.PlatformLinuxARM64.Validate())
	require.NoError(t, v1.OCIPlatform("darwin/arm64").Validate())
	for _, bad := range []v1.OCIPlatform{"", "linux", "/arm64", "linux/", "linux/arm64/v8"} {
		err := bad.Validate()
		require.Error(t, err, "%q", bad)
		require.Equal(t, fault.Invalid, fault.KindOf(err))
	}
	require.Equal(t, "linux", v1.PlatformLinuxAMD64.OS())
	require.Equal(t, "amd64", v1.PlatformLinuxAMD64.Arch())
	require.Empty(t, v1.OCIPlatform("linux").Arch())
	require.Equal(t, v1.OCIPlatform(runtime.GOOS+"/"+runtime.GOARCH), v1.HostPlatform())
}
