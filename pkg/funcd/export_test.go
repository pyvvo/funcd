package funcd

import (
	"net"
	"testing"
)

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
