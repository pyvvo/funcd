package funcd

import (
	"net"
	"testing"
	"time"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
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

// WithGatewayReadTimeoutForTest sets the data-plane listener's read timeout, which has no public option, so a slow
// body answers its 400 (#90) within a test.
func WithGatewayReadTimeoutForTest(d time.Duration) Option {
	return func(c *config) error { c.gatewayReadTimeout = d; return nil }
}

// SetAnswerRecorderForTest is the test binary's hook of ADR-0329 Decision 4: every platform New builds from then on
// reports each answer of its control plane and its gateway to record, by operation ID and status. nil stops it. A
// server never sets it.
func SetAnswerRecorderForTest(record func(operationID string, status int)) {
	if record == nil {
		answerRecorder.Store(nil)
		return
	}
	answerRecorder.Store(&record)
}

// SetFrugalStoresForTest is the test binary's hook of the in-memory stores: every platform New builds from then on
// opens its in-memory event and run stores with the memtables and caches of their on-disk profile. A server never sets
// it.
func SetFrugalStoresForTest(on bool) { frugalStores.Store(on) }

// LocalAPISocketForTest hands the external tests the socket of the local API that serves the Function ns/fn, the
// path the reconciler sets as its FUNCD_INVOKE_SOCKET; the cause matrices of ADR-0330 dial it.
func LocalAPISocketForTest(p *Platform, ns v1.NamespaceName, fn v1.ObjectName) (string, error) {
	return p.exec.sockets.SocketFor(ns, fn)
}
