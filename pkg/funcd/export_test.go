package funcd

import (
	"net"
	"testing"
)

// FreeLoopbackAddr hands the S3 gateway's port reservation to the external tests.
func FreeLoopbackAddr(t *testing.T) string {
	t.Helper()
	return freeLoopbackAddr(t)
}

// TakenPortReserve hands the #288 bind collision to the external tests.
func TakenPortReserve() (reserve func(*testing.T) string, taken func() net.Listener) {
	return takenPortReserve()
}

// StartWithS3Gateway hands the #288 bind-collision retry to the external tests: build assembles a platform whose S3
// gateway listens on addr and that logs through logger, from which the retry reads the gateway's Run error.
func StartWithS3Gateway(t *testing.T, reserve func(*testing.T) string,
	build func(addr string, logger Option) (*Platform, error)) (*Platform, string) {
	t.Helper()
	logger, serve := platformRun()
	return startS3Gateway(t, reserve, func(addr string) (*Platform, error) { return build(addr, logger) }, serve)
}

// WithBucketQuotaForTest sets the per-namespace Bucket count cap (ADR-0080), which has no public option, so
// the ADR-0147 quota race runs at a small cap.
func WithBucketQuotaForTest(maxPerNamespace int) Option {
	return func(c *config) error { c.bucketMaxPerNamespace = maxPerNamespace; return nil }
}
