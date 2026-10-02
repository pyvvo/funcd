package v1alpha1

import (
	"runtime"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
)

// OCIPlatform is an OCI platform "<os>/<architecture>", e.g. "linux/arm64" (ADR-0145): the platform a function
// artifact was built for, or the platform a worker node runs.
type OCIPlatform string

// The platforms `funcdctl push --platform` accepts for a function artifact (ADR-0145).
const (
	PlatformLinuxAMD64 OCIPlatform = "linux/amd64"
	PlatformLinuxARM64 OCIPlatform = "linux/arm64"
)

// Validate checks the "<os>/<arch>" shape: two non-empty segments, no variant.
func (p OCIPlatform) Validate() error {
	parts := strings.Split(string(p), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fault.Invalidf("OCIPlatform.Validate", "%q is not an <os>/<arch> platform", p)
	}
	return nil
}

// OS is the platform's operating system ("" when p is not valid).
func (p OCIPlatform) OS() string {
	if p.Validate() != nil {
		return ""
	}
	os, _, _ := strings.Cut(string(p), "/")
	return os
}

// Arch is the platform's CPU architecture ("" when p is not valid).
func (p OCIPlatform) Arch() string {
	if p.Validate() != nil {
		return ""
	}
	_, arch, _ := strings.Cut(string(p), "/")
	return arch
}

// HostPlatform is the daemon's own GOOS/GOARCH.
func HostPlatform() OCIPlatform {
	return OCIPlatform(runtime.GOOS + "/" + runtime.GOARCH)
}
